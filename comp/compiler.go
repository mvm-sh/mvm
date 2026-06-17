// Package comp implements a byte code generator targeting the vm.
package comp

import (
	"errors"
	"fmt"
	"go/constant"
	"go/token"
	"maps"
	"math"
	"os"
	"path"
	"reflect"
	"runtime"
	rtdebug "runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

const debug = false

// Debug env flags, read once: generate runs per declaration.
var (
	debugTokens = os.Getenv("MVM_DEBUG_TOKENS") != ""
	debugPanic  = os.Getenv("MVM_DEBUG_PANIC") != ""
)

var builtinDeferOp = map[string]vm.Op{
	"print":   vm.Print,
	"println": vm.Println,
	"close":   vm.ChanClose,
	"delete":  vm.DeleteMap,
	"copy":    vm.CopySlice,
	"clear":   vm.Clear,
	"panic":   vm.Panic,
}

// Compiler represents the state of a compiler.
type Compiler struct {
	*goparser.Parser
	vm.Code            // produced code, to fill VM with
	Data    []vm.Value // produced data, will be at the bottom of VM stack
	Entry   int        // offset in Code to start execution from

	FuncRanges []vm.FuncRange // bytecode [Start, End) range for every compiled function, in source order

	strings      map[string]int              // locations of strings in Data
	methodIDs    map[string]int              // global method ID by method name
	methodRtype  map[int]reflect.Type        // func type (no receiver) by global method ID
	typeIdxs     map[*vm.Type]int            // dedup cache for typeIndex, keyed by mvm type pointer
	typeSyms     map[*vm.Type]*symbol.Symbol // dedup cache for typeSym (type-descriptor slot), keyed by mvm type pointer
	zeroTypeIdxs map[*vm.Type]int            // dedup cache: Data slot holding a zero VALUE of a type (Fnew source), keyed by mvm type pointer
	zeroSlotType map[int]*vm.Type            // reverse of zeroTypeIdxs: *Type by slot index, for *Type-identity slot compares
	labelAtPos   map[int]bool                // code positions occupied by Labels; consulted by fuseCmpJump

	pendingTypeSlots []pendingSlot // Data slots whose value is deferred until FillTypeSlots

	genStart int // code offset at the start of the current generate() pass; bounds resolveLabel staleness and the remove*/patchNilFnewLen retraction scans

	// Defer brackets the target package's var inits so eval runs imported init()
	// funcs before them (Go init order); zero when no reordering is needed.
	Defer VarDeferral

	// LenientCompile turns a codegen panic into a (rolled-back) error instead of
	// crashing. Off by default; `mvm test` enables it for the external-test unit.
	LenientCompile bool
}

// VarDeferral enforces Go init order: each package's var inits and init()s run
// before any importer's var inits. mvm otherwise flattens to [all var inits][all
// init()s][main], breaking when an imported var init reads what another import's
// init() set. finishCompile lays out
//
//	[pkg0 vars][Jump0][pkg1 vars][Jump1]...[pkgN vars][JumpN][rest]
//
// and evalCompiled patches each JumpI through pkgI's init() shims to pkg(I+1)'s
// vars (the last to rest): pkg0 vars -> pkg0 inits -> ... -> rest -> main.
type VarDeferral struct {
	Active bool
	Groups []DeferGroup
}

// DeferGroup is one package's init bracket. Its var inits precede JumpPos in
// Code; NumInits consecutive entries of this Eval's InitFuncs are its init()s.
type DeferGroup struct {
	JumpPos  int
	NumInits int
}

// pendingSlot is a Data slot deferred by typeSlotValue, filled by FillTypeSlots.
type pendingSlot struct {
	idx        int
	typ        *vm.Type
	descriptor bool // true: TypeValue (type descriptor); false: NewValue (zero value)
}

// NewCompiler returns a new compiler state for a given scanner.
func NewCompiler(spec *lang.Spec) *Compiler {
	return &Compiler{
		Parser:       goparser.NewParser(spec, true),
		Entry:        -1,
		strings:      map[string]int{},
		methodIDs:    map[string]int{},
		methodRtype:  map[int]reflect.Type{},
		typeIdxs:     map[*vm.Type]int{},
		typeSyms:     map[*vm.Type]*symbol.Symbol{},
		zeroTypeIdxs: map[*vm.Type]int{},
		zeroSlotType: map[int]*vm.Type{},
		labelAtPos:   map[int]bool{},
	}
}

func ifaceMethodSig(ifaceTyp *vm.Type, methodName string) reflect.Type {
	if rm, ok := vm.MaterializeRtype(ifaceTyp).MethodByName(methodName); ok {
		return rm.Type
	}
	for _, im := range ifaceTyp.IfaceMethods {
		if im.Name != methodName {
			continue
		}
		if im.Rtype != nil {
			return im.Rtype
		}
		// A body-local anonymous interface (a type-assertion / type-switch target)
		// is parsed after materializeIfaceMethods, so its Rtype is still the nil
		// goparser left; materialize it from the symbolic Sig on demand.
		if im.Sig != nil {
			return vm.MaterializeRtype(im.Sig)
		}
	}
	return nil
}

// resolveIfaceMethodSym builds a Symbol carrying the bound method signature
// for an interface-dispatch site.
func (c *Compiler) resolveIfaceMethodSym(ifaceTyp *vm.Type, methodName string) *symbol.Symbol {
	ifaceSig := ifaceMethodSig(ifaceTyp, methodName)
	methodSym := c.findConcreteFuncSym(methodName)
	if methodSym != nil && ifaceSig != nil && !concreteMatchesIface(methodSym.Type, ifaceSig) {
		methodSym = nil
	}
	if methodSym != nil {
		return methodSym
	}
	// Prefer the interpreted symbolic signature: it preserves named result types
	// (e.g. an interface-returning method) regardless of reflect materialization
	// order. ifaceSig (Out via reflect) may have erased a not-yet-materialized
	// named-interface return to bare interface{}, hiding its method set.
	if interpSig := ifaceMethodInterpSig(ifaceTyp, methodName); interpSig != nil && len(interpSig.Returns) > 0 {
		return &symbol.Symbol{Kind: symbol.Value, Type: interpSig}
	}
	if ifaceSig != nil {
		return &symbol.Symbol{Kind: symbol.Value, Type: &vm.Type{Rtype: ifaceSig}}
	}
	return nil
}

// ifaceMethodInterpSig returns the interpreted symbolic signature (a func *vm.Type
// with symbolic Returns) for methodName on an interface type, or nil for a native
// interface whose methods carry only a reflect Rtype.
func ifaceMethodInterpSig(ifaceTyp *vm.Type, methodName string) *vm.Type {
	ifaceTyp.EnsureIfaceMethods()
	for _, im := range ifaceTyp.IfaceMethods {
		if im.Name == methodName {
			return im.Sig
		}
	}
	return nil
}

func concreteMatchesIface(concrete *vm.Type, ifaceSig reflect.Type) bool {
	if ifaceSig == nil || concrete == nil || concrete.Kind() != reflect.Func || vm.MaterializeRtype(concrete) == nil {
		return false
	}
	rt := vm.MaterializeRtype(concrete)
	if rt.NumIn() != ifaceSig.NumIn() || rt.NumOut() != ifaceSig.NumOut() {
		return false
	}
	for i := range rt.NumIn() {
		if rt.In(i) != ifaceSig.In(i) {
			return false
		}
	}
	for i := range rt.NumOut() {
		if rt.Out(i) != ifaceSig.Out(i) {
			return false
		}
	}
	return true
}

func (c *Compiler) aliasTargetTopLevel(pkgPath string) {
	prefix := pkgPath + "."
	for k, s := range c.Symbols {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		short := k[len(prefix):]
		if short == "" || strings.ContainsAny(short, "./*") {
			continue
		}
		if _, exists := c.Symbols[short]; exists {
			continue
		}
		c.Symbols[short] = s // mvm:symkey-ok: deliberate bare-key alias for the test driver
		c.Seg.Add(short)
	}
}

// Compile parses src and generates code and data, or returns a non-nil error.
// Code and data are added incrementally in c.Code and C.Data.
func (c *Compiler) Compile(name, src string) (err error) {
	c.ResetUnitLabels()
	// On failure, roll back to the pre-compile state so a half-compiled unit
	// can't corrupt the next compile on this reused Compiler.
	snap := c.SnapshotUnit()
	cg := c.snapshotCodegen()
	defer func() {
		if err != nil {
			c.RestoreUnit(snap)
			c.restoreCodegen(cg)
			c.resetDanglingIndexes(cg.data)
		}
	}()
	// An empty src means a package-path target (`mvm run`/`mvm test <path>`):
	// qualify the target's top-level symbols at pkg-qualified keys (like importSrc)
	// so deferred bodies and external test units resolve members like maps.Clone.
	var remaining []goparser.DeferredDecl
	// targetTag identifies the unit's own (target) package: its decls are tagged
	// with the import-path name for a package-path target, "" for a source/REPL
	// target. finishCompile uses it to defer the target's var inits past imports.
	targetTag := ""
	if src == "" {
		targetTag = name
		restore := c.WithImportingPkg(name)
		remaining, err = c.ParseAll(name, src)
		restore()
		if err == nil {
			c.aliasTargetTopLevel(name)
		}
	} else {
		remaining, err = c.ParseAll(name, src)
	}
	if err != nil {
		return err
	}
	return c.finishCompile(remaining, targetTag)
}

// CompileFiles compiles several in-memory source files as a single main-package
// unit (Phase 1 across all files, then Phase 2 code-gen), so top-level symbols
// declared in one file are visible to the others regardless of file or
// declaration order. Backs `mvm run f1.go f2.go ...`.
func (c *Compiler) CompileFiles(sources []goparser.PackageSource) (err error) {
	c.ResetUnitLabels()
	snap := c.SnapshotUnit()
	cg := c.snapshotCodegen()
	defer func() {
		// Recover only when lenient; else a real bug crashes with its own stack.
		if c.LenientCompile {
			if r := recover(); r != nil {
				err = fmt.Errorf("compile panic: %v", r)
			}
		}
		if err != nil {
			c.RestoreUnit(snap)
			c.restoreCodegen(cg)
			c.resetDanglingIndexes(cg.data)
		}
	}()
	var remaining []goparser.DeferredDecl
	remaining, err = c.ParseAllFiles(sources)
	if err != nil {
		return err
	}
	// A source-file / REPL unit's own package decls are tagged "".
	return c.finishCompile(remaining, "")
}

// codegenSnap records the Compiler's Phase-2 output and dedup caches for
// rollback: slices revert by length, caches by restore. The index-keyed caches
// (strings, zeroSlotType, labelAtPos) must be restored since the next unit
// reuses those Code/Data indices; the rest are restored for uniformity.
type codegenSnap struct {
	code, data, funcRanges, pendingSlots, entry int
	strings                                     map[string]int
	methodIDs                                   map[string]int
	methodRtype                                 map[int]reflect.Type
	typeIdxs                                    map[*vm.Type]int
	typeSyms                                    map[*vm.Type]*symbol.Symbol
	zeroTypeIdxs                                map[*vm.Type]int
	zeroSlotType                                map[int]*vm.Type
	labelAtPos                                  map[int]bool
}

func (c *Compiler) snapshotCodegen() codegenSnap {
	return codegenSnap{
		code: len(c.Code), data: len(c.Data), funcRanges: len(c.FuncRanges),
		pendingSlots: len(c.pendingTypeSlots), entry: c.Entry,
		strings:      maps.Clone(c.strings),
		methodIDs:    maps.Clone(c.methodIDs),
		methodRtype:  maps.Clone(c.methodRtype),
		typeIdxs:     maps.Clone(c.typeIdxs),
		typeSyms:     maps.Clone(c.typeSyms),
		zeroTypeIdxs: maps.Clone(c.zeroTypeIdxs),
		zeroSlotType: maps.Clone(c.zeroSlotType),
		labelAtPos:   maps.Clone(c.labelAtPos),
	}
}

// resetDanglingIndexes clears global-slot indexes pointing past the truncated
// Data. RestoreUnit cannot undo in-place mutation of pre-existing symbols
// (shared pointers), so a slot allocated by the failed unit (e.g. the Period
// case allocating a Data slot for an imported const) would otherwise stay
// recorded and alias whatever the retry compiles into that slot.
func (c *Compiler) resetDanglingIndexes(dataLen int) {
	for _, s := range c.Symbols {
		if s.Index != symbol.UnsetAddr && s.Index >= dataLen {
			s.Index = symbol.UnsetAddr
		}
	}
}

func (c *Compiler) restoreCodegen(s codegenSnap) {
	c.Code = c.Code[:s.code]
	c.Data = c.Data[:s.data]
	c.FuncRanges = c.FuncRanges[:s.funcRanges]
	c.pendingTypeSlots = c.pendingTypeSlots[:s.pendingSlots]
	c.Entry = s.entry
	c.strings = s.strings
	c.methodIDs = s.methodIDs
	c.methodRtype = s.methodRtype
	c.typeIdxs = s.typeIdxs
	c.typeSyms = s.typeSyms
	c.zeroTypeIdxs = s.zeroTypeIdxs
	c.zeroSlotType = s.zeroSlotType
	c.labelAtPos = s.labelAtPos
}

// isInitDecl reports whether a deferred decl is a `func init()` declaration.
func isInitDecl(toks goparser.Tokens) bool {
	return len(toks) >= 2 && toks[0].Tok == lang.Func &&
		toks[1].Tok == lang.Ident && toks[1].Str == "init"
}

// finishCompile runs Phase 2 over the deferred declarations: allocate global
// slots, compile var initializers first (so all var types resolve) then func
// bodies and expression statements, then propagate embedded methods. Shared by
// Compile and CompileFiles.
func (c *Compiler) finishCompile(remaining []goparser.DeferredDecl, targetTag string) error {
	c.allocGlobalSlots()
	c.preregisterMethods()
	// Materialize interface method signatures now -- after the method pre-pass, so a
	// named type referenced in a signature reserves its method-bearing identity
	// rather than getting stamped methodless (goparser leaves IfaceMethod.Rtype nil).
	c.materializeIfaceMethods()
	// Promote embedded-interface methods now (before any body-compile
	// materialization) so a struct embedding an interface carries its promoted
	// methods when the reserve gate runs; the post-body call below additionally
	// promotes embedded value-type methods once their code addresses resolve.
	c.propagateEmbeddedMethods()

	// Group var inits by package for Go init order (see VarDeferral). Package
	// order is first appearance in remaining, which is topological: imports
	// precede importers and the var sort only reorders along dependency edges.
	// A package's init()s are the consecutive InitFuncs entries for its rest
	// init decls (parse order). Vars compile before funcs, so var types resolve.
	c.Defer = VarDeferral{}
	varsByPkg := map[string][]goparser.DeferredDecl{}
	initsByPkg := map[string]int{}
	var pkgOrder []string
	seenPkg := map[string]bool{}
	var rest []goparser.DeferredDecl
	importedInits := 0
	totalVars := 0
	for _, decl := range remaining {
		if len(decl.Toks) == 0 {
			continue
		}
		if !seenPkg[decl.PkgPath] {
			seenPkg[decl.PkgPath] = true
			pkgOrder = append(pkgOrder, decl.PkgPath)
		}
		if decl.Toks[0].Tok == lang.Var {
			varsByPkg[decl.PkgPath] = append(varsByPkg[decl.PkgPath], decl)
			totalVars++
			continue
		}
		rest = append(rest, decl)
		if isInitDecl(decl.Toks) {
			initsByPkg[decl.PkgPath]++
			if decl.PkgPath != targetTag {
				importedInits++
			}
		}
	}
	// Emit init-order jumps only when an imported init() could be observed by a
	// var init; otherwise the flat order is already Go-correct. Var inits compile
	// in the same per-package order either way.
	deferVars := importedInits > 0 && totalVars > 0
	c.Defer.Active = deferVars

	for _, pkg := range pkgOrder {
		for _, decl := range varsByPkg[pkg] {
			if err := c.compileDeferred(decl); err != nil {
				return err
			}
		}
		if deferVars && (len(varsByPkg[pkg]) > 0 || initsByPkg[pkg] > 0) {
			c.Defer.Groups = append(c.Defer.Groups, DeferGroup{
				JumpPos:  len(c.Code),
				NumInits: initsByPkg[pkg],
			})
			c.emit(goparser.Token{}, vm.Jump, 0) // patched in evalCompiled -> this pkg's init()s
		}
	}
	for _, decl := range rest {
		if err := c.compileDeferred(decl); err != nil {
			return err
		}
	}
	// Drain generic-instance bodies queued above, looping since one can trigger more.
	for {
		insts := c.TakeInstanceDecls()
		if len(insts) == 0 {
			break
		}
		for _, dd := range insts {
			if err := c.compileInstance(dd); err != nil {
				return err
			}
		}
	}
	c.propagateEmbeddedMethods()
	return nil
}

// compileInstance code-gens one already-parsed generic-instance body in its own
// generate call under its template's package, so labels resolve consistently.
func (c *Compiler) compileInstance(dd goparser.DeferredDecl) error {
	c.CompilingPkg = dd.PkgPath
	defer func() { c.CompilingPkg = "" }()
	return c.generate(dd.Toks)
}

// preregisterMethods records every method onto its receiver type's Methods
// table before Phase-2 body compile materializes any type.
func (c *Compiler) preregisterMethods() {
	keys := make([]string, 0, len(c.Symbols))
	for key := range c.Symbols {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		s := c.Symbols[key]
		if s.Kind != symbol.Func || s.Type == nil || s.Type.Kind() != reflect.Func ||
			strings.ContainsRune(key, '#') {
			continue
		}
		body := strings.TrimPrefix(key, "*")
		dot := strings.LastIndex(body, ".")
		if dot < 0 {
			continue // free function: no receiver
		}
		ts, ok := c.Symbols[body[:dot]]
		if !ok || ts.Kind != symbol.Type || ts.Type == nil {
			continue // typeKey is a package or unknown, not a receiver type
		}
		id := c.methodID(body[dot+1:])
		for len(ts.Type.Methods) <= id {
			ts.Type.Methods = append(ts.Type.Methods, vm.Method{Index: -1})
		}
		if m := &ts.Type.Methods[id]; !m.IsResolved() && m.Sig == nil {
			*m = vm.Method{Index: -1, PtrRecv: strings.HasPrefix(key, "*"), Sig: s.Type}
		}
	}
}

// materializeIfaceMethods fills IfaceMethod.Rtype from its symbolic Sig for every
// interface reachable from the symbol table.
func (c *Compiler) materializeIfaceMethods() {
	seen := map[*vm.Type]bool{}
	var visit func(t *vm.Type)
	visit = func(t *vm.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		visit(t.ElemType)
		visit(t.KeyType)
		visit(t.Base)
		for _, f := range t.Fields {
			visit(f)
		}
		for _, p := range t.Params {
			visit(p)
		}
		for _, r := range t.Returns {
			visit(r)
		}
		for _, e := range t.Embedded {
			visit(e.Type)
		}
		for i := range t.IfaceMethods {
			vm.MaterializeIfaceMethod(&t.IfaceMethods[i])
		}
	}
	for _, sym := range c.Symbols {
		visit(sym.Type)
	}
}

func (c *Compiler) compileDeferred(dd goparser.DeferredDecl) error {
	c.CompilingPkg = dd.PkgPath
	defer func() { c.CompilingPkg = "" }()
	return c.compileDecl(dd.Toks)
}

func (c *Compiler) propagateEmbeddedMethods() {
	seen := map[*vm.Type]bool{}
	var visit func(t *vm.Type)
	visit = func(t *vm.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		for _, emb := range t.Embedded {
			visit(emb.Type)
		}
		for _, emb := range t.Embedded {
			embType := emb.Type
			if embType == nil {
				continue
			}
			for id, m := range embType.Methods {
				if !m.IsResolved() {
					continue
				}
				if id < len(t.Methods) && t.Methods[id].IsResolved() {
					continue
				}
				newPath := append([]int{emb.FieldIdx}, m.Path...)
				for len(t.Methods) <= id {
					t.Methods = append(t.Methods, vm.Method{Index: -1})
				}
				t.Methods[id] = vm.Method{Index: m.Index, Path: newPath, PtrRecv: m.PtrRecv, EmbedIface: m.EmbedIface, Rtype: m.Rtype, Sig: m.Sig}
			}
			// An embedded interface promotes its methods as EmbedIface dispatch
			// (its method set lives in IfaceMethods, not Methods). Recording them
			// on the value type lets synth attach build a rtype satisfying the
			// promoted interface (e.g. `struct{ error }` -> error).
			if embType.IsInterface() {
				embType.EnsureIfaceMethods()
				for _, im := range embType.IfaceMethods {
					id := c.methodID(im.Name)
					if id < len(t.Methods) && t.Methods[id].IsResolved() {
						continue
					}
					for len(t.Methods) <= id {
						t.Methods = append(t.Methods, vm.Method{Index: -1})
					}
					t.Methods[id] = vm.Method{Index: -1, Path: []int{emb.FieldIdx}, EmbedIface: true, Rtype: im.Rtype, Sig: im.Sig}
				}
			}
		}
	}
	for _, sym := range c.Symbols {
		if sym.Kind == symbol.Type && sym.Type != nil {
			visit(sym.Type)
		}
	}
}

func (c *Compiler) compileDecl(decl goparser.Tokens) error {
	toks, err := c.ParseOneStmt(decl)
	if err != nil {
		return err
	}
	return c.generate(toks)
}

func (c *Compiler) allocGlobalSlots() {
	for _, s := range c.Symbols {
		if s.Index != symbol.UnsetAddr {
			continue
		}
		switch s.Kind {
		case symbol.Func:
			s.Index = len(c.Data)
			c.Data = append(c.Data, s.Value)
		case symbol.Var:
			s.Index = len(c.Data)
			v := s.Value
			if data, ok := c.EmbedBytes(s.Name); ok && s.Type != nil {
				// //go:embed var: the file bytes are the initial (and final) slot value.
				v = c.embedValue(s.Type, data)
			} else if s.Type != nil {
				// Re-allocate via vm.NewValue at the current rtype: an earlier
				// vm.NewValue call (e.g. addSymVar at parse time) may have used
				// a struct-placeholder rtype whose Size has since grown via
				// SetFields, leaving s.Value's backing memory too small to
				// hold the finalized struct. Reads past the original size hit
				// adjacent memory (the language.Und Tag pExt-garbage bug).
				v = c.typeSlotValue(s.Index, s.Type, false)
			}
			c.Data = append(c.Data, v)
		}
	}
}

func (c *Compiler) methodID(name string) int {
	if id, ok := c.methodIDs[name]; ok {
		return id
	}
	id := len(c.methodIDs)
	c.methodIDs[name] = id
	return id
}

// populateIfaceMethodIDs assigns global method IDs to an interface type's
// IfaceMethods so vm.Type.Implements / ResolveMethodType can match interpreted
// methods at runtime (type assertions and type switches). Idempotent: methodID
// is deterministic and IDs are only assigned when unset (ID < 0). Call after
// typ.EnsureIfaceMethods().
func (c *Compiler) populateIfaceMethodIDs(typ *vm.Type) {
	if typ.IsInterface() && len(typ.IfaceMethods) > 0 && typ.IfaceMethods[0].ID < 0 {
		for i, im := range typ.IfaceMethods {
			typ.IfaceMethods[i].ID = c.methodID(im.Name)
		}
	}
}

// MethodNames returns the reverse mapping of global method IDs to names.
func (c *Compiler) MethodNames() []string {
	names := make([]string, len(c.methodIDs))
	for name, id := range c.methodIDs {
		names[id] = name
	}
	return names
}

// MethodFuncTypes returns a slice of bound-method func types (no receiver)
// indexed by global method ID. Entries are nil when no interface declaration
// recorded the signature (e.g. methods on native types resolved purely via
// reflect).
func (c *Compiler) MethodFuncTypes() []reflect.Type {
	ft := make([]reflect.Type, len(c.methodIDs))
	for id, rtype := range c.methodRtype {
		if id < len(ft) {
			ft[id] = rtype
		}
	}
	return ft
}

// methodExprType builds func(recv, params...) rets from a receiver type and the
// method's receiver-less signature; nil if inner is not a usable func type.
func (c *Compiler) methodExprType(recv, inner *vm.Type) *vm.Type {
	if recv == nil || inner == nil || inner.Kind() != reflect.Func {
		return nil
	}
	params := make([]*vm.Type, 0, inner.NumIn()+1)
	params = append(params, recv)
	for i := 0; i < inner.NumIn(); i++ {
		params = append(params, inner.ParamType(i))
	}
	rets := make([]*vm.Type, inner.NumOut())
	for i := range rets {
		rets[i] = inner.ReturnType(i)
	}
	variadic := inner.Variadic
	if inner.Rtype != nil {
		variadic = inner.Rtype.IsVariadic()
	}
	return vm.SymFunc(params, rets, variadic)
}

func (c *Compiler) typeIndex(typ *vm.Type) int {
	if i, ok := c.typeIdxs[typ]; ok {
		return i
	}
	i := len(c.Data)
	c.Data = append(c.Data, vm.ValueOf(typ))
	c.typeIdxs[typ] = i
	return i
}

// rtype is the single seam through which comp obtains a type's reflect.Type.
// goparser builds types symbolically; comp materializes the rtype from the
// symbolic graph on first use, caching it on the *Type.
func (c *Compiler) rtype(typ *vm.Type) reflect.Type {
	return vm.MaterializeRtype(typ)
}

// typeSlotValue returns the value for Data slot idx holding typ: a zero value
// (descriptor=false) or a type descriptor (descriptor=true). An un-materialized
// typ is deferred -- the slot is recorded and FillTypeSlots settles it once the
// type's reserved identity is filled, so the slot captures the final rtype.
func (c *Compiler) typeSlotValue(idx int, typ *vm.Type, descriptor bool) vm.Value {
	if typ != nil && typ.Rtype == nil {
		c.pendingTypeSlots = append(c.pendingTypeSlots, pendingSlot{idx: idx, typ: typ, descriptor: descriptor})
		return vm.Value{}
	}
	if descriptor {
		return vm.TypeValue(c.rtype(typ))
	}
	return vm.NewValue(c.rtype(typ))
}

// embedValue returns an addressable slot value of typ holding data (string or []byte).
func (c *Compiler) embedValue(typ *vm.Type, data []byte) vm.Value {
	rt := c.rtype(typ)
	v := vm.NewValue(rt)
	if rt.Kind() == reflect.String {
		v.Set(reflect.ValueOf(string(data)).Convert(rt))
	} else {
		v.Set(reflect.ValueOf(data).Convert(rt))
	}
	return v
}

// FillTypeSlots settles deferred type Data slots to their final rtype, now that
// MaterializeAll + the per-type reserve/fill attach have run. The reserve path
// fills each type's identity in place, so a slot's type already holds the final
// rtype; only slots left invalid at generate time (deferred, or an imported
// descriptor that could not materialize at parse) need settling here.
func (c *Compiler) FillTypeSlots() {
	for _, p := range c.pendingTypeSlots {
		if p.typ.Rtype == nil {
			continue
		}
		if p.descriptor {
			c.Data[p.idx] = vm.TypeValue(p.typ.Rtype)
		} else {
			c.Data[p.idx] = vm.NewValue(p.typ.Rtype)
		}
	}
	// Type symbols whose slot came from an invalid parse-time descriptor (e.g. an
	// imported `type Language uint16`) bypass pendingTypeSlots; materialize them.
	for _, sym := range c.Symbols {
		if sym.Kind != symbol.Type || sym.Index == symbol.UnsetAddr || sym.Type == nil {
			continue
		}
		if sym.Index >= len(c.Data) || c.Data[sym.Index].IsValid() {
			continue
		}
		if sym.Type.Rtype != nil {
			c.Data[sym.Index] = vm.NewValue(sym.Type.Rtype)
		}
	}
}

func (c *Compiler) findTypeSym(rtype reflect.Type) *vm.Type {
	// rtype is an already-materialized reflect.Type; a symbol can match it by
	// identity only if it too is materialized, so skip nil-Rtype symbols rather
	// than materializing every type just to compare.
	for _, sym := range c.Symbols {
		if sym.Kind == symbol.Type && sym.Type != nil && sym.Type.Rtype != nil && sym.Type.Rtype == rtype {
			return sym.Type
		}
	}
	return nil
}

func (c *Compiler) findConcreteFuncSym(name string) *symbol.Symbol {
	suffix := "." + name
	for k, sym := range c.Symbols {
		if strings.HasSuffix(k, suffix) && sym.Kind == symbol.Func {
			return sym
		}
	}
	return nil
}

func findEmbeddedIfaceMethod(typ *vm.Type, name string) ([]int, reflect.Type, *vm.Type) {
	for _, emb := range typ.Embedded {
		if emb.Type == nil {
			continue
		}
		if emb.Type.IsInterface() {
			emb.Type.EnsureIfaceMethods()
			for _, im := range emb.Type.IfaceMethods {
				if im.Name == name {
					return []int{emb.FieldIdx}, im.Rtype, im.Sig
				}
			}
		}
		if p, mt, sig := findEmbeddedIfaceMethod(emb.Type, name); p != nil {
			return append([]int{emb.FieldIdx}, p...), mt, sig
		}
	}
	return nil, nil, nil
}

func findEmbeddedMethod(typ *vm.Type, rtype reflect.Type, name string, path []int) (reflect.Method, []int, bool) {
	for _, emb := range typ.Embedded {
		embRtype := rtype.Field(emb.FieldIdx).Type
		fieldPath := append(path[:len(path):len(path)], emb.FieldIdx) //nolint:gocritic // intentionally creates a new slice
		rt := embRtype
		if rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rm, ok := rt.MethodByName(name); ok {
			return rm, fieldPath, false
		}
		if rm, ok := reflect.PointerTo(rt).MethodByName(name); ok {
			return rm, fieldPath, embRtype.Kind() != reflect.Pointer
		}
		if emb.Type != nil {
			if rm, p, na := findEmbeddedMethod(emb.Type, rt, name, fieldPath); rm.Type != nil {
				return rm, p, na
			}
		}
	}
	return reflect.Method{}, nil, false
}

func (c *Compiler) registerMethods(iface, typ *vm.Type) {
	isPtr := typ.Kind() == reflect.Pointer
	lookupTyp := typ
	if isPtr {
		if typ.ElemType != nil {
			lookupTyp = typ.ElemType
		} else if t := c.findTypeSym(c.rtype(typ).Elem()); t != nil {
			lookupTyp = t
		}
	} else if typ.Name == "" {
		if t := c.findTypeSym(c.rtype(typ)); t != nil {
			lookupTyp = t
		}
	}
	iface.EnsureIfaceMethods()
	for _, im := range iface.IfaceMethods {
		id := c.methodID(im.Name)
		if im.Rtype != nil {
			c.methodRtype[id] = im.Rtype
		}
		if id < len(typ.Methods) && typ.Methods[id].IsResolved() {
			continue // already registered directly or through embedded interface
		}
		s := &symbol.Symbol{Kind: symbol.Var, Name: lookupTyp.Name, Type: lookupTyp}
		m, fieldPath := c.Symbols.MethodByName(s, im.Name, c.Seg)
		if m == nil {
			// MethodByName only finds concrete function symbols; interface methods have none.
			for _, emb := range lookupTyp.Embedded {
				embType := emb.Type
				if embType == nil || !embType.IsInterface() {
					continue
				}
				embType.EnsureIfaceMethods()
				for _, embIM := range embType.IfaceMethods {
					if embIM.Name != im.Name {
						continue
					}
					for len(typ.Methods) <= id {
						typ.Methods = append(typ.Methods, vm.Method{Index: -1})
					}
					typ.Methods[id] = vm.Method{Index: -1, Path: []int{emb.FieldIdx}, EmbedIface: true, Rtype: embIM.Rtype, Sig: embIM.Sig}
					break
				}
			}
			continue
		}
		var mpath []int
		if len(fieldPath) > 0 {
			if isPtr {
				mpath = append([]int{}, fieldPath...)
			} else {
				mpath = fieldPath
			}
		} else if isPtr && !strings.HasPrefix(m.Name, "*") {
			mpath = []int{} // non-nil empty = deref only
		}
		for len(typ.Methods) <= id {
			typ.Methods = append(typ.Methods, vm.Method{Index: -1})
		}
		var msig *vm.Type
		if m.Type != nil && m.Type.Kind() == reflect.Func {
			msig = m.Type // materialize-time source of Rtype; filled by MaterializeAll
		}
		typ.Methods[id] = vm.Method{Index: m.Index, Path: mpath, PtrRecv: strings.HasPrefix(m.Name, "*"), Sig: msig}
	}
}

func (c *Compiler) stringIndex(s string) int {
	i, ok := c.strings[s]
	if !ok {
		i = len(c.Data)
		c.Data = append(c.Data, vm.ValueOf(s))
		c.strings[s] = i
	}
	return i
}

func (c *Compiler) errAt(t goparser.Token, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if loc := c.Sources.FormatPos(t.Pos); loc != "" {
		return fmt.Errorf("%s: %s", loc, msg)
	}
	return errors.New(msg)
}

func (c *Compiler) errUndef(t goparser.Token, name string) error {
	return goparser.ErrUndefined{Name: name, Loc: c.Sources.FormatPos(t.Pos), Pos: t.Pos}
}

// errOverflow reports a constant that cannot be represented in typ, matching the
// goparser-side error (gc "constant X overflows T") so both carry a source snippet.
func (c *Compiler) errOverflow(t goparser.Token, cv constant.Value, typ *vm.Type) error {
	return goparser.ErrConstOverflow{Value: cv.String(), Type: typ.String(), Loc: c.Sources.FormatPos(t.Pos), Pos: t.Pos}
}

func (c *Compiler) symAt(name string) (*symbol.Symbol, bool) {
	if c.CompilingPkg != "" {
		if s, ok := c.Symbols[goparser.QualifyName(c.CompilingPkg, name)]; ok {
			return s, true
		}
	}
	s, ok := c.Symbols[name]
	return s, ok
}

func showStack(stack []*symbol.Symbol) {
	if debug {
		_, file, line, _ := runtime.Caller(1)
		fmt.Fprintf(os.Stderr, "%s%d: showstack: %d\n", path.Base(file), line, len(stack))
		for i, s := range stack {
			fmt.Fprintf(os.Stderr, "  stack[%d]: %v\n", i, s)
		}
	}
}

func (c *Compiler) emit(t goparser.Token, op vm.Op, arg ...int) {
	if debug {
		_, file, line, _ := runtime.Caller(1)
		fmt.Fprintf(os.Stderr, "%s:%d: %v emit %v %v\n", path.Base(file), line, t, op, arg)
	}
	inst := vm.Instruction{Op: op, Pos: vm.Pos(t.Pos)}
	if len(arg) > 0 {
		inst.A = int32(arg[0])
	}
	if len(arg) > 1 {
		inst.B = int32(arg[1])
	}
	// Field/FieldSet encode a variable-length field index path in A, B.
	// Unused trailing B must be -1 so the VM can distinguish path length.
	if op == vm.Field || op == vm.FieldSet {
		if len(arg) < 2 {
			inst.B = -1
		}
	}
	c.Code = append(c.Code, inst)
}

func (c *Compiler) emitField(t goparser.Token, path []int) {
	for len(path) > 2 {
		c.emit(t, vm.Field, path[0], path[1])
		path = path[2:]
	}
	c.emit(t, vm.Field, path...)
}

func fieldPathOffset(rt reflect.Type, path []int) uintptr {
	var off uintptr
	for _, i := range path {
		if rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		f := rt.Field(i)
		off += f.Offset
		rt = f.Type
	}
	return off
}

// paramNeedsDetach reports whether any parameter of fnType has reflect Kind
// Struct or Array. Those are the only kinds where vm.detachByValueArgs does
// real work (see its definition); for everything else the detach is a no-op
// the compiler can elide via vm.CallImmFast.
func paramNeedsDetach(fnType *vm.Type) bool {
	if fnType == nil || fnType.Kind() != reflect.Func {
		return true
	}
	for i, n := 0, fnType.NumIn(); i < n; i++ {
		switch fnType.ParamType(i).Kind() {
		case reflect.Struct, reflect.Array:
			return true
		}
	}
	return false
}

func (c *Compiler) emitIfaceWrap(t goparser.Token, ifaceTyp, concreteTyp *vm.Type) {
	c.emitIfaceWrapAt(t, ifaceTyp, concreteTyp, 0)
}

func (c *Compiler) emitIfaceWrapAt(t goparser.Token, ifaceTyp, concreteTyp *vm.Type, depth int) {
	if ifaceTyp == nil || !ifaceTyp.IsInterface() || concreteTyp == nil || concreteTyp.IsInterface() {
		return
	}
	c.registerMethods(ifaceTyp, concreteTyp)
	c.emit(t, vm.IfaceWrap, c.typeIndex(concreteTyp), depth)
}

func (c *Compiler) emitMapValueWrap(t goparser.Token, elemTyp *vm.Type, vs *symbol.Symbol) {
	if vs.Type != nil && vs.Type.Kind() == reflect.Func {
		c.emit(t, vm.WrapFunc, c.typeIndex(vs.Type))
	} else {
		c.emitIfaceWrap(t, elemTyp, vs.Type)
	}
}

func (c *Compiler) emitTypeOrGlobal(t goparser.Token, sym *symbol.Symbol, index int) {
	if sym.Kind == symbol.Type {
		switch sym.Type.Kind() {
		case reflect.Slice, reflect.Map:
			// B=-1 makes Fnew produce the nil zero value; a composite literal
			// patches it to a non-nil container (see the Composite handler).
			c.emit(t, vm.Fnew, index, -1)
		case reflect.Pointer:
			c.emit(t, vm.FnewE, index, 1)
		default:
			c.emit(t, vm.Fnew, index, 1)
		}
	} else {
		c.emit(t, vm.GetGlobal, index)
	}
}

// generate generates vm code and data from parsed tokens, or returns an error.
func (c *Compiler) generate(tokens goparser.Tokens) (err error) {
	if debugTokens {
		fmt.Printf("== generate tokens: %v\n", tokens)
	}
	// Pass start, so resolveLabel can tell a this-pass label from a stale one.
	savedGenStart := c.genStart
	c.genStart = len(c.Code)
	defer func() { c.genStart = savedGenStart }()
	// In lenient mode, turn a codegen panic into a located error (at the current
	// token) so the external-test loader can drop the file; else crash loudly.
	var cur goparser.Token
	defer func() {
		if !c.LenientCompile {
			return
		}
		if r := recover(); r != nil {
			if debugPanic {
				rtdebug.PrintStack()
			}
			err = c.errAt(cur, "internal compile error: %v", r)
		}
	}()
	fixList := goparser.Tokens{}  // list of tokens to fix after all necessary information is gathered
	stack := []*symbol.Symbol{}   // for symbolic evaluation and type checking
	codeStarts := []int{}         // parallel to stack: c.Code index where each operand's load began, for const-fold retraction
	flen := []int{}               // stack length according to function scopes
	funcStack := []string{}       // names of functions currently being compiled
	funcStartStack := []int{}     // entry code address per function on funcStack, used to record FuncRange on exit
	jumpDepth := map[string]int{} // expected compile-stack depth at short-circuit merge labels
	exprBaseStack := []int{}      // stack of compile-stack depths at expression-statement starts; nested (closure body inside outer call args) requires a stack, not a single base
	growPos := []int{}            // code positions of Grow instructions per function scope
	maxExprDepth := []int{}       // max expression depth above locals per function scope
	hasDefer := []bool{}          // whether current function scope uses defer
	retCellSlots := [][]int{}     // per function scope: slot indices of cell-promoted named returns
	// Per function scope: local slot indices that have had their address taken
	// (lang.Addr -> vm.AddrLocal). Future GetLocals on these slots emit
	// vm.GetLocalSync so a native callee writing through the pushed pointer is
	// seen by subsequent num-based reads. Reset on function entry/exit.
	addressedSlots := []map[int]bool{}

	// markAddressed records that a local-frame slot has had its address taken in
	// this function; future GetLocals on it emit GetLocalSync (see lang.Ident).
	markAddressed := func(slot int) {
		if len(addressedSlots) == 0 {
			return
		}
		m := addressedSlots[len(addressedSlots)-1]
		if m == nil {
			m = map[int]bool{}
			addressedSlots[len(addressedSlots)-1] = m
		}
		m[slot] = true
	}

	// pushAt appends a compile-stack entry recording the c.Code index where its
	// load sequence began (used by the const folder to retract operand loads).
	pushAt := func(s *symbol.Symbol, start int) {
		stack = append(stack, s)
		codeStarts = append(codeStarts, start)
		if len(maxExprDepth) > 0 {
			if d := len(stack) - flen[len(flen)-1]; d > maxExprDepth[len(maxExprDepth)-1] {
				maxExprDepth[len(maxExprDepth)-1] = d
			}
		}
	}
	// push records the current code position as the entry's load start. Callers
	// that emit a load do so right after push, so len(c.Code) is the load's start.
	push := func(s *symbol.Symbol) { pushAt(s, len(c.Code)) }
	// markEscapingMethodDetach flags the IfaceCall producing a defer/go method
	// value so it captures the receiver by value, not its slot.
	// The function is at stack[len-1-narg]; its load spans the IfaceCall.
	markEscapingMethodDetach := func(narg int) {
		fi := len(stack) - 1 - narg
		if fi < 0 || fi >= len(codeStarts) {
			return
		}
		start, end := codeStarts[fi], len(c.Code)
		if narg > 0 {
			end = codeStarts[fi+1]
		}
		for i := end - 1; i >= start; i-- {
			if c.Code[i].Op == vm.IfaceCall {
				c.Code[i].B |= vm.IfaceCallDetachBit
				return
			}
		}
	}
	// reserveDepth raises the function's max expression depth by `extra` slots
	// beyond what push() modelled, for transient operand runs that Grow must still cover.
	reserveDepth := func(extra int) {
		if extra <= 0 || len(maxExprDepth) == 0 {
			return
		}
		if d := len(stack) - flen[len(flen)-1] + extra; d > maxExprDepth[len(maxExprDepth)-1] {
			maxExprDepth[len(maxExprDepth)-1] = d
		}
	}
	top := func() *symbol.Symbol { return stack[len(stack)-1] }
	pop := func() *symbol.Symbol {
		l := len(stack) - 1
		s := stack[l]
		stack = stack[:l]
		codeStarts = codeStarts[:l]
		return s
	}
	// truncStack trims both the symbol stack and its parallel codeStarts to n.
	truncStack := func(n int) { stack = stack[:n]; codeStarts = codeStarts[:n] }
	// foldBinaryConst folds `left <t.Tok> right` into a single constant load when
	// both operands are constants, retracting their loads (from leftStart) and
	// pushing the result. Operands must already be popped; on a non-fold it leaves
	// the stack untouched so the caller's runtime path proceeds. Returns whether
	// it folded.
	foldBinaryConst := func(t goparser.Token, left, right *symbol.Symbol, leftStart int) (bool, error) {
		cv, rtyp, ok := c.foldConstBinary(t.Tok, left, right)
		if !ok {
			return false, nil
		}
		c.Code = c.Code[:leftStart]
		sym, err := c.emitFoldedConst(t, cv, rtyp)
		if err != nil {
			return true, err
		}
		pushAt(sym, leftStart)
		return true, nil
	}
	// foldUnaryConst folds a unary op on the stack-top constant in place (peeking,
	// then popping only on success). Returns whether it folded.
	foldUnaryConst := func(t goparser.Token, op lang.Token) (bool, error) {
		s := top()
		if s.Kind != symbol.Const || s.Cval == nil {
			return false, nil
		}
		if op == lang.Not && s.Cval.Kind() != constant.Bool {
			return false, nil
		}
		start := codeStarts[len(stack)-1]
		pop()
		cv, _, _ := goparser.FoldUnary(op, s.Cval, s.Type)
		c.Code = c.Code[:start]
		sym, err := c.emitFoldedConst(t, cv, s.Type)
		if err != nil {
			return true, err
		}
		pushAt(sym, start)
		return true, nil
	}
	// checkTopN returns ErrUndefined if any of the top n stack entries is an unresolved
	// identifier (Unset with a non-empty Name). Anonymous Unset entries (Name=="") are
	// legitimate intermediate values (e.g. field-access results) and are not checked.
	// t is the current token; its position is attached to the error so the user
	// sees file:line:col rather than a bare "undefined: X".
	checkTopN := func(t goparser.Token, n int) error {
		for j := range n {
			if i := len(stack) - 1 - j; i >= 0 && stack[i].Kind == symbol.Unset && stack[i].Name != "" {
				return c.errUndef(t, stack[i].Name)
			}
		}
		return nil
	}
	popflen := func() int { le := len(flen) - 1; l := flen[le]; flen = flen[:le]; return l }
	curFunc := func() string {
		if n := len(funcStack); n > 0 {
			return funcStack[n-1]
		}
		return ""
	}
	// freeVarIndex returns the capture index of a variable named in the current
	// closure body, or -1 when name is not a captured (free) variable.
	freeVarIndex := func(name string) int {
		cf := curFunc()
		if cf == "" {
			return -1
		}
		cloSym, ok := c.symAt(cf)
		if !ok || cloSym == nil {
			return -1
		}
		return cloSym.FreeVarIndex(name)
	}
	// isCallable reports whether sym can be the target of a function call.
	isCallable := func(sym *symbol.Symbol) bool {
		if sym.Kind == symbol.Func || sym.Kind == symbol.Builtin {
			return true
		}
		if sym.Type != nil {
			return sym.Type.Kind() == reflect.Func
		}
		rv := sym.Value.Reflect()
		return rv.IsValid() && rv.Kind() == reflect.Func
	}

	for _, t := range tokens {
		cur = t
		switch t.Tok {
		case lang.Int:
			n64, err := strconv.ParseInt(t.Str, 0, 64)
			if err != nil {
				// Try unsigned parse for large literals (e.g. MaxUint64).
				u64, uerr := strconv.ParseUint(t.Str, 0, 64)
				if uerr != nil {
					return err
				}
				n64 = int64(u64)
			}
			n := int(n64)
			intTyp := c.Symbols["int"].Type
			push(&symbol.Symbol{Kind: symbol.Const, Value: vm.ValueOf(n), Cval: litCval(t.Str, token.INT), Type: intTyp})
			c.emitConstLoad(t, vm.ValueOf(n), intTyp)

		case lang.Float:
			f, err := strconv.ParseFloat(t.Str, 64)
			if err != nil {
				return err
			}
			v := vm.ValueOf(f)
			di := len(c.Data)
			c.Data = append(c.Data, v)
			push(&symbol.Symbol{Kind: symbol.Const, Value: v, Cval: litCval(t.Str, token.FLOAT), Type: c.Symbols["float64"].Type})
			c.emit(t, vm.GetGlobal, di)

		case lang.Imag:
			f, err := strconv.ParseComplex(t.Str, 128)
			if err != nil {
				return err
			}
			v := vm.ValueOf(f)
			di := len(c.Data)
			c.Data = append(c.Data, v)
			push(&symbol.Symbol{Kind: symbol.Const, Value: v, Cval: litCval(t.Str, token.IMAG), Type: c.Symbols["complex128"].Type})
			c.emit(t, vm.GetGlobal, di)

		case lang.String:
			if t.Prefix() == "'" {
				r, _, _, err2 := strconv.UnquoteChar(t.Block(), '\'')
				if err2 != nil {
					return err2
				}
				push(&symbol.Symbol{Kind: symbol.Const, Value: vm.ValueOf(r), Cval: litCval(t.Str, token.CHAR), Type: c.Symbols["rune"].Type})
				c.emit(t, vm.Push, int(r))
				break
			}
			s, err2 := strconv.Unquote(t.Str)
			if err2 != nil {
				return err2
			}
			push(&symbol.Symbol{Kind: symbol.Const, Value: vm.ValueOf(s), Cval: constant.MakeString(s), Type: c.Symbols["string"].Type})
			c.emit(t, vm.GetGlobal, c.stringIndex(s))

		case lang.Add, lang.Mul, lang.Sub, lang.Quo, lang.Rem:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			leftStart := codeStarts[len(stack)-2]
			rightStart := codeStarts[len(stack)-1]
			right, left := pop(), pop()
			if h, err := foldBinaryConst(t, left, right, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			if err := c.errIfMismatch(t, left, right); err != nil {
				return err
			}
			typ := arithmeticOpType(right, left)
			c.convertOperand(t, right, rightStart, typ, 0)
			c.convertOperand(t, left, leftStart, typ, 1)
			push(&symbol.Symbol{Kind: constKind(right, left), Type: typ})
			if typ != nil {
				if k := typ.Kind(); k == reflect.Complex64 || k == reflect.Complex128 {
					var op vm.Op
					switch t.Tok {
					case lang.Add:
						op = vm.AddComplex
					case lang.Sub:
						op = vm.SubComplex
					case lang.Mul:
						op = vm.MulComplex
					case lang.Quo:
						op = vm.DivComplex
					default:
						return c.errAt(t, "operator %s not defined on %s", t.Str, typ.Name)
					}
					c.emit(t, op, int(k))
					break
				}
			}
			switch t.Tok {
			case lang.Add:
				c.emitArithmeticOp(t, right, typ, vm.AddInt, vm.AddIntImm, vm.GetLocalAddIntImm, vm.AddStr)
			case lang.Mul:
				c.emitArithmeticOp(t, right, typ, vm.MulInt, vm.MulIntImm, vm.GetLocalMulIntImm, 0)
			case lang.Sub:
				c.emitArithmeticOp(t, right, typ, vm.SubInt, vm.SubIntImm, vm.GetLocalSubIntImm, 0)
			case lang.Quo:
				c.emitArithmeticOp(t, right, typ, vm.DivInt, 0, 0, 0)
			case lang.Rem:
				c.emitArithmeticOp(t, right, typ, vm.RemInt, 0, 0, 0)
			}

		case lang.Minus:
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			if h, err := foldUnaryConst(t, lang.Minus); err != nil {
				return err
			} else if h {
				break
			}
			typ := symbol.Vtype(top())
			if k := typ.Kind(); k == reflect.Complex64 || k == reflect.Complex128 {
				c.emit(t, vm.NegComplex, int(k))
				break
			}
			c.emit(t, numericOp(vm.NegInt, typ))

		case lang.Not:
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			if h, err := foldUnaryConst(t, lang.Not); err != nil {
				return err
			} else if h {
				break
			}
			c.emit(t, vm.Not)

		case lang.Plus:
			// Unary '+' is idempotent. Nothing to do.

		case lang.Addr:
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			srcType := pop().Type
			push(&symbol.Symbol{Kind: symbol.Value, Type: vm.PointerTo(srcType)})
			// AddrLocal aliases the frame slot directly, which is only safe
			// when the slot's reflect type matches the language type. Interface-
			// typed locals are stored as vm.Iface internally; &r must still
			// yield a *interface{} (handled by the Addr opcode's Iface branch).
			concrete := srcType != nil && !srcType.IsInterface()
			n := len(c.Code)
			// Func slots are interface{} boxes; pass the func type (+1; 0=none)
			// so AddrLocal retypes the slot, making &f a *func(...).
			funcRetypeOp := 0
			if srcType != nil && srcType.Kind() == reflect.Func {
				funcRetypeOp = c.typeSym(srcType).Index + 1
			}
			switch {
			case n > 0 && c.Code[n-1].Op == vm.Index:
				c.Code[n-1].Op = vm.IndexAddr
			case concrete && n > 0 && c.Code[n-1].Op == vm.GetLocal:
				c.Code[n-1].Op = vm.AddrLocal
				c.Code[n-1].B = int32(funcRetypeOp)
				markAddressed(int(c.Code[n-1].A))
			case concrete && n > 0 && c.Code[n-1].Op == vm.GetLocal2:
				idx := int(c.Code[n-1].B)
				c.Code[n-1].Op = vm.GetLocal
				c.Code[n-1].B = 0
				c.emit(t, vm.AddrLocal, idx, funcRetypeOp)
				markAddressed(idx)
			default:
				c.emit(t, vm.Addr)
			}

		case lang.Deref:
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			s := pop()
			if s.Type == nil {
				return c.errUndef(t, s.Name)
			}
			if !s.Type.IsPtr() {
				return c.errAt(t, "cannot dereference non-pointer type %v", s.Type)
			}
			push(&symbol.Symbol{Kind: symbol.Value, Type: s.Type.Elem()})
			c.emit(t, vm.Deref)

		case lang.TypeAssert:
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			okForm := t.Arg[0].(int)
			typ := t.Arg[1].(*vm.Type)
			typ.EnsureIfaceMethods()
			c.populateIfaceMethodIDs(typ)
			pop() // interface value
			push(&symbol.Symbol{Kind: symbol.Value, Type: typ})
			if okForm == 1 {
				push(&symbol.Symbol{Kind: symbol.Value, Type: vm.TypeOf(false)})
			}
			c.emit(t, vm.TypeAssert, c.typeIndex(typ), okForm)

		case lang.TypeSwitchJump:
			var typ *vm.Type
			if t.Arg[0] != nil {
				typ = t.Arg[0].(*vm.Type)
			}
			pop() // consume iface_sym from compiler stack
			typeIdx := -1
			if typ != nil {
				typ.EnsureIfaceMethods()
				c.populateIfaceMethodIDs(typ)
				typeIdx = c.typeIndex(typ)
			}
			c.emit(t, vm.TypeBranch, c.resolveLabel(t, &fixList), typeIdx)

		case lang.Index:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			okForm := len(t.Arg) > 0 && t.Arg[0].(int) == 1
			pop()
			s := pop()
			vt := symbol.Vtype(s)
			if vt == nil {
				return c.errUndef(t, s.Name)
			}
			if vt.IsPtr() {
				vt = vt.Elem()
			}
			var elemType *vm.Type
			switch vt.Kind() {
			case reflect.Map:
				elemType = vt.Elem()
				if okForm {
					c.emit(t, vm.MapIndexOk)
					push(&symbol.Symbol{Kind: symbol.Value, Type: elemType})
					push(&symbol.Symbol{Kind: symbol.Value, Type: c.Symbols["bool"].Type})
				} else {
					c.emit(t, vm.MapIndex)
					push(&symbol.Symbol{Kind: symbol.Value, Type: elemType})
				}
			case reflect.String:
				c.emit(t, vm.Index)
				elemType = c.Symbols["uint8"].Type
				push(&symbol.Symbol{Kind: symbol.Value, Type: elemType})
			default:
				c.emit(t, vm.Index)
				elemType = vt.Elem()
				push(&symbol.Symbol{Kind: symbol.Value, Type: elemType})
			}

		case lang.Greater, lang.Less, lang.GreaterEqual, lang.LessEqual:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			leftStart := codeStarts[len(stack)-2]
			s2Start := codeStarts[len(stack)-1]
			s2, s1 := pop(), pop()
			if h, err := foldBinaryConst(t, s1, s2, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			if err := c.errIfMismatch(t, s1, s2); err != nil {
				return err
			}
			typ := arithmeticOpType(s2, s1)
			c.convertOperand(t, s2, s2Start, typ, 0)
			c.convertOperand(t, s1, leftStart, typ, 1)
			push(&symbol.Symbol{Kind: symbol.Value, Type: booleanOpType(s2, s1)})
			switch t.Tok {
			case lang.Greater:
				c.emitComparisonOp(t, s2, typ, vm.GreaterInt,
					vm.GreaterIntImm, vm.GreaterUintImm,
					vm.GetLocalGreaterIntImm, vm.GetLocalGreaterUintImm, vm.GreaterStr, false)
			case lang.Less:
				c.emitComparisonOp(t, s2, typ, vm.LowerInt,
					vm.LowerIntImm, vm.LowerUintImm,
					vm.GetLocalLowerIntImm, vm.GetLocalLowerUintImm, vm.LowerStr, false)
			case lang.GreaterEqual:
				c.emitComparisonOp(t, s2, typ, vm.LowerInt,
					vm.LowerIntImm, vm.LowerUintImm,
					vm.GetLocalLowerIntImm, vm.GetLocalLowerUintImm, vm.LowerStr, true)
			case lang.LessEqual:
				c.emitComparisonOp(t, s2, typ, vm.GreaterInt,
					vm.GreaterIntImm, vm.GreaterUintImm,
					vm.GetLocalGreaterIntImm, vm.GetLocalGreaterUintImm, vm.GreaterStr, true)
			}

		case lang.NotEqual:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			leftStart := codeStarts[len(stack)-2]
			s2Start := codeStarts[len(stack)-1]
			s2, s1 := pop(), pop()
			if h, err := foldBinaryConst(t, s1, s2, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			if err := c.errIfMismatch(t, s1, s2); err != nil {
				return err
			}
			typ := arithmeticOpType(s2, s1)
			c.convertOperand(t, s2, s2Start, typ, 0)
			c.convertOperand(t, s1, leftStart, typ, 1)
			push(&symbol.Symbol{Type: booleanOpType(s2, s1)})
			c.emit(t, vm.Equal)
			c.emit(t, vm.Not)

		case lang.And, lang.Or, lang.Xor, lang.AndNot:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			leftStart := codeStarts[len(stack)-2]
			right, left := pop(), pop()
			if h, err := foldBinaryConst(t, left, right, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			if err := c.errIfMismatch(t, left, right); err != nil {
				return err
			}
			typ := arithmeticOpType(right, left)
			push(&symbol.Symbol{Kind: symbol.Value, Type: typ})
			switch t.Tok {
			case lang.And:
				c.emit(t, vm.BitAnd)
			case lang.Or:
				c.emit(t, vm.BitOr)
			case lang.Xor:
				c.emit(t, vm.BitXor)
			case lang.AndNot:
				c.emit(t, vm.BitAndNot)
			}

		case lang.Shl, lang.Shr:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			leftStart := codeStarts[len(stack)-2]
			shift := pop() // shift amount
			left := pop()  // left operand
			// A shift of two constants is itself a constant. Folding it gives the
			// exact arbitrary-precision value, and emitFoldedConst widens a result
			// past int64 to uint64 (e.g. 1<<63) or float64 (e.g. 1<<120).
			if h, err := foldBinaryConst(t, left, shift, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			leftTyp := shiftLeftType(left, c.Symbols["int"].Type)
			c.emitConstConvert(t, left, leftTyp, 1)
			kind := constKind(left, shift)
			// An untyped-const left operand of a non-constant shift assumes the
			// type of its context (spec: Operators), not its default type.
			// Keep the result Const-kinded (Cval nil, so no fold applies) so binary-op
			// contexts adopt the other operand's type and retype via Convert.
			// A nil-Cval Const left is itself such a marker (chained shifts).
			if left.Kind == symbol.Const {
				kind = symbol.Const
			}
			push(&symbol.Symbol{Kind: kind, Type: leftTyp})
			if t.Tok == lang.Shl {
				c.emit(t, vm.BitShl)
			} else {
				c.emit(t, vm.BitShr)
			}

		case lang.BitComp:
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			if h, err := foldUnaryConst(t, lang.BitComp); err != nil {
				return err
			} else if h {
				break
			}
			c.emit(t, vm.BitComp)

		case lang.Arrow: // unary channel receive: <-ch
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			okForm := 0
			if len(t.Arg) > 0 {
				okForm = t.Arg[0].(int)
			}
			ch := pop()
			if ch.Type.Kind() != reflect.Chan {
				return c.errAt(t, "invalid channel receive: not a channel type")
			}
			elemType := ch.Type.Elem()
			push(&symbol.Symbol{Kind: symbol.Value, Type: elemType})
			if okForm == 1 {
				push(&symbol.Symbol{Kind: symbol.Value, Type: c.Symbols["bool"].Type})
			}
			c.emit(t, vm.ChanRecv, okForm)

		case lang.Call:
			narg := t.Arg[0].(int)
			spread := len(t.Arg) > 1 && t.Arg[1].(int) != 0
			if err := checkTopN(t, narg); err != nil {
				return err
			}
			s := stack[len(stack)-1-narg]
			// If s is a non-callable plain Value, arguments may have been expanded
			// from a multi-return call (e.g. g(f()) where f returns 2 values).
			// Only Value symbols (not Type, Func, etc.) indicate expansion.
			// Search backward for the real function symbol.
			if s.Kind == symbol.Value && !isCallable(s) {
				for i := narg + 1; i < len(stack); i++ {
					if candidate := stack[len(stack)-1-i]; isCallable(candidate) {
						s = candidate
						narg = i
						break
					}
				}
			}
			if ok, err := c.compileBuiltin(s, narg, t, &stack, push, pop, top); ok {
				if err != nil {
					return err
				}
				break
			}
			if ok, err := c.compileIntrinsic(s, narg, t, push, pop, stack); ok {
				if err != nil {
					return err
				}
				break
			}
			if s.Kind == symbol.Type {
				if narg != 1 {
					return c.errAt(t, "type conversion requires exactly one argument")
				}
				if !s.NoFnew {
					c.removeFnew(s.Index)
				}
				argStart := codeStarts[len(stack)-1]
				arg := pop() // argument (top of stack)
				pop()        // type symbol
				// A constant the target type can't represent is a compile error (int8(200)).
				// Checked here for every conversion since the fold below is skipped for named types.
				if arg.Kind == symbol.Const && arg.Cval != nil && goparser.OverflowsType(arg.Cval, s.Type) {
					return c.errOverflow(t, arg.Cval, s.Type)
				}
				// A named-type value must keep its rtype to dispatch methods at the native
				// boundary (e.g. xml.Marshal), but the integer-const fold below would drop it.
				// Route named types through a runtime Convert; plain basic conversions still fold.
				namedMethodful := s.Type.Base != nil || (s.Type.Rtype != nil && s.Type.Rtype.NumMethod() > 0)
				// Converting a numeric constant to a numeric type is itself a
				// constant (Go spec): fold it so e.g. `int32(7) * int32(6)`
				// collapses to a single load. Retract the argument's load (and any
				// Nop left by removeFnew above) and emit the converted constant,
				// marking it Const so an enclosing constant expression folds further.
				if !namedMethodful && arg.Kind == symbol.Const && arg.Cval != nil && isNumericConvType(s.Type) &&
					(arg.Cval.Kind() == constant.Int || arg.Cval.Kind() == constant.Float) {
					for argStart > 0 && c.Code[argStart-1].Op == vm.Nop {
						argStart--
					}
					c.Code = c.Code[:argStart]
					sym, err := c.emitFoldedConst(t, arg.Cval, s.Type)
					if err != nil {
						return err
					}
					pushAt(sym, argStart)
					break
				}
				push(&symbol.Symbol{Kind: symbol.Value, Type: s.Type})
				if s.Type.IsInterface() {
					c.emitIfaceWrap(t, s.Type, arg.Type)
				} else {
					c.emit(t, vm.Convert, s.Index)
				}
				break
			}
			if s.MethodExpr {
				if narg < 1 {
					return c.errAt(t, "method expression call requires at least a receiver argument")
				}
				// Direct call: retract the speculative MkMethodExpr value emit (only
				// needed when stored) and bind the receiver inline below.
				if meStart := codeStarts[len(stack)-1-narg]; meStart < len(c.Code) && c.Code[meStart].Op == vm.MkMethodExpr {
					c.Code[meStart] = vm.Instruction{Op: vm.Nop}
				}
				methodNarg := narg - 1
				methodWantsPtr := strings.HasPrefix(s.Name, "*")
				recvSym := stack[len(stack)-narg]
				recvIsPtr := recvSym.Type != nil && recvSym.Type.Kind() == reflect.Pointer

				// Bring receiver to top of stack.
				if narg > 1 {
					c.emit(t, vm.Swap, 0, narg-1)
				}
				switch {
				case methodWantsPtr && !recvIsPtr:
					c.emit(t, vm.Addr)
				case !methodWantsPtr && recvIsPtr:
					c.emit(t, vm.Deref)
				}
				// Create closure binding receiver to method.
				c.emit(t, vm.HeapAlloc)
				c.emit(t, vm.GetGlobal, s.Index)
				c.emit(t, vm.Swap, 0, 1)
				c.emit(t, vm.MkClosure, 1)
				// Move closure to function position (bottom of args).
				if narg > 1 {
					c.emit(t, vm.Swap, 0, narg-1)
				}

				pop() // method expression symbol
				for i := 0; i < narg; i++ {
					pop()
				}
				typ := s.Type
				nret := typ.NumOut()
				for i := range nret {
					push(&symbol.Symbol{Kind: symbol.Value, Type: typ.ReturnType(i)})
				}
				c.emit(t, vm.Call, methodNarg, nret)
				break
			}
			if s.Kind != symbol.Value {
				typ := s.Type
				if typ == nil {
					return c.errUndef(t, s.Name)
				}
				if typ.Kind() != reflect.Func {
					return c.errAt(t, "internal: call of non-func %s (type %v)", s.Name, typ)
				}
				// Wrap concrete args in Iface when the parameter expects an interface type.
				// Use mvm-level Params types (which carry IfaceMethods) when available.
				nIn := typ.NumIn()
				nFixed := nIn
				if typ.IsVariadic() {
					nFixed = nIn - 1
				}
				for k := 0; k < narg && k < nIn; k++ {
					argSym := stack[len(stack)-narg+k]
					if argSym.Type == nil {
						if k < nFixed || (spread && k == nFixed) {
							c.emitNilCoerce(t, argSym, typ.ParamType(k), narg-1-k)
						}
						continue
					}
					if argSym.Type.IsInterface() {
						continue
					}
					ifaceTyp := typ.ParamType(k)
					depth := narg - 1 - k
					c.emitIfaceWrapAt(t, ifaceTyp, argSym.Type, depth)
					if !ifaceTyp.IsInterface() {
						c.emitConstConvert(t, argSym, ifaceTyp, depth)
					}
				}
				// Type switches on variadic slice elements require Iface wrapping at the call site.
				// For spread calls (f(s...)), the slice is pre-built; skip per-element wrapping.
				if typ.IsVariadic() && !spread {
					elemTyp := typ.ParamType(nIn - 1).Elem()
					if elemTyp.Kind() == reflect.Interface {
						for k := nFixed; k < narg; k++ {
							argSym := stack[len(stack)-narg+k]
							if argSym.Type == nil || argSym.Type.IsInterface() {
								continue
							}
							c.emitIfaceWrapAt(t, elemTyp, argSym.Type, narg-1-k)
						}
					}
				}
				// Pop function and input arg symbols, push return value symbols.
				pop()
				for i := 0; i < narg; i++ {
					pop()
				}
				nret := typ.NumOut()
				for i := range nret {
					push(&symbol.Symbol{Kind: symbol.Value, Type: typ.ReturnType(i)})
				}
				callNarg := narg
				if typ.IsVariadic() {
					if !spread {
						// Pack trailing arguments into a slice for the variadic parameter.
						nExtra := narg - nFixed
						elemIdx := c.typeSym(typ.ParamType(nIn - 1).Elem()).Index
						c.emit(t, vm.MkSlice, nExtra, elemIdx)
					}
					callNarg = nFixed + 1
				}
				// Direct call to a declared function (no closure): use CallImm
				// to avoid loading the func value and skip type dispatch at runtime.
				if s.Kind == symbol.Func && len(s.FreeVars) == 0 && c.removeGetGlobal(s.Index) {
					// CallImmFast skips detachByValueArgs when no param is
					// Struct/Array (the only kinds that ever need the detach;
					// see vm.detachByValueArgs). If method calls are ever lowered
					// to CallImm, the receiver must be included as param 0.
					op := vm.CallImm
					if !paramNeedsDetach(typ) {
						op = vm.CallImmFast
					}
					c.emit(t, op, s.Index, callNarg<<16|nret)
				} else {
					callNret := nret
					// The flag marks the last arg as a pre-built slice (packed
					// by MkSlice above or written as f(s...)), so a native
					// callee goes through CallSlice.
					if typ.IsVariadic() {
						callNret |= int(vm.CallSpreadFlag)
					}
					c.emit(t, vm.Call, callNarg, callNret)
				}
				break
			}
			// s.Kind == symbol.Value: function value on stack (native Go func or returned mvm closure).
			var rtyp reflect.Type
			if rv := s.Value.Reflect(); rv.IsValid() {
				rtyp = rv.Type()
			} else if s.Type != nil {
				rtyp = c.rtype(s.Type)
			}
			if rtyp != nil && rtyp.Kind() != reflect.Func {
				return c.errAt(t, "cannot call non-function value of type %s", rtyp)
			}
			// Wrap concrete args in Iface when the parameter expects an interface type.
			if rtyp != nil && rtyp.Kind() == reflect.Func {
				nIn := rtyp.NumIn()
				nFixed := nIn
				if rtyp.IsVariadic() {
					nFixed = nIn - 1
				}
				for k := 0; k < narg && k < nIn; k++ {
					argSym := stack[len(stack)-narg+k]
					if argSym.Type == nil {
						if k < nFixed || (spread && k == nFixed) {
							c.emitNilCoerce(t, argSym, &vm.Type{Rtype: rtyp.In(k)}, narg-1-k)
						}
						continue
					}
					if argSym.Type.IsInterface() {
						continue
					}
					ifaceTyp := &vm.Type{Rtype: rtyp.In(k)}
					depth := narg - 1 - k
					c.emitIfaceWrapAt(t, ifaceTyp, argSym.Type, depth)
					if !ifaceTyp.IsInterface() {
						c.emitConstConvert(t, argSym, ifaceTyp, depth)
					}
				}
				if rtyp.IsVariadic() && !spread {
					nFixed := nIn - 1
					elemType := rtyp.In(nFixed).Elem()
					if elemType.Kind() == reflect.Interface {
						elemTyp := &vm.Type{Rtype: elemType}
						for k := nFixed; k < narg; k++ {
							argSym := stack[len(stack)-narg+k]
							if argSym.Type == nil || argSym.Type.IsInterface() {
								continue
							}
							c.emitIfaceWrapAt(t, elemTyp, argSym.Type, narg-1-k)
						}
					}
				}
			}
			// Pop function and input arg symbols, push return value symbols.
			for i := 0; i < narg+1; i++ {
				pop()
			}
			nret := 0
			if rtyp != nil && rtyp.Kind() == reflect.Func {
				nret = rtyp.NumOut()
				for i := 0; i < nret; i++ {
					var retType *vm.Type
					if s.Type != nil {
						retType = s.Type.ReturnType(i)
					} else {
						retType = &vm.Type{Rtype: rtyp.Out(i)}
					}
					push(&symbol.Symbol{Kind: symbol.Value, Type: retType})
				}
			}
			callNarg := narg
			callNret := nret
			if rtyp != nil && rtyp.IsVariadic() {
				if !spread {
					// Pack trailing args into the variadic slice, matching the
					// caller-packs convention used for declared variadic funcs.
					nFixed := rtyp.NumIn() - 1
					var elemTyp *vm.Type
					if s.Type != nil {
						elemTyp = s.Type.ParamType(nFixed).Elem()
					} else {
						elemTyp = &vm.Type{Rtype: rtyp.In(nFixed).Elem()}
					}
					c.emit(t, vm.MkSlice, narg-nFixed, c.typeSym(elemTyp).Index)
					callNarg = nFixed + 1
				}
				callNret |= int(vm.CallSpreadFlag)
			}
			c.emit(t, vm.Call, callNarg, callNret)

		case lang.Colon:
			// Struct field key: field name is in Arg[0], only the value is on the stack.
			if fieldName, ok := t.FieldKeyName(); ok {
				vs := pop()
				ts := top()
				tsType := ts.Type
				if ts.IsPtr() {
					tsType = ts.Type.Elem()
				}
				if !tsType.IsStruct() {
					break
				}
				j, ft := tsType.FieldLookup(fieldName)
				if j == nil {
					break
				}
				if ft != nil && ft.Kind() == reflect.Func {
					c.emit(t, vm.WrapFunc, c.typeIndex(ft))
				}
				c.emitNumConvert(t, ft, vs.Type, 0)
				c.emitIfaceWrap(t, ft, vs.Type)
				c.emit(t, vm.FieldSet, j...)
				break
			}
			vs := pop() // value
			ks := pop() // key or index
			ts := top()
			if ts.IsPtr() {
				// Resolve index on the element type
				ts = &symbol.Symbol{Kind: symbol.Value, Type: ts.Type.Elem()}
			}
			// `key: value` in a map composite literal.
			// The key may be any kind of expression, so this must come before the ks.Kind switch.
			if ts.Type != nil && ts.Type.Kind() == reflect.Map {
				elemTyp := ts.Type.Elem()
				c.emitNumConvert(t, ts.Type.Key(), ks.Type, 1)
				// Elided composite key for a *T key type denotes &T{...}; key is at depth 1.
				if ts.Type.Key().IsPtr() && ks.Kind == symbol.Type {
					c.emit(t, vm.Swap, 0, 1)
					c.emit(t, vm.Addr)
					c.emit(t, vm.Swap, 0, 1)
				}
				if elemTyp.IsPtr() && vs.Kind == symbol.Type {
					c.emit(t, vm.Addr)
				}
				c.emitNumConvert(t, elemTyp, vs.Type, 0)
				c.emitMapValueWrap(t, elemTyp, vs)
				c.emit(t, vm.MapSet)
				break
			}
			switch ks.Kind {
			case symbol.Const:
				switch ts.Type.Kind() {
				case reflect.Struct:
					if ks.Value.CanInt() {
						fieldIdx := int(ks.Value.Int())
						if fieldIdx < len(ts.Type.Fields) {
							ft := ts.Type.Fields[fieldIdx]
							if ft != nil && ft.Kind() == reflect.Func {
								c.emit(t, vm.WrapFunc, c.typeIndex(ft))
							}
							c.emitNumConvert(t, ft, vs.Type, 0)
							c.emitIfaceWrap(t, ft, vs.Type)
						}
						c.emit(t, vm.FieldFset)
					}
				case reflect.Array, reflect.Slice:
					if ts.Type.Elem().IsPtr() && vs.Kind == symbol.Type {
						c.emit(t, vm.Addr)
					}
					c.emitNumConvert(t, ts.Type.Elem(), vs.Type, 0)
					c.emitIfaceWrap(t, ts.Type.Elem(), vs.Type)
					c.emit(t, vm.IndexSet)
				}

			case symbol.Type, symbol.Unset, symbol.Generic, symbol.Builtin, symbol.Pkg:
				fieldName := ks.Name
				if ks.Kind == symbol.Type {
					// Field name matches a type name: Ident emitted a spurious Fnew for it.
					if ts.Type.Kind() != reflect.Struct || ks.Type == nil {
						break
					}
					fieldName = ks.Type.Name
				}
				j, ft := ts.Type.FieldLookup(fieldName)
				if j == nil {
					break
				}
				if ks.Kind == symbol.Type && !ks.NoFnew {
					c.removeFnew(ks.Index)
				}
				if ft != nil && ft.Kind() == reflect.Func {
					c.emit(t, vm.WrapFunc, c.typeIndex(ft))
				}
				c.emitNumConvert(t, ft, vs.Type, 0)
				c.emitIfaceWrap(t, ft, vs.Type)
				c.emit(t, vm.FieldSet, j...)

			case symbol.LocalVar, symbol.Var:
				if ts.Type == nil || ts.Type.Kind() != reflect.Struct {
					break
				}
				fieldName := ks.Name
				if j := strings.LastIndex(fieldName, "/"); j >= 0 {
					fieldName = fieldName[j+1:]
				}
				j, ft := ts.Type.FieldLookup(fieldName)
				if j == nil {
					break
				}
				if ks.Kind == symbol.LocalVar {
					c.removeGetLocal(ks.Index)
				} else {
					c.removeGetGlobal(ks.Index)
				}
				if ft != nil && ft.Kind() == reflect.Func {
					c.emit(t, vm.WrapFunc, c.typeIndex(ft))
				}
				c.emitNumConvert(t, ft, vs.Type, 0)
				c.emitIfaceWrap(t, ft, vs.Type)
				c.emit(t, vm.FieldSet, j...)
			}

		case lang.Composite:
			sliceLen := t.Arg[0].(int)
			// Patch this literal's nil slice/map Fnew (B=-1) to its length.
			idx := int32(-1)
			var typ *vm.Type
			if sym := c.Symbols[t.Str]; sym != nil {
				typ, idx = sym.Type, int32(sym.Index)
			}
			c.patchNilFnewLen(idx, typ, sliceLen)
			// Mark the stack top as a composite literal value so that a
			// subsequent Period treats it as a method call, not a method
			// expression (Type.Method).
			if len(stack) > 0 && top().Kind == symbol.Type {
				s := *top()
				s.Composite = true
				stack[len(stack)-1] = &s
			}

		case lang.Grow:
			growPos = append(growPos, len(c.Code))
			maxExprDepth = append(maxExprDepth, 0)
			hasDefer = append(hasDefer, false)
			c.emit(t, vm.Grow, t.Arg[0].(int))
			// Allocate a heap cell for each captured named return so a capturing closure shares the slot.
			cellRet, _ := t.Arg[1].([]int)
			retCellSlots = append(retCellSlots, cellRet)
			if len(cellRet) > 0 {
				// Mark this frame as having captured named returns so the
				// Return opcode and panicUnwind finalize results from the
				// fixed slots (deref cells) after defers.
				c.emit(t, vm.MarkNamedRet)
				for _, idx := range cellRet {
					c.emit(t, vm.GetLocal, idx)
					c.emit(t, vm.HeapAlloc)
					c.emit(t, vm.SetLocal, idx, 0)
				}
			}
			// Box captured params into cells unconditionally (no MarkNamedRet:
			// params are not named returns), so a conditional/absent reassign
			// can't leave the slot a plain value the closure capture misreads.
			if cellParams, _ := t.Arg[2].([]int); len(cellParams) > 0 {
				for _, idx := range cellParams {
					c.emit(t, vm.GetLocal, idx)
					c.emit(t, vm.HeapAlloc)
					c.emit(t, vm.SetLocal, idx, 0)
				}
			}

		case lang.Define:
			showStack(stack)
			n := t.Arg[0].(int)
			if err := checkTopN(t, n); err != nil {
				return err
			}
			if len(stack) < 2*n {
				return c.errAt(t, "internal: define stack underflow (have %d symbols, need %d)", len(stack), 2*n)
			}
			l := len(stack)
			rhs := stack[l-n:]
			truncStack(l - n)
			l = len(stack)
			lhs := stack[l-n:]
			truncStack(l - n)
			showStack(stack)
			// Local define: initialize local slots and assign via Set.
			if n > 0 && lhs[0].Kind == symbol.LocalVar {
				for i, r := range rhs {
					typ := r.Type
					if typ == nil {
						switch {
						case r.Name == "nil" && lhs[i].Type != nil:
							// Bare nil to a same-scope := rebind is an assignment to
							// the already-typed var (`wasEmpty, empty := empty, nil`).
							if !isNilableType(lhs[i].Type) {
								return c.errAt(t, "cannot use nil as %s value in assignment", lhs[i].Type)
							}
							typ = lhs[i].Type
						case !r.Value.Reflect().IsValid():
							return c.errUndef(t, lhs[i].Name)
						default:
							typ = vm.TypeOf(r.Value.Interface())
						}
					}
					lhs[i].Type = typ
					if !lhs[i].NeedsCell() {
						typeIdx := c.typeSym(typ).Index
						c.fixPtrFnewE(typ, typeIdx)
						c.emit(t, vm.New, lhs[i].Index, typeIdx)
					}
					lhs[i].Used = true
				}
				for i := n - 1; i >= 0; i-- {
					if rhs[i].Type == nil && rhs[i].Name == "nil" {
						// Typed-nil coerce for concrete nilable LHS types.
						c.emitNilCoerce(t, rhs[i], lhs[i].Type, 0)
					}
					if lhs[i].NeedsCell() {
						// Convert to the declared type so the cell is typed *T, not *int.
						if isNumericConvType(lhs[i].Type) {
							c.emit(t, vm.Convert, c.typeSym(lhs[i].Type).Index, 0)
						}
						c.emit(t, vm.HeapAlloc)
						lhs[i].CellSlot = true
					}
					c.emit(t, vm.SetLocal, lhs[i].Index, 0)
				}
				c.emit(t, vm.Pop, n)
				break
			}
			for i, r := range rhs {
				// Propage type of rhs to lhs.
				typ := r.Type
				if typ == nil {
					if !r.Value.Reflect().IsValid() {
						return c.errUndef(t, lhs[i].Name)
					}
					typ = vm.TypeOf(r.Value.Interface())
				}
				// If lhs has an interface type, keep it and wrap the concrete value.
				if lhs[i].Type != nil && lhs[i].Type.IsInterface() && !typ.IsInterface() {
					c.emitIfaceWrap(t, lhs[i].Type, typ)
					c.Data[lhs[i].Index] = c.typeSlotValue(lhs[i].Index, lhs[i].Type, false)
				} else {
					lhs[i].Type = typ
					c.Data[lhs[i].Index] = c.typeSlotValue(lhs[i].Index, typ, false)
				}
			}
			c.emit(t, vm.SetS, n)

		case lang.Assign:
			n := t.Arg[0].(int)
			if err := checkTopN(t, n); err != nil { // check rhs values (top n items)
				return err
			}
			if n > 1 {
				// Batched multi-assign: compiler stack has [lhs0..lhs_(n-1), rhs0..rhs_(n-1)].
				// All RHS were pushed before any assignment, so swaps like a,b=b,a work correctly.
				l := len(stack)
				rhss := stack[l-n:]
				truncStack(l - n)
				lhss := stack[len(stack)-n:]
				truncStack(len(stack) - n)
				// Process from top of stack (rhs[n-1]) down to rhs[0].
				// Blank idents (Kind=Unset) have no slot on the VM stack; just discard their rhs.
				slotCount, namedAbove := 0, 0
				for i := n - 1; i >= 0; i-- {
					if lhss[i].Kind == symbol.Unset {
						c.emit(t, vm.Pop, 1) // discard rhs for blank ident
						continue
					}
					c.emitIfaceWrap(t, lhss[i].Type, rhss[i].Type)
					c.emitNumConvert(t, lhss[i].Type, rhss[i].Type, 0)
					switch {
					case lhss[i].Kind == symbol.LocalVar && freeVarIndex(lhss[i].Name) >= 0:
						// Captured variable: write through the closure's heap cell.
						c.emit(t, vm.HeapSet, freeVarIndex(lhss[i].Name))
						slotCount++
						namedAbove++
					case lhss[i].Kind == symbol.LocalVar:
						// Param slots alias the caller's Value; SetLocal would write through to the caller.
						// Detach via vm.New, matching the single-assign branch below.
						if (!lhss[i].Used || lhss[i].IsParam()) && !lhss[i].NeedsCell() {
							typeIdx := c.typeSym(lhss[i].Type).Index
							c.fixPtrFnewE(lhss[i].Type, typeIdx)
							c.emit(t, vm.New, lhss[i].Index, typeIdx)
							lhss[i].Used = true
						}
						if lhss[i].CellSlot {
							c.emit(t, vm.CellSet, lhss[i].Index)
						} else {
							c.emit(t, vm.SetLocal, lhss[i].Index, 0)
						}
						slotCount++
						namedAbove++
					case lhss[i].Index != symbol.UnsetAddr:
						c.emit(t, vm.SetGlobal, lhss[i].Index, 0)
						slotCount++
						namedAbove++
					default:
						// Struct-field lhs (Index==UnsetAddr): field reflect.Value is on the VM
						// stack at depth D = namedAbove + i + 1 below the rhs at the top.
						// Bubble it to sp-2 via D-1 Swaps, then SetS(1) assigns and pops both.
						d := namedAbove + i + 1
						for j := 0; j < d-1; j++ {
							c.emit(t, vm.Swap, d-j, d-j-1)
						}
						c.emit(t, vm.FieldRefSet)
					}
				}
				if slotCount > 0 {
					c.emit(t, vm.Pop, slotCount) // pop lhs copies for local/global vars
				}
				break
			}
			rhs := pop()
			lhs := pop()
			if lhs.Kind == symbol.Unset {
				c.emit(t, vm.Pop, 1)
				break
			}
			if lhs.Kind == symbol.LocalVar {
				// Type-switch case var has no parse-time type; infer from RHS so typeSym(nil) doesn't panic at New.
				if lhs.Type == nil && rhs.Type != nil {
					lhs.Type = rhs.Type
					if sym := c.Symbols[lhs.Name]; sym != nil && sym.Type == nil {
						sym.Type = rhs.Type
					}
				}
				// Captured variable write inside closure body: use HeapSet.
				if idx := freeVarIndex(lhs.Name); idx >= 0 {
					c.emit(t, vm.HeapSet, idx)
					c.emit(t, vm.Pop, 1) // pop stale value pushed by HeapGet in Ident
					break
				}
				// Param slots alias the caller's pushed Value, so SetLocal would write through dst.ref to the caller.
				if !lhs.Used || lhs.IsParam() {
					typeIdx := c.typeSym(lhs.Type).Index
					// A cell-promoted ptr var still needs its zero fixed from
					// new-elem (FnewE) to nil ptr, else the cell snapshots a
					// spurious elem value (&captured then types one star short).
					c.fixPtrFnewE(lhs.Type, typeIdx)
					if !lhs.NeedsCell() {
						c.emit(t, vm.New, lhs.Index, typeIdx)
					}
					lhs.Used = true
				}
				// Wrap concrete value in Iface when assigning to interface local.
				c.emitIfaceWrap(t, lhs.Type, rhs.Type)
				c.emitNumConvert(t, lhs.Type, rhs.Type, 0)
				switch {
				case lhs.CellSlot:
					c.emit(t, vm.CellSet, lhs.Index)
					c.emit(t, vm.Pop, 1) // pop stale lhs value left by Ident's Get
				case lhs.NeedsCell() && !lhs.CellSlot:
					c.emit(t, vm.HeapAlloc)
					lhs.CellSlot = true
					c.emit(t, vm.SetLocal, lhs.Index, 0)
					c.emit(t, vm.Pop, 1) // pop stale lhs value left by Ident's Get
				default:
					if !c.fuseLocalAssign(t, lhs.Index) {
						c.emit(t, vm.SetLocal, lhs.Index, 0)
						c.emit(t, vm.Pop, 1) // pop stale lhs value left by Ident's Get
					}
				}
				break
			}
			c.emitNumConvert(t, lhs.Type, rhs.Type, 0)
			if lhs.Index != symbol.UnsetAddr {
				if v := c.Data[lhs.Index]; !v.IsValid() {
					// A declared LHS type owns the (deferred) slot; infer from the
					// RHS only for an untyped var, else a typed global retypes wrong.
					typ := lhs.Type
					if typ == nil {
						typ = rhs.Type
					}
					if typ != nil {
						c.Data[lhs.Index] = c.typeSlotValue(lhs.Index, typ, false)
						if sym := c.Symbols[lhs.Name]; sym != nil && sym.Type == nil {
							sym.Type = typ
						}
					}
				}
			}
			// Wrap concrete value in Iface when assigning to interface variable.
			c.emitIfaceWrap(t, lhs.Type, rhs.Type)
			if lhs.Index == symbol.UnsetAddr {
				// Struct field ref: route through setFuncField which unwraps
				// Iface for native types so reflect-based code sees raw values.
				c.emit(t, vm.FieldRefSet)
			} else {
				// SetGlobal writes m.globals[idx] directly; SetS would mutate only
				// the LHS stack copy, losing a resolveFuncField reassign. Pop drops
				// that copy (as multi-assign does).
				c.emit(t, vm.SetGlobal, lhs.Index, 0)
				c.emit(t, vm.Pop, 1)
			}

		case lang.DerefAssign:
			if err := checkTopN(t, 2); err != nil { // check rhs and pointer target
				return err
			}
			rhsStart := codeStarts[len(stack)-1]
			rhs := pop()
			lhs := pop() // pointer, not yet dereferenced
			if lhs.Type != nil && lhs.Type.IsPtr() {
				c.convertOperand(t, rhs, rhsStart, lhs.Type.Elem(), 0)
			}
			c.emit(t, vm.DerefSet)

		case lang.IndexAssign:
			if err := checkTopN(t, 3); err != nil { // check container, index, and value
				return err
			}
			s := stack[len(stack)-3]
			typ := s.Type
			if typ.IsPtr() {
				typ = typ.Elem()
			}
			kind := typ.Kind()
			// Peephole: `arr[i] = boolConst` collapses the bool load,
			// IndexSet, and trailing Pop into a single IndexSetBool op.
			if (kind == reflect.Array || kind == reflect.Slice) && len(c.Code) > 0 &&
				!c.labelAtPos[len(c.Code)-1] {
				val := stack[len(stack)-1]
				if val.Kind == symbol.Const && val.Cval != nil && val.Cval.Kind() == constant.Bool {
					if last := c.Code[len(c.Code)-1].Op; last == vm.GetGlobal || last == vm.Push {
						c.Code = c.Code[:len(c.Code)-1]
						b := 0
						if constant.BoolVal(val.Cval) {
							b = 1
						}
						c.emit(t, vm.IndexSetBool, b)
						truncStack(len(stack) - 3)
						break
					}
				}
			}
			// Coerce value (and map key) to the container's elem/key type:
			// an untyped const folds at compile time, else runtime Convert.
			switch kind {
			case reflect.Array, reflect.Slice:
				c.convertOperand(t, stack[len(stack)-1], codeStarts[len(stack)-1], typ.Elem(), 0)
				c.emit(t, vm.IndexSet)
			case reflect.Map:
				c.convertOperand(t, stack[len(stack)-1], codeStarts[len(stack)-1], typ.Elem(), 0)
				c.convertOperand(t, stack[len(stack)-2], codeStarts[len(stack)-2], typ.Key(), 1)
				c.emit(t, vm.MapSet)
			default:
				return c.errAt(t, "not a map or array: %s", s.Name)
			}
			c.emit(t, vm.Pop, 1)
			truncStack(len(stack) - 3)

		case lang.Equal:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			leftStart := codeStarts[len(stack)-2]
			s2Start := codeStarts[len(stack)-1]
			s2, s1 := pop(), pop()
			if h, err := foldBinaryConst(t, s1, s2, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			if err := c.errIfMismatch(t, s1, s2); err != nil {
				return err
			}
			typ := arithmeticOpType(s2, s1)
			c.convertOperand(t, s2, s2Start, typ, 0)
			c.convertOperand(t, s1, leftStart, typ, 1)
			push(&symbol.Symbol{Type: booleanOpType(s2, s1)})
			c.emit(t, vm.Equal)

		case lang.EqualSet:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			s2Start := codeStarts[len(stack)-1]
			s2, s1 := pop(), pop()
			// A case compare is `operand == value`: fold/convert an untyped-const
			// case value to the switch operand's type, as lang.Equal does. The
			// type rides on the token: the model's s1 entry is not the operand
			// past the first case of the chain. Same guards as lang.Equal: a
			// typed mismatched case value is a compile error, and a case
			// constant must be representable (no silent runtime truncation).
			if len(t.Arg) > 0 {
				if opTyp, ok := t.Arg[0].(*vm.Type); ok && opTyp != nil {
					if err := c.errIfMismatch(t, &symbol.Symbol{Kind: symbol.Var, Type: opTyp}, s2); err != nil {
						return err
					}
					if err := c.errIfUnrepresentable(t, s2, opTyp); err != nil {
						return err
					}
					c.convertOperand(t, s2, s2Start, opTyp, 0)
				}
			}
			push(&symbol.Symbol{Type: booleanOpType(s2, s1)})
			c.emit(t, vm.EqualSet)

		case lang.Ident:
			var s *symbol.Symbol
			if typ := t.ResolvedType(); typ != nil {
				// Type reference carrying its resolved type by identity: push a
				// symbol bound to the type's shared zero-value slot, bypassing the
				// name lookup against the (mutable, shared) symbol table. The symbol
				// carries the precise *vm.Type so method resolution stays exact even
				// when distinct types share the slot's rtype.
				s = &symbol.Symbol{Kind: symbol.Type, Name: t.Str, Type: typ, Index: c.zeroTypeSlot(typ)}
			} else {
				var ok bool
				if s, ok = c.symAt(t.Str); !ok {
					// It could be either an undefined symbol or a key ident in a literal composite expr.
					s = &symbol.Symbol{Name: t.Str}
				} else if s.Kind == symbol.Const {
					// Copy so foldConstLoad's per-use type rewrite doesn't fix the
					// shared const's type and overflow a later use's context.
					cs := *s
					s = &cs
				}
			}
			push(s)
			if s.Kind == symbol.Pkg || s.Kind == symbol.Unset || s.Kind == symbol.Builtin || s.Kind == symbol.Generic {
				break
			}
			// A dot-imported bridged constant (bare name, e.g. Pi from
			// `import . "math"`) is bound as a plain Value; wrap the stack entry
			// as a Const carrying its package's high-precision Cval so a fully
			// constant expression folds at full precision. The value still loads
			// via the normal path below (and is retracted if the expr folds).
			if s.Kind == symbol.Value && s.PkgPath != "" {
				if pkg, ok := c.Packages[s.PkgPath]; ok {
					if cv, isC := pkg.Cvals[s.Name]; isC && cv != nil {
						cs := *s
						cs.Kind = symbol.Const
						cs.Cval = cv
						stack[len(stack)-1] = &cs
					}
				}
			}
			// Closure creation: emit code address + captured cell pointers + MkClosure.
			if s.Kind == symbol.Func && len(s.FreeVars) > 0 {
				reserveDepth(len(s.FreeVars))
				c.emit(t, vm.GetGlobal, s.Index)
				// Determine the current function's FreeVars for transitive capture.
				var outerCloSym *symbol.Symbol
				if cf := curFunc(); cf != "" {
					outerCloSym, _ = c.symAt(cf)
				}
				for _, fvName := range s.FreeVars {
					fvSym := c.Symbols[fvName]
					if fvSym == nil {
						return c.errUndef(t, fvName)
					}
					if outerCloSym != nil {
						if idx := outerCloSym.FreeVarIndex(fvName); idx >= 0 {
							// The free variable is already captured in the enclosing closure's Heap.
							// Use HeapPtr to push the existing cell pointer (transitive capture).
							c.emit(t, vm.HeapPtr, idx)
							continue
						}
					}
					if fvSym.Kind == symbol.LocalVar {
						c.emit(t, vm.GetLocal, fvSym.Index)
						if !fvSym.CellSlot {
							c.emit(t, vm.HeapAlloc) // snapshot: not promoted to cell
						}
					} else {
						c.emit(t, vm.GetGlobal, fvSym.Index)
						c.emit(t, vm.HeapAlloc)
					}
				}
				c.emit(t, vm.MkClosure, len(s.FreeVars))
				break
			}
			// Captured variable read inside a closure body: use HeapGet.
			if cf := curFunc(); cf != "" {
				if cloSym, ok := c.symAt(cf); ok && cloSym != nil {
					if idx := cloSym.FreeVarIndex(t.Str); idx >= 0 {
						c.emit(t, vm.HeapGet, idx)
						break
					}
				}
			}
			if s.Kind == symbol.LocalVar && s.CellSlot {
				c.emit(t, vm.CellGet, s.Index)
				break
			}
			// Regular local or global access.
			// Type symbols are always in global Data.
			if s.Kind == symbol.LocalVar {
				// Once the slot's address has been taken in this function, all
				// future reads of it must re-sync num from ref (a native callee
				// may have written through the pointer). Skip the GetLocal2
				// fusion so the sync variant fires unconditionally for both.
				addressed := len(addressedSlots) > 0 && addressedSlots[len(addressedSlots)-1][s.Index]
				switch {
				case addressed:
					c.emit(t, vm.GetLocalSync, s.Index)
				case c.fuseGetLocal(vm.GetLocal2, s.Index):
					// fused; nothing more to do
				default:
					c.emit(t, vm.GetLocal, s.Index)
				}
			} else {
				// Inline an integer constant as an immediate Push so the
				// immediate-fusion fast paths (e.g. `j <= N`) fire. The pushed
				// symbol still carries its Cval, so a fully-constant expression
				// folds regardless of this load form.
				if s.Kind == symbol.Const && c.emitConstImm(t, s) {
					break
				}
				if s.Index == symbol.UnsetAddr {
					// Type or value symbol discovered during Phase 2 code generation.
					if s.Kind == symbol.Type && s.Type != nil {
						// Share the canonical zero-value slot (by rtype) so a name-
						// keyed type ident and a carried-type ident resolve to the
						// same Fnew slot -- the composite handler patches it by type.
						// s keeps its own .Type, so method resolution stays exact.
						s.Index = c.zeroTypeSlot(s.Type)
					} else {
						s.Index = len(c.Data)
						c.Data = append(c.Data, s.Value)
					}
				}
				// Type idents tagged by the parser as non-composite skip the speculative Fnew.
				// Mark the stack entry so consumer ops know not to scan back for a matching Fnew.
				if s.Kind == symbol.Type && t.NoFnew() {
					sc := *s
					sc.NoFnew = true
					stack[len(stack)-1] = &sc
					break
				}
				c.emitTypeOrGlobal(t, s, s.Index)
			}

		case lang.Label:
			if expected, ok := jumpDepth[t.Str]; ok && len(stack) != expected {
				return fmt.Errorf("stack depth mismatch at label %s: got %d, want %d", t.Str, len(stack), expected)
			}
			lc := len(c.Code)
			c.labelAtPos[lc] = true // signal fuseCmpJump to leave this position's JumpFalse standalone
			// In Phase-2 deferred bodies, label keys still use the bare func/method name.
			// Prefer this pkg's qualified Symbol when both exist.
			labelKey := t.Str
			if qk := c.qualifyLabel(t.Str); qk != t.Str {
				if _, ok := c.labelSym(qk); ok {
					labelKey = qk
				}
			}
			// Route a func "_end" to the funcexit handling (else branch) via the
			// per-compile funcStack, so a stale Label symbol from a prior committed
			// compile can't divert it into the first branch and skip truncStack.
			endBase, isEnd := strings.CutSuffix(t.Str, "_end")
			isFuncEnd := isEnd && len(funcStack) > 0 && funcStack[len(funcStack)-1] == endBase
			if s, ok := c.labelSym(labelKey); ok && !isFuncEnd {
				s.Value = vm.ValueOf(lc)
				if s.Kind == symbol.Func {
					// Label is a function entry point, update its code address in data.
					if s.Index == symbol.UnsetAddr {
						// Method registered during Phase 2 func body parsing.
						s.Index = len(c.Data)
						c.Data = append(c.Data, s.Value)
					} else {
						c.Data[s.Index] = s.Value
					}
					flen = append(flen, len(stack))
					funcStack = append(funcStack, t.Str)
					funcStartStack = append(funcStartStack, lc)
					addressedSlots = append(addressedSlots, nil)
					// Register method in its receiver type's method table, prefer the qualified key.
					if parts := strings.SplitN(t.Str, ".", 2); len(parts) == 2 {
						typeName := strings.TrimPrefix(parts[0], "*")
						if ts, ok := c.symAt(typeName); ok && ts.Kind == symbol.Type {
							id := c.methodID(parts[1])
							for len(ts.Type.Methods) <= id {
								ts.Type.Methods = append(ts.Type.Methods, vm.Method{Index: -1})
							}
							var msig *vm.Type
							if s.Type != nil && s.Type.Kind() == reflect.Func {
								msig = s.Type // materialize-time source of Rtype; filled by MaterializeAll
							}
							ts.Type.Methods[id] = vm.Method{Index: s.Index, PtrRecv: strings.HasPrefix(parts[0], "*"), Sig: msig}
						}
					}
				} else {
					if s.Index == symbol.UnsetAddr {
						s.Index = len(c.Data)
						c.Data = append(c.Data, s.Value)
					} else {
						c.Data[s.Index] = s.Value
					}
				}
			} else {
				if isEnd {
					endKey := endBase
					if qk := c.qualifyLabel(endBase); qk != endBase {
						if _, ok := c.Symbols[qk]; ok {
							endKey = qk
						}
					}
					if s, ok = c.Symbols[endKey]; ok && s.Kind == symbol.Func {
						// Patch the Grow instruction with max expression depth for bounds-check-free GetLocal.
						if len(growPos) > 0 {
							gp := growPos[len(growPos)-1]
							c.Code[gp].B = int32(maxExprDepth[len(maxExprDepth)-1])
							growPos = growPos[:len(growPos)-1]
							maxExprDepth = maxExprDepth[:len(maxExprDepth)-1]
							hasDefer = hasDefer[:len(hasDefer)-1]
							retCellSlots = retCellSlots[:len(retCellSlots)-1]
						}
						// Exit function: restore caller stack and function name tracking.
						l := popflen()
						truncStack(l)
						top := len(funcStack) - 1
						c.FuncRanges = append(c.FuncRanges, vm.FuncRange{
							Start: funcStartStack[top],
							End:   lc,
							Name:  funcStack[top],
						})
						funcStack = funcStack[:top]
						funcStartStack = funcStartStack[:top]
						addressedSlots = addressedSlots[:top]
					}
				}
				c.SymSet(c.qualifyLabel(t.Str), &symbol.Symbol{Kind: symbol.Label, Value: vm.ValueOf(lc)})
			}

		case lang.Len:
			push(&symbol.Symbol{Type: c.Symbols["int"].Type})
			c.emit(t, vm.Len, t.Arg[0].(int))

		case lang.JumpFalse:
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			// Peephole: a trailing `Not` flips the branch sense. Drop it and
			// dispatch to the JumpTrue-flavored fusions, which lets `<=`/`>=`
			// loop conditions and `if !cond` reach the fused compare-and-branch
			// fast paths instead of paying for a separate Not.
			if len(c.Code) > 0 && c.Code[len(c.Code)-1].Op == vm.Not &&
				!c.labelAtPos[len(c.Code)-1] && !c.labelAtPos[len(c.Code)] {
				c.Code = c.Code[:len(c.Code)-1]
				if c.fuseCmpJump(t, &fixList, vm.LowerIntImm, vm.LowerIntImmJumpTrue,
					vm.GetLocalLowerIntImm, vm.GetLocalLowerIntImmJumpTrue, 0) ||
					c.fuseCmpJump(t, &fixList, vm.GreaterIntImm, vm.LowerIntImmJumpFalse,
						vm.GetLocalGreaterIntImm, vm.GetLocalLowerIntImmJumpFalse, 1) {
					break
				}
				c.emitJump(t, &fixList, vm.JumpTrue)
				break
			}
			if c.fuseCmpJump(t, &fixList, vm.LowerIntImm, vm.LowerIntImmJumpFalse,
				vm.GetLocalLowerIntImm, vm.GetLocalLowerIntImmJumpFalse, 0) ||
				c.fuseCmpJump(t, &fixList, vm.GreaterIntImm, vm.LowerIntImmJumpTrue,
					vm.GetLocalGreaterIntImm, vm.GetLocalLowerIntImmJumpTrue, 1) {
				break
			}
			c.emitJump(t, &fixList, vm.JumpFalse)

		case lang.JumpSetFalse, lang.JumpSetTrue:
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			pop()                             // LHS result: consumed on the non-jumping path; both paths leave one value at label.
			jumpDepth[t.Str] = len(stack) + 1 // one value (LHS or RHS) arrives at the merge label
			op := vm.JumpSetFalse
			if t.Tok == lang.JumpSetTrue {
				op = vm.JumpSetTrue
			}
			c.emitJump(t, &fixList, op)

		case lang.Goto:
			c.emitJump(t, &fixList, vm.Jump)

		case lang.Drop:
			// A switch's no-match drop is a control-flow merge the linear model
			// can't always represent: return-terminated case bodies empty the
			// model while the runtime miss path still holds the operand. Pop the
			// model only when it has an in-frame value; always emit the Pop.
			base := 0
			if len(flen) > 0 {
				base = flen[len(flen)-1]
			}
			if len(stack) > base {
				pop()
			}
			c.emit(t, vm.Pop, 1)

		case lang.PopExpr:
			if t.Arg[0].(int) == 0 {
				// Mark: save the compile-time stack depth before the expression.
				exprBaseStack = append(exprBaseStack, len(stack))
			} else {
				// Pop unused return values left by the expression statement.
				if n := len(exprBaseStack); n > 0 {
					exprBase := exprBaseStack[n-1]
					exprBaseStack = exprBaseStack[:n-1]
					if len(stack) > exprBase {
						excess := len(stack) - exprBase
						for range excess {
							pop()
						}
						c.emit(t, vm.Pop, excess)
					}
				}
			}

		case lang.Period:
			if len(stack) < 1 {
				return c.errAt(t, "missing symbol")
			}
			if err := checkTopN(t, 1); err != nil {
				return err
			}
			s := pop()
			switch s.Kind {
			case symbol.Pkg:
				p, ok := c.Packages[s.PkgPath]
				if !ok {
					return c.errAt(t, "package not found: %s", s.PkgPath)
				}
				v, ok := p.Values[t.Str[1:]]
				if !ok {
					return c.errAt(t, "symbol not found in package %s: %s", s.PkgPath, t.Str[1:])
				}
				name := s.PkgPath + t.Str
				var l int
				sym, _, ok := c.Symbols.Get(name, "")
				switch {
				case ok && sym.Index != symbol.UnsetAddr:
					l = sym.Index
				case ok:
					// Symbol exists (e.g. a Const placeholder registered with
					// UnsetAddr) but has no Data slot yet. Allocate one now so
					// the emitted GetGlobal lands on a valid global index.
					l = len(c.Data)
					c.Data = append(c.Data, v)
					sym.Index = l
				default:
					l = len(c.Data)
					if rtype, ok := v.UnwrapType(); ok {
						nv := vm.NewValue(rtype)
						c.Data = append(c.Data, nv)
						c.SymAdd(l, name, nv, symbol.Type, &vm.Type{Name: rtype.Name(), Rtype: rtype})
					} else {
						c.Data = append(c.Data, v)
						// Use the reflect.Value's static type (v.Type()), not the dynamic type via v.Interface().
						rt := v.Type()
						c.SymAdd(l, name, v, symbol.Value, &vm.Type{Name: rt.Name(), Rtype: rt})
					}
					sym = c.Symbols[name]
				}
				// A bridged high-precision constant (e.g. math.Pi): push a Const
				// carrying its exact Cval so a fully-constant expression folds at
				// full precision. The bridged value load is emitted as before and
				// retracted if the expression folds.
				if cv, isConst := p.Cvals[t.Str[1:]]; isConst && cv != nil {
					push(&symbol.Symbol{Kind: symbol.Const, Value: sym.Value, Cval: cv, Type: sym.Type})
				} else {
					push(sym)
				}
				c.emitTypeOrGlobal(t, sym, l)
			case symbol.Unset:
				return c.errAt(t, "invalid symbol: %s", s.Name)
			default:
				// Dynamic dispatch for interface receiver.
				if s.Type != nil && s.Type.IsInterface() {
					methodName := t.Str[1:]
					methodSym := c.resolveIfaceMethodSym(s.Type, methodName)
					if methodSym == nil {
						return c.errUndef(t, methodName)
					}
					push(methodSym)
					c.emit(t, vm.IfaceCall, c.methodID(methodName))
					break
				}
				if m, fieldPath := c.Symbols.MethodByName(s, t.Str[1:], c.Seg); m != nil {
					// Method expression: Type.Method yields a func with receiver as first arg.
					// A composite literal (T{}.Method) is a value, not a method expression.
					if s.Kind == symbol.Type && !s.Composite {
						if !s.NoFnew {
							c.removeFnew(s.Index)
						}
						// Emit a runtime func value so the method expression works
						// stored, not just inline-called (Call retracts it for the fast
						// path). Receiver pointer-ness must match the expression's:
						// T.M (value recv) and (*T).M (ptr recv) pass args[0] through
						// unchanged; the mixed (*T).M-on-value-recv form would need a
						// deref in the wrapper, so it stays symbolic-only.
						var exprType *vm.Type
						if strings.HasPrefix(m.Name, "*") == s.Type.IsPtr() {
							exprType = c.methodExprType(s.Type, m.Type)
						}
						sym := &symbol.Symbol{
							Kind:       symbol.Func,
							Name:       m.Name,
							Index:      m.Index,
							Type:       m.Type,
							MethodExpr: true,
						}
						if exprType != nil {
							sym.Type = exprType
							meStart := len(c.Code)
							c.emit(t, vm.MkMethodExpr, m.Index, c.typeIndex(exprType))
							pushAt(sym, meStart)
							break
						}
						push(sym)
						break
					}
					push(m)
					// Extract embedded receiver if method is promoted through embedded fields.
					if len(fieldPath) > 0 {
						c.emitField(t, fieldPath)
					}
					// Determine if auto-deref or auto-addr is needed.
					methodWantsPtr := strings.HasPrefix(m.Name, "*")
					recvIsPtr := s.Type.Kind() == reflect.Pointer
					if len(fieldPath) > 0 {
						if ft := s.Type.FieldTypeAtPath(fieldPath); ft != nil {
							recvIsPtr = ft.Kind() == reflect.Pointer
						}
					}
					switch {
					case methodWantsPtr && !recvIsPtr:
						// A bare local receiver (no field path) must be addressed via
						// AddrLocal so the pointer aliases the slot; plain Addr on a
						// numeric local boxes a detached copy and the method's
						// mutation is lost. Mark the slot so later reads re-sync.
						n := len(c.Code)
						switch {
						case n > 0 && c.Code[n-1].Op == vm.GetLocal:
							c.Code[n-1].Op = vm.AddrLocal
							c.Code[n-1].B = 0
							markAddressed(int(c.Code[n-1].A))
						case n > 0 && c.Code[n-1].Op == vm.GetLocal2:
							// Receiver is the second (top) fused load; split it off.
							idx := int(c.Code[n-1].B)
							c.Code[n-1].Op = vm.GetLocal
							c.Code[n-1].B = 0
							c.emit(t, vm.AddrLocal, idx, 0)
							markAddressed(idx)
						default:
							c.emit(t, vm.Addr)
						}
					case !methodWantsPtr && recvIsPtr:
						c.emit(t, vm.Deref)
					}
					// Closure-based method dispatch.
					// VM stack before Period: [..., receiver_value]
					// HeapAlloc: wrap receiver in a heap cell.
					// Get Global m.Index: push method code address above the cell.
					// Swap 0 1: put code addr below cell (MkClosure convention: code at sp-n-1).
					// MkClosure 1: produce Closure{code, [receiver_cell]}.
					c.emit(t, vm.HeapAlloc)
					if m.Index == symbol.UnsetAddr {
						// Method instantiated during this Phase-2 statement (e.g. (*Generic[T])(x).M()); its body is drained after it.
						// Reserve the global slot now so GetGlobal references it and the later Label fills it.
						m.Index = len(c.Data)
						c.Data = append(c.Data, m.Value)
					}
					c.emit(t, vm.GetGlobal, m.Index)
					c.emit(t, vm.Swap, 0, 1)
					c.emit(t, vm.MkClosure, 1)
					break
				}
				if s.Type == nil {
					return c.errUndef(t, s.Name)
				}
				// Native method expression: T.Method / (*T).Method where T is a
				// (bridged) type whose methods live on its reflect rtype, not in
				// mvm's symbol table (so MethodByName above returned nil). reflect's
				// Method.Func IS Go's method-expression func value (receiver as the
				// first parameter); emit it as a native func global. Works for direct
				// calls and as a stored/passed value via Call's native-func path.
				if s.Kind == symbol.Type && !s.Composite {
					mname := t.Str[1:]
					if mfunc, ok := c.rtype(s.Type).MethodByName(mname); ok {
						if !s.NoFnew {
							c.removeFnew(s.Index)
						}
						idx := len(c.Data)
						c.Data = append(c.Data, vm.FromReflect(mfunc.Func))
						// Kind:Value (not Func): the value is a native reflect func held in
						// a DATA slot, so it must be invoked via the value-call path (load +
						// dynamic Call). Kind:Func would trigger CallImm(Index), which treats
						// Index as a code address -> wrong for a data-slot func value.
						push(&symbol.Symbol{Kind: symbol.Value, Name: mname, Index: idx, Type: &vm.Type{Name: mname, Rtype: mfunc.Func.Type()}, Value: vm.FromReflect(mfunc.Func)})
						c.emit(t, vm.GetGlobal, idx)
						break
					}
				}
				// Probe fields symbolically so an interpreted receiver isn't
				// materialized; Elem/Kind fall back to Rtype for native types.
				styp := s.Type
				if styp.IsPtr() {
					styp = styp.Elem()
				}
				if styp.Kind() == reflect.Struct {
					structType := vm.CanonicalType(styp)
					fieldName := t.Str[1:]
					fieldPath, ft := structType.FieldLookup(fieldName)
					var foff uintptr
					if fieldPath != nil {
						foff = structType.FieldOffset(fieldPath)
					} else if typ := c.rtype(s.Type); typ != nil {
						// Symbolic miss: a method selector, a promoted field through a
						// forward-declared embedded type, or a native reflect.StructOf
						// layout. Fall back to the materialized rtype.
						if typ.Kind() == reflect.Pointer {
							typ = typ.Elem()
						}
						if st := c.findTypeSym(typ); st != nil {
							structType = st
						}
						if f, ok := typ.FieldByName(fieldName); ok {
							fieldPath = f.Index
							ft = structType.FieldType(fieldName)
							foff = fieldPathOffset(typ, fieldPath)
						}
					}
					if fieldPath != nil {
						push(&symbol.Symbol{
							Kind:           symbol.Var,
							Index:          symbol.UnsetAddr,
							Type:           ft,
							HasFieldOffset: true,
							FieldOffset:    foff,
						})
						c.emitField(t, fieldPath)
						break
					}
				}
				// Native method on concrete reflect type: use IfaceCall for
				// reflect-based dispatch at runtime.
				methodName := t.Str[1:]
				rtype := c.rtype(s.Type)
				if rtype == nil {
					// Unmaterialized receiver (e.g. forward-declared field): located error, not a nil deref.
					return c.errUndef(t, methodName)
				}
				rm, ok := rtype.MethodByName(methodName)
				needAddr := false
				if !ok && rtype.Kind() != reflect.Pointer {
					rm, ok = reflect.PointerTo(rtype).MethodByName(methodName)
					needAddr = true
				}
				// reflect.StructOf does not promote methods from embedded fields.
				// Walk embedded fields to find native methods promoted through embedding.
				var embFieldPath []int
				if !ok {
					lookupTyp := s.Type
					lr := rtype
					if lr.Kind() == reflect.Pointer {
						lr = lr.Elem()
						if lt := lookupTyp.Elem(); lt != nil {
							lookupTyp = lt
						}
					}
					// Look up the full mvm type (with Embedded info) from the symbol table.
					if ft := c.findTypeSym(lr); ft != nil {
						lookupTyp = ft
					}
					if fieldPath, mt, sig := findEmbeddedIfaceMethod(lookupTyp, methodName); fieldPath != nil {
						c.emitField(t, fieldPath)
						// mt is the embedded interface's bound method type (no receiver).
						// Validate findConcreteFuncSym against it.
						methodSym := c.findConcreteFuncSym(methodName)
						if methodSym != nil && mt != nil && mt.Kind() == reflect.Func && !concreteMatchesIface(methodSym.Type, mt) {
							methodSym = nil
						}
						if methodSym == nil {
							// Prefer the symbolic Sig: its Returns keep method-bearing
							// named types (e.g. an interface-returning method) that the
							// materialized mt may have erased, so chaining a call on the
							// result resolves. Fall back to mt, then bare interface{}.
							symType := &vm.Type{Rtype: vm.AnyRtype}
							switch {
							case sig != nil && sig.Kind() == reflect.Func:
								symType = sig
							case mt != nil && mt.Kind() == reflect.Func:
								symType = &vm.Type{Rtype: mt}
							}
							methodSym = &symbol.Symbol{Kind: symbol.Value, Type: symType}
						}
						push(methodSym)
						c.emit(t, vm.IfaceCall, c.methodID(methodName))
						break
					}
					if rm, embFieldPath, needAddr = findEmbeddedMethod(lookupTyp, lr, methodName, nil); rm.Type != nil {
						ok = true
					}
				}
				if ok {
					if len(embFieldPath) > 0 {
						c.emitField(t, embFieldPath)
					}
					// Build bound method signature (without receiver) so the
					// Call handler sees the correct parameter/return types.
					mt := rm.Type
					in := make([]reflect.Type, mt.NumIn()-1)
					for i := range in {
						in[i] = mt.In(i + 1)
					}
					out := make([]reflect.Type, mt.NumOut())
					for i := range out {
						out[i] = mt.Out(i)
					}
					boundType := reflect.FuncOf(in, out, mt.IsVariadic())
					push(&symbol.Symbol{Kind: symbol.Value, Type: &vm.Type{Rtype: boundType}})
					if needAddr {
						c.emit(t, vm.Addr)
					}
					// For named numeric types (e.g. time.Duration), the VM may lose
					// the named type during arithmetic.
					// Pass the receiver type index+1 in B so the VM can convert before method lookup.
					var recvTypeHint int
					embRtype := rtype
					if len(embFieldPath) > 0 {
						for _, idx := range embFieldPath {
							if embRtype.Kind() == reflect.Pointer {
								embRtype = embRtype.Elem()
							}
							embRtype = embRtype.Field(idx).Type
						}
					}
					if embRtype.Kind() >= reflect.Bool && embRtype.Kind() <= reflect.Float64 && embRtype.Name() != "" && embRtype.Name() != embRtype.Kind().String() {
						recvTypeHint = c.typeSym(s.Type).Index + 1
					}
					c.emit(t, vm.IfaceCall, c.methodID(methodName), recvTypeHint)
					break
				}
				return c.errUndef(t, t.Str[1:])
			}

		case lang.Next:
			showStack(stack)
			n := t.Arg[0].(int)
			i := c.resolveLabel(t, &fixList)
			lf := func(s *symbol.Symbol) int {
				if s.Kind == symbol.LocalVar {
					return vm.Local
				}
				return vm.Global
			}
			switch n {
			case 0:
				c.emit(t, vm.Next0, i)
			case 1:
				k := stack[len(stack)-2]
				if lf(k) == vm.Local {
					c.emit(t, vm.NextLocal, i, k.Index)
				} else {
					c.emit(t, vm.Next, i, k.Index)
				}
			case 2:
				v := stack[len(stack)-2]
				k := stack[len(stack)-3]
				// Pack kAddr (low 16) and vAddr (high 16) into one int.
				packed := k.Index | (v.Index << 16)
				if lf(k) == vm.Local {
					c.emit(t, vm.Next2Local, i, packed)
				} else {
					c.emit(t, vm.Next2, i, packed)
				}
			}

		case lang.Range:
			n := t.Arg[0].(int)
			topSym := top()
			vt := symbol.Vtype(topSym)
			var rangeKind reflect.Kind
			if vt != nil {
				rangeKind = vt.Kind()
			}
			// Go spec: range over an array iterates a copy;
			// range over a pointer-to-array or a slice uses the original.
			var copyArray int
			if rangeKind == reflect.Array {
				copyArray = 1
			}
			if rangeKind == reflect.Pointer {
				vt = vt.Elem()
				rangeKind = vt.Kind()
				c.emit(t, vm.Deref)
			}
			// Pull/Pull2 A operand: bit 0 = copy-array flag, bits 1+ = n; the VM
			// uses n to drop the n+1 dead slots (loop-var values + subject).
			pullA := copyArray | (n << 1)
			initRangeVar := func(s *symbol.Symbol, typ *vm.Type) {
				s.Type = typ
				if s.Kind == symbol.LocalVar {
					c.emit(t, vm.New, s.Index, c.typeSym(s.Type).Index)
				} else {
					c.Data[s.Index] = c.typeSlotValue(s.Index, s.Type, false)
				}
			}
			switch rangeKind {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
				if n > 1 {
					return c.errAt(t, "range over integer permits only one iteration variable")
				}
				if n > 0 {
					initRangeVar(stack[len(stack)-2], c.Symbols["int"].Type)
				}
				c.emit(t, vm.Pull, pullA)
			case reflect.Array, reflect.Slice, reflect.String:
				var vType *vm.Type
				if rangeKind == reflect.String {
					vType = c.Symbols["rune"].Type
				} else {
					vType = vt.Elem()
				}
				switch n {
				case 0:
					c.emit(t, vm.Pull, pullA)
				case 1:
					initRangeVar(stack[len(stack)-2], c.Symbols["int"].Type)
					c.emit(t, vm.Pull, pullA)
				case 2:
					k, v := stack[len(stack)-3], stack[len(stack)-2]
					initRangeVar(k, c.Symbols["int"].Type)
					initRangeVar(v, vType)
					c.emit(t, vm.Pull2, pullA)
				}
			case reflect.Map:
				keyType := vt.Key()
				switch n {
				case 0:
					c.emit(t, vm.Pull, pullA)
				case 1:
					initRangeVar(stack[len(stack)-2], keyType)
					c.emit(t, vm.Pull, pullA)
				case 2:
					k, v := stack[len(stack)-3], stack[len(stack)-2]
					initRangeVar(k, keyType)
					initRangeVar(v, vt.Elem())
					c.emit(t, vm.Pull2, pullA)
				}
			case reflect.Chan:
				if n > 1 {
					return c.errAt(t, "range over channel permits only one iteration variable")
				}
				switch n {
				case 0:
					c.emit(t, vm.Pull, pullA)
				case 1:
					initRangeVar(stack[len(stack)-2], vt.Elem())
					c.emit(t, vm.Pull, pullA)
				}
			case reflect.Func:
				// Range-over-func: subject must be func(yield func(V) bool)
				// or func(yield func(K, V) bool).
				if vt.NumIn() != 1 || vt.NumOut() != 0 {
					return c.errAt(t, "cannot range over %s (must be func(yield func(...) bool))", vt)
				}
				yieldType := vt.ParamType(0)
				if yieldType.Kind() != reflect.Func || yieldType.NumOut() != 1 ||
					yieldType.ReturnType(0).Kind() != reflect.Bool {
					return c.errAt(t, "cannot range over %s (yield must return bool)", vt)
				}
				yieldArity := yieldType.NumIn()
				if yieldArity < 1 || yieldArity > 2 {
					return c.errAt(t, "cannot range over %s (yield must take 1 or 2 args)", vt)
				}
				if n > yieldArity {
					return c.errAt(t, "range-over-func: too many iteration variables (%d) for yield arity %d", n, yieldArity)
				}
				// c.B encodes typeSym.Index+1 so the VM can wrap a mvm Closure
				// into a native Go func; 0 is reserved as "not a func range".
				funcTypeIdx := c.typeSym(vt).Index + 1
				op := vm.Pull
				switch n {
				case 2:
					k, v := stack[len(stack)-3], stack[len(stack)-2]
					initRangeVar(k, yieldType.ParamType(0))
					initRangeVar(v, yieldType.ParamType(1))
					op = vm.Pull2
				case 1:
					initRangeVar(stack[len(stack)-2], yieldType.ParamType(0))
				}
				c.emit(t, op, pullA, funcTypeIdx)
			default:
				// Unhandled range type. n == 0 degrades to a no-op iteration
				// (used by some upstream paths that emit a range over a value
				// of unresolved/degenerate type, e.g. an empty composite
				// literal). n > 0 is a Go spec violation -- emit a clean error
				// rather than miscompiling.
				if n > 0 {
					return c.errAt(t, "cannot range over %v", topSym.Type)
				}
				c.emit(t, vm.Pop, 1)
				c.emit(t, vm.Push, 0)
				c.emit(t, vm.Pull)
			}

		case lang.Stop:
			c.emit(t, vm.Stop, t.Arg[0].(int))

		case lang.Defer:
			if len(hasDefer) > 0 {
				hasDefer[len(hasDefer)-1] = true
			}
			narg := t.Arg[0].(int)
			spread := len(t.Arg) > 1 && t.Arg[1].(int) != 0
			s := stack[len(stack)-1-narg]
			markEscapingMethodDetach(narg)
			isX := 0
			switch s.Kind {
			case symbol.Type:
				return c.errAt(t, "cannot defer a type conversion")
			case symbol.Value:
				// A value of func type can be either a native Go func or a VM closure.
				// DeferPush detects native at runtime, same as for `go`.
			case symbol.Builtin:
				// Builtin functions have no VM-callable representation.
				// Push the opcode number as funcVal then use isX=2 so
				// DeferPush rotates it into position and Return dispatches
				// the opcode directly.
				op, ok := builtinDeferOp[s.Name]
				if !ok {
					return c.errAt(t, "cannot defer builtin %s", s.Name)
				}
				c.emit(t, vm.Push, int(op))
				isX = 2
			}
			callNarg, packed := c.emitVariadicPack(t, s, narg, spread, stack)
			pop() // function
			for range narg {
				pop()
			}
			deferB := isX
			if packed {
				deferB |= vm.DeferSpreadFlag
			}
			c.emit(t, vm.DeferPush, callNarg, deferB)

		case lang.Go:
			narg := t.Arg[0].(int)
			spread := len(t.Arg) > 1 && t.Arg[1].(int) != 0
			s := stack[len(stack)-1-narg]
			if s.Kind == symbol.Type {
				return c.errAt(t, "cannot use a type conversion as a goroutine")
			}
			markEscapingMethodDetach(narg)
			callNarg, packed := c.emitVariadicPack(t, s, narg, spread, stack)
			pop() // function
			for range narg {
				pop()
			}
			if s.Kind == symbol.Func && len(s.FreeVars) == 0 && c.removeGetGlobal(s.Index) {
				c.emit(t, vm.GoCallImm, s.Index, callNarg)
			} else {
				goB := 0
				if packed {
					goB = 1
				}
				c.emit(t, vm.GoCall, callNarg, goB)
			}

		case lang.ChanSend:
			vs := pop() // value
			ch := pop() // channel
			if ch.Type != nil {
				elemTyp := ch.Type.Elem()
				c.emitIfaceWrap(t, elemTyp, vs.Type)
			}
			c.emit(t, vm.ChanSend)

		case lang.Return:
			numOut := t.Arg[0].(int)
			var cells []int
			if len(retCellSlots) > 0 {
				cells = retCellSlots[len(retCellSlots)-1]
			}
			if err := checkTopN(t, numOut); err != nil {
				return err
			}
			// Wrap concrete return values in Iface when the function return type is an interface.
			// Skip if the stack doesn't have enough values (unreachable return after panic, etc.).
			if funcType, ok := t.Arg[1].(*vm.Type); ok && len(stack) >= numOut {
				for i := range numOut {
					stackSym := stack[len(stack)-numOut+i]
					// An untyped const adopts the declared return type
					// (e.g. `return 1` in a float64-returning func).
					c.emitConstConvert(t, stackSym, funcType.ReturnType(i), numOut-1-i)
					// A bare nil becomes a typed nil of the result type, so a
					// later Iface box of the returned value holds a valid ref.
					c.emitNilCoerce(t, stackSym, funcType.ReturnType(i), numOut-1-i)
					c.emitIfaceWrapAt(t, funcType.ReturnType(i), stackSym.Type, numOut-1-i)
				}
			}
			if len(cells) == 0 {
				// Fast path (unnamed or named-but-not-captured): the result
				// can't be modified by a defer, so the pushed values are the
				// result. Unchanged from the original return path.
				if len(hasDefer) == 0 || hasDefer[len(hasDefer)-1] || !c.fuseGetLocal(vm.GetLocalReturn, 0) {
					c.emit(t, vm.Return)
				}
				break
			}
			// Captured named returns: the result must be read from the cells
			// after defers (MarkNamedRet flagged the frame; Return finalizes
			// from slots). Store an explicit `return X...`'s values into the
			// cells first; a bare return's pushed idents are already the cell
			// values and are discarded by Return's stack reset.
			explicit := t.Arg[2].(bool)
			if explicit {
				// Named returns are registered right-to-left, so result slots
				// run 1..numOut with slot 1 = last result; popping top-down into
				// slots 1..numOut places each value correctly.
				for slot := 1; slot <= numOut; slot++ {
					if slices.Contains(cells, slot) {
						c.emit(t, vm.CellSet, slot)
					} else {
						c.emit(t, vm.SetLocal, slot, 0)
					}
				}
			} else if numOut > 0 {
				c.emit(t, vm.Pop, numOut)
			}
			if len(stack) >= numOut {
				truncStack(len(stack) - numOut)
			}
			c.emit(t, vm.Return)

		case lang.Slice:
			var coll *symbol.Symbol
			if t.Arg[0].(bool) { // 3-index slice a[low:high:max]
				coll = stack[len(stack)-4]
				c.emit(t, vm.Slice3)
				truncStack(len(stack) - 4)
			} else {
				coll = stack[len(stack)-3]
				c.emit(t, vm.Slice)
				truncStack(len(stack) - 3)
			}
			// Slicing a slice/string yields the same named type; only an
			// array or *array operand produces a fresh []T result. Use Vtype
			// so an untyped operand (e.g. a string const) resolves via its
			// Value instead of nil-dereferencing coll.Type.
			resType := symbol.Vtype(coll)
			if resType != nil {
				// Symbolic Kind/Elem so a named array operand (e.g. uuid.UUID,
				// [16]byte) isn't materialized just to detect the array case.
				at := resType
				if at.IsPtr() && at.Elem().Kind() == reflect.Array {
					at = at.Elem()
				}
				if at.Kind() == reflect.Array {
					resType = vm.SymSlice(at.Elem())
				}
			}
			push(&symbol.Symbol{Kind: symbol.Value, Type: resType})

		case lang.Select:
			descs := t.Arg[0].([]goparser.SelectCaseDesc)
			meta := &vm.SelectMeta{Cases: make([]vm.SelectCaseInfo, len(descs))}
			// initSlot initializes a variable slot and returns its index.
			initSlot := func(name string, typ *vm.Type) int {
				s := c.Symbols[name]
				s.Type = typ
				switch {
				case s.Kind == symbol.LocalVar:
					c.emit(t, vm.New, s.Index, c.typeSym(typ).Index)
				case s.Index == symbol.UnsetAddr:
					s.Index = len(c.Data)
					c.Data = append(c.Data, c.typeSlotValue(s.Index, typ, false))
				default:
					c.Data[s.Index] = c.typeSlotValue(s.Index, typ, false)
				}
				return s.Index
			}
			// Pop stack entries in reverse (LIFO) to collect channel element types.
			chanTypes := make([]*vm.Type, len(descs))
			for i, v := range slices.Backward(descs) {
				switch v.Dir {
				case reflect.SelectSend:
					pop() // value
					pop() // channel
				case reflect.SelectRecv:
					chanTypes[i] = pop().Type.Elem()
				}
			}
			for i, d := range descs {
				ci := vm.SelectCaseInfo{Dir: d.Dir, Slot: -1, OkSlot: -1}
				switch d.Dir {
				case reflect.SelectRecv:
					if d.ValName != "" {
						ci.Local = c.Symbols[d.ValName].Kind == symbol.LocalVar
						ci.Slot = initSlot(d.ValName, chanTypes[i])
					}
					if d.OkName != "" {
						ci.OkSlot = initSlot(d.OkName, c.Symbols["bool"].Type)
					}
				case reflect.SelectSend:
					meta.TotalPop += 2
				}
				if d.Dir == reflect.SelectRecv {
					meta.TotalPop++
				}
				meta.Cases[i] = ci
			}
			metaIdx := len(c.Data)
			c.Data = append(c.Data, vm.ValueOf(meta))
			push(&symbol.Symbol{Kind: symbol.Value, Type: c.Symbols["int"].Type})
			c.emit(t, vm.SelectExec, metaIdx, len(descs))

		default:
			return fmt.Errorf("generate: unsupported token %v", t)
		}
	}

	// Finally we fix unresolved labels for jump destinations.
	for _, t := range fixList {
		s, ok := c.labelSym(c.qualifyLabel(t.Str))
		if !ok {
			s, ok = c.labelSym(t.Str)
		}
		if !ok {
			return fmt.Errorf("label not found: %q", t.Str)
		}
		loc := t.Arg[0].(int)
		jumpOff := int(s.Value.Int()) - loc
		switch c.Code[loc].Op {
		case vm.GetLocalLowerIntImmJumpFalse, vm.GetLocalLowerIntImmJumpTrue:
			// Packed encoding: A = jumpOff_int16<<16 | localOff_uint16. The
			// localOff was already written at fuse time; OR the jumpOff into
			// the high 16 bits. Error out if a function body exceeds the int16
			// jumpOff bound (typical Go code stays well under).
			if jumpOff < -32768 || jumpOff > 32767 {
				return c.errAt(t, "jump offset %d overflows int16 in fused compare-and-branch", jumpOff)
			}
			c.Code[loc].A = (int32(jumpOff) << 16) | (c.Code[loc].A & 0xFFFF)
		default:
			c.Code[loc].A = int32(jumpOff)
		}
	}
	return err
}

// errIfMismatch rejects a numeric binary op on two differently-typed operands,
// as gc does. Gated to isVarKind operands so a typed var mixed with an untyped
// stdlib constant (f >= math.MaxUint64) and interface/nil/string compares pass.
func (c *Compiler) errIfMismatch(t goparser.Token, left, right *symbol.Symbol) error {
	if !isVarKind(left) || !isVarKind(right) {
		return nil
	}
	lt, rt := symbol.Vtype(left), symbol.Vtype(right)
	if !isNumericConvType(lt) || !isNumericConvType(rt) || lt.Identical(rt) {
		return nil
	}
	return c.errAt(t, "invalid operation: mismatched types %s and %s", lt, rt)
}

// isNilableType reports a type whose values can be nil (assignable from bare nil).
func isNilableType(t *vm.Type) bool {
	switch t.Kind() {
	case reflect.Interface, reflect.Slice, reflect.Map, reflect.Pointer,
		reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return true
	}
	return false
}

// errIfUnrepresentable rejects an untyped constant that cannot be represented
// in typ, as gc does: integer-range overflow, or a non-integer constant for an
// integer type ("constant 2.5 truncated to integer").
func (c *Compiler) errIfUnrepresentable(t goparser.Token, s *symbol.Symbol, typ *vm.Type) error {
	if s.Kind != symbol.Const || s.Cval == nil || typ == nil {
		return nil
	}
	if goparser.OverflowsType(s.Cval, typ) {
		return c.errOverflow(t, s.Cval, typ)
	}
	if k := typ.Kind(); k >= reflect.Int && k <= reflect.Uintptr && constant.ToInt(s.Cval).Kind() != constant.Int {
		return c.errAt(t, "constant %v truncated to integer", s.Cval)
	}
	return nil
}

func isVarKind(s *symbol.Symbol) bool {
	switch s.Kind {
	case symbol.Var, symbol.LocalVar:
		return true
	case symbol.Value:
		// Anonymous Value: a computed temporary (reliable type). A named Value
		// is a package binding, maybe an untyped const mvm models as typed.
		return s.Name == ""
	}
	return false
}

func arithmeticOpType(right, left *symbol.Symbol) *vm.Type {
	// Untyped constants take their type from the other operand (Go spec Operators).
	if right.Kind == symbol.Const && left.Kind != symbol.Const {
		return symbol.Vtype(left)
	}
	if left.Kind == symbol.Const && right.Kind != symbol.Const {
		return symbol.Vtype(right)
	}
	// Both constants (or both non-const): pick the wider numeric type.
	rt, lt := symbol.Vtype(right), symbol.Vtype(left)
	if rt != nil && lt != nil {
		if numericRank(lt) > numericRank(rt) {
			return lt
		}
	}
	return rt
}

func numericRank(typ *vm.Type) int {
	// Per Go spec, complex > float > int.
	switch typ.Kind() {
	case reflect.Complex64, reflect.Complex128:
		return 2
	case reflect.Float32, reflect.Float64:
		return 1
	}
	return 0
}

func constKind(right, left *symbol.Symbol) symbol.Kind {
	if right.Kind == symbol.Const && left.Kind == symbol.Const {
		return symbol.Const
	}
	return symbol.Value
}

func (c *Compiler) emitConstConvert(t goparser.Token, s *symbol.Symbol, typ *vm.Type, depth int) {
	if s.Kind != symbol.Const {
		return
	}
	c.emitNumConvert(t, typ, symbol.Vtype(s), depth)
}

// emitNilCoerce coerces an untyped nil argument (Type == nil) to a typed zero
// value of a concrete nilable parameter type (slice/map/ptr/chan/func), so that
// len/range/index inside the callee see a typed nil (e.g. a nil []int) rather
// than an invalid reflect.Value. Interface params keep the untyped nil (a nil
// interface), which already compares and dispatches correctly. The Convert
// opcode turns an invalid source into reflect.Zero(dstType) at runtime.
func (c *Compiler) emitNilCoerce(t goparser.Token, argSym *symbol.Symbol, paramTyp *vm.Type, depth int) {
	if argSym.Type != nil || paramTyp == nil {
		return
	}
	switch paramTyp.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Chan, reflect.Func:
		c.emit(t, vm.Convert, c.typeSym(paramTyp).Index, depth)
	}
}

func (c *Compiler) emitNumConvert(t goparser.Token, lhsType, rhsType *vm.Type, depth int) {
	if lhsType == nil || rhsType == nil || lhsType.Identical(rhsType) {
		return
	}
	if isNumericConvType(lhsType) && isNumericConvType(rhsType) {
		c.emit(t, vm.Convert, c.typeSym(lhsType).Index, depth)
	}
}

// convertOperand coerces a binary-op operand to typ: an untyped constant folds
// at compile time (foldConstLoad), anything else converts at runtime via Convert.
func (c *Compiler) convertOperand(t goparser.Token, s *symbol.Symbol, off int, typ *vm.Type, depth int) {
	st := symbol.Vtype(s)
	if typ == nil || st == nil || typ.Identical(st) || !isNumericConvType(typ) || !isNumericConvType(st) {
		return
	}
	if c.foldConstLoad(s, off, typ) {
		return
	}
	c.emit(t, vm.Convert, c.typeSym(typ).Index, depth)
}

// foldConstLoad converts a constant operand to typ at compile time, rewriting
// its single load instruction at off. Declines (false) for a non-constant, an
// unrecognized load, or a value overflowing a sized integer typ.
func (c *Compiler) foldConstLoad(s *symbol.Symbol, off int, typ *vm.Type) bool {
	if s.Kind != symbol.Const || s.Cval == nil || off < 0 || off >= len(c.Code) {
		return false
	}
	if op := c.Code[off].Op; op != vm.Push && op != vm.GetGlobal {
		return false
	}
	if isOverflowCheckedType(typ) && goparser.OverflowsType(s.Cval, typ) {
		return false
	}
	cv := goparser.ConstConvert(s.Cval, typ)
	val := vm.ValueOf(goparser.TypedConstValue(cv, typ))
	c.Code[off] = c.constLoadInstr(val, typ, c.Code[off].Pos)
	s.Cval, s.Value, s.Type = cv, val, typ
	return true
}

func booleanOpType(_, _ *symbol.Symbol) *vm.Type { return vm.TypeOf(true) }

func shiftLeftType(left *symbol.Symbol, intTyp *vm.Type) *vm.Type {
	vt := symbol.Vtype(left)
	if left.Kind == symbol.Const && vt != nil {
		if k := vt.Kind(); k == reflect.Float32 || k == reflect.Float64 {
			return intTyp
		}
	}
	return vt
}

func (c *Compiler) fuseGetLocal(op vm.Op, imm int) bool {
	if len(c.Code) == 0 || c.Code[len(c.Code)-1].Op != vm.GetLocal {
		return false
	}
	c.Code[len(c.Code)-1].Op = op
	c.Code[len(c.Code)-1].B = int32(imm)
	return true
}

// fuseLocalAssign collapses `x op= rhs` (and `x = x op rhs`) on a non-cell
// integer local into one of the in-place super-instructions. The trailing
// SetLocal+Pop are also subsumed, since the fused op leaves no stack value.
// Returns true when the fuse succeeded (caller skips emitting SetLocal+Pop).
func (c *Compiler) fuseLocalAssign(t goparser.Token, lhsIdx int) bool {
	n := len(c.Code)
	if n < 2 {
		return false
	}
	last := &c.Code[n-1]
	// Pattern A: GetLocal2 X X; GetLocal Y; (AddInt|SubInt)
	if (last.Op == vm.AddInt || last.Op == vm.SubInt) && n >= 3 {
		head := &c.Code[n-3]
		mid := &c.Code[n-2]
		if head.Op != vm.GetLocal2 || int(head.A) != lhsIdx || int(head.B) != lhsIdx {
			return false
		}
		if mid.Op != vm.GetLocal {
			return false
		}
		// A label pinned at n-3 (head's position) survives the fuse since the
		// new op occupies the same slot; labels at n-2 / n-1 would be lost.
		if c.labelAtPos[n-2] || c.labelAtPos[n-1] {
			return false
		}
		op := vm.AddLocalLocal
		if last.Op == vm.SubInt {
			op = vm.SubLocalLocal
		}
		y := int(mid.A)
		c.Code = c.Code[:n-3]
		c.emit(t, op, lhsIdx, y)
		return true
	}
	// Pattern B: GetLocal2 X X; (AddIntImm|SubIntImm) imm
	if last.Op == vm.AddIntImm || last.Op == vm.SubIntImm {
		head := &c.Code[n-2]
		if head.Op != vm.GetLocal2 || int(head.A) != lhsIdx || int(head.B) != lhsIdx {
			return false
		}
		// Label at n-2 (head's position) survives; one at n-1 would be lost.
		if c.labelAtPos[n-1] {
			return false
		}
		op := vm.AddLocalIntImm
		if last.Op == vm.SubIntImm {
			op = vm.SubLocalIntImm
		}
		imm := int(last.A)
		c.Code = c.Code[:n-2]
		c.emit(t, op, lhsIdx, imm)
		return true
	}
	return false
}

// immAddOverflowsInt32 reports whether imm+adj exceeds the int32 immediate field.
func immAddOverflowsInt32(imm, adj int32) bool {
	s := int64(imm) + int64(adj)
	return s < math.MinInt32 || s > math.MaxInt32
}

func (c *Compiler) fuseCmpJump(t goparser.Token, fixList *goparser.Tokens,
	cmpOp, fusedOp, getLocalCmpOp, getLocalFusedOp vm.Op, immAdj int32,
) bool {
	if len(c.Code) == 0 {
		return false
	}
	// A `&&`/`||` short-circuit merge label pinned at the current position
	// would expect a standalone JumpFalse here; fusing it into the prior cmp
	// op would route the merge jump into the if-body instead of the else arm.
	if c.labelAtPos[len(c.Code)] {
		return false
	}
	prev := &c.Code[len(c.Code)-1]
	var fused vm.Op
	var newA, newB int32
	switch prev.Op {
	case cmpOp:
		// Layout: A=jumpOff(int32), B=imm32.
		// Decline when imm+immAdj overflows int32 (imm == MaxInt32); the standalone path stays correct.
		if immAddOverflowsInt32(prev.A, immAdj) {
			return false
		}
		fused = fusedOp
		newB = prev.A + immAdj
		// newA = jumpOff (filled below).
	case getLocalCmpOp:
		// Layout: A=jumpOff_i16<<16|localOff_i16, B=imm32. localOff is the
		// signed slot offset relative to fp (params are negative, locals
		// positive); always fits int16 in practice. jumpOff fits int16 in
		// practice (loop bodies are tens of ops); the fixup loop checks at
		// the end and errors if a real function violates the bound.
		if prev.A < -32768 || prev.A > 32767 {
			return false // localOff doesn't fit int16
		}
		if immAddOverflowsInt32(prev.B, immAdj) {
			return false // imm+immAdj overflows int32
		}
		fused = getLocalFusedOp
		newA = prev.A & 0xFFFF // low 16: localOff (sign bit preserved by mask).
		newB = prev.B + immAdj // full int32 imm.
	default:
		return false
	}
	loc := len(c.Code) - 1
	if s, ok := c.Symbols[t.Str]; !ok {
		t.Arg = []any{loc} // fixup at the fused instruction's position
		*fixList = append(*fixList, t)
	} else if fused == getLocalFusedOp {
		jumpOff := int32(int(s.Value.Int()) - loc)
		if jumpOff < -32768 || jumpOff > 32767 {
			return false // backward jump too far for the packed encoding
		}
		newA |= jumpOff << 16
	} else {
		newA = int32(int(s.Value.Int()) - loc)
	}
	prev.Op = fused
	prev.A = newA
	prev.B = newB
	return true
}

func (c *Compiler) retractPush(s *symbol.Symbol) (int, bool) {
	if s.Kind != symbol.Const || len(c.Code) == 0 || c.Code[len(c.Code)-1].Op != vm.Push {
		return 0, false
	}
	n := int(c.Code[len(c.Code)-1].A)
	c.Code = c.Code[:len(c.Code)-1]
	return n, true
}

// litCval returns the arbitrary-precision constant for a literal token string,
// or nil if it is not a valid constant literal (so the operand simply will not
// fold). tok is the go/token kind (INT, FLOAT, CHAR, STRING).
func litCval(s string, tok token.Token) constant.Value {
	cv := constant.MakeFromLiteral(s, tok, 0)
	if cv.Kind() == constant.Unknown {
		return nil
	}
	return cv
}

// foldConstBinary folds `left op right` into a single constant when both
// operands are constants carrying a go/constant.Value. It returns ok=false when
// either operand has no Cval or when go/constant declines the operation (an
// invalid shift count, or division/remainder by zero), so the caller falls back
// to emitting the runtime op. The result type follows Go's rules: bool for
// comparisons, the left operand's type for shifts, and the wider operand type
// (float over int) for arithmetic and bitwise ops.
func (c *Compiler) foldConstBinary(op lang.Token, left, right *symbol.Symbol) (constant.Value, *vm.Type, bool) {
	if left.Kind != symbol.Const || right.Kind != symbol.Const || left.Cval == nil || right.Cval == nil {
		return nil, nil, false
	}
	cv, _, ok := goparser.FoldBinary(op, left.Cval, left.Type, right.Cval, right.Type)
	if !ok {
		return nil, nil, false
	}
	var typ *vm.Type
	switch {
	case op.IsBoolOp():
		typ = booleanOpType(left, right)
	case op == lang.Shl || op == lang.Shr:
		typ = shiftLeftType(left, c.Symbols["int"].Type)
	default:
		typ = arithmeticOpType(right, left)
	}
	return cv, typ, true
}

// emitFoldedConst materializes a folded constant of type typ into a single load
// instruction and returns the resulting Const symbol (the caller pushes it). An
// untyped integer result that no longer fits int64 is widened to uint64, and
// beyond uint64 to float64 (the only valid non-complex context for a value that
// wide), mirroring Go's untyped-constant rules and the former wide-shift fold.
func (c *Compiler) emitFoldedConst(t goparser.Token, cv constant.Value, typ *vm.Type) (*symbol.Symbol, error) {
	if typ == nil {
		typ = goparser.DefaultConstType(cv, c.Symbols)
	}
	// A folded result whose type is an explicitly-sized integer (never the type
	// of an untyped default) but whose value doesn't fit is a compile error, the
	// same as gc -- e.g. int8(100)+int8(100). The widening below is reserved for
	// untyped int/rune results (1<<63 etc.), so it is not reached for these.
	if isOverflowCheckedType(typ) && goparser.OverflowsType(cv, typ) {
		return nil, c.errOverflow(t, cv, typ)
	}
	// Widen an untyped integer result that overflows its (default int) type to
	// uint64, then float64 -- but only for an integer target. An explicit float
	// target (e.g. float64(1<<63)) must convert via ConstConvert below, not widen.
	if cv.Kind() == constant.Int && isIntegerKind(typ) && !isUint64Kind(typ) {
		if _, ok := constant.Int64Val(cv); !ok {
			if u, ok := constant.Uint64Val(cv); ok {
				typ = c.Symbols["uint64"].Type
				val := vm.ValueOf(u)
				c.emitConstLoad(t, val, typ)
				return &symbol.Symbol{Kind: symbol.Const, Value: val, Cval: cv, Type: typ}, nil
			}
			f, _ := constant.Float64Val(cv)
			cv = constant.MakeFloat64(f)
			typ = c.Symbols["float64"].Type
		}
	}
	cv = goparser.ConstConvert(cv, typ)
	val := vm.ValueOf(goparser.TypedConstValue(cv, typ))
	c.emitConstLoad(t, val, typ)
	return &symbol.Symbol{Kind: symbol.Const, Value: val, Cval: cv, Type: typ}, nil
}

// isOverflowCheckedType reports whether typ is an integer type that is never the
// default type of an untyped constant (so a folded value of this type must come
// from an explicitly-typed operand and can be range-checked). int (untyped int
// default) and int32 (untyped rune default) are intentionally excluded so untyped
// arithmetic like 1<<63 keeps widening instead of erroring.
func isOverflowCheckedType(typ *vm.Type) bool {
	if typ == nil {
		return false
	}
	switch typ.Kind() {
	case reflect.Int8, reflect.Int16, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}

// emitConstLoad emits a single instruction loading val (of type typ): an
// immediate Push for a signed integer that fits int32, otherwise a GetGlobal of
// a fresh data slot (floats, strings, wide and unsigned ints).
func (c *Compiler) emitConstLoad(t goparser.Token, val vm.Value, typ *vm.Type) {
	c.Code = append(c.Code, c.constLoadInstr(val, typ, vm.Pos(t.Pos)))
}

// constLoadInstr builds (without appending) the single load instruction for val
// of type typ -- see emitConstLoad.
func (c *Compiler) constLoadInstr(val vm.Value, typ *vm.Type, pos vm.Pos) vm.Instruction {
	if typ != nil {
		if k := typ.Kind(); k >= reflect.Int && k <= reflect.Int64 {
			if v := val.Int(); v >= -1<<31 && v < 1<<31 {
				return vm.Instruction{Op: vm.Push, A: int32(v), Pos: pos}
			}
		}
	}
	di := len(c.Data)
	c.Data = append(c.Data, val)
	return vm.Instruction{Op: vm.GetGlobal, A: int32(di), Pos: pos}
}

// emitConstImm emits a constant identifier as an immediate Push when it is a
// signed integer fitting int32, returning true. This lets a named const (e.g.
// `const N = ...` in `j <= N`) feed the immediate-fusion fast paths instead of
// loading via GetGlobal. Floats, strings, unsigned and wide ints keep the
// GetGlobal path (the caller falls through). The const still carries its Cval on
// the compile stack, so a fully-constant expression folds regardless.
func (c *Compiler) emitConstImm(t goparser.Token, s *symbol.Symbol) bool {
	if !s.Value.IsValid() {
		return false
	}
	// An untyped const carries Type==nil but a concrete Value; use the value's
	// kind in that case.
	k := s.Value.Kind()
	if s.Type != nil {
		k = s.Type.Kind()
	}
	if k >= reflect.Int && k <= reflect.Int64 && s.Value.CanInt() {
		if v := s.Value.Int(); v >= -1<<31 && v < 1<<31 {
			c.emit(t, vm.Push, int(v))
			return true
		}
	}
	return false
}

func isInt64Kind(typ *vm.Type) bool {
	if typ == nil {
		return false
	}
	k := typ.Kind()
	return k == reflect.Int || k == reflect.Int64
}

func isUint64Kind(typ *vm.Type) bool {
	if typ == nil {
		return false
	}
	k := typ.Kind()
	return k == reflect.Uint || k == reflect.Uint64
}

// isNumericConvType reports whether typ is a numeric type (including complex and
// named types like time.Duration), so a constant conversion to it can be folded
// or a runtime Convert emitted.
func isNumericConvType(typ *vm.Type) bool {
	if typ == nil {
		return false
	}
	k := typ.Kind()
	return k >= reflect.Int && k <= reflect.Complex128
}

// isIntegerKind reports whether typ is a signed or unsigned integer type.
func isIntegerKind(typ *vm.Type) bool {
	if typ == nil {
		return false
	}
	k := typ.Kind()
	return k >= reflect.Int && k <= reflect.Uintptr
}

func numericOp(base vm.Op, typ *vm.Type) vm.Op {
	if typ == nil {
		panic("numericOp: nil type")
	}
	k := typ.Kind()
	if int(k) >= len(vm.NumKindOffset) || vm.NumKindOffset[k] < 0 {
		panic(fmt.Sprintf("numericOp: non-numeric kind %v", k))
	}
	return base + vm.Op(vm.NumKindOffset[k])
}

func (c *Compiler) emitArithmeticOp(t goparser.Token, right *symbol.Symbol, typ *vm.Type, baseOp, immOp, fuseOp, strOp vm.Op) {
	if strOp != 0 && typ != nil && typ.Kind() == reflect.String {
		c.emit(t, strOp)
		return
	}
	if immOp != 0 && (isInt64Kind(typ) || isUint64Kind(typ)) {
		if n, ok := c.retractPush(right); ok {
			if fuseOp == 0 || !c.fuseGetLocal(fuseOp, n) {
				c.emit(t, immOp, n)
			}
			return
		}
	}
	c.emit(t, numericOp(baseOp, typ))
}

func (c *Compiler) emitComparisonOp(t goparser.Token, s2 *symbol.Symbol, typ *vm.Type, baseOp, intImm, uintImm, fuseInt, fuseUint, strOp vm.Op, negate bool) {
	if strOp != 0 && typ != nil && typ.Kind() == reflect.String {
		c.emit(t, strOp)
		if negate {
			c.emit(t, vm.Not)
		}
		return
	}
	var immOp, fuseOp vm.Op
	if isInt64Kind(typ) {
		immOp, fuseOp = intImm, fuseInt
	} else if isUint64Kind(typ) {
		immOp, fuseOp = uintImm, fuseUint
	}
	if immOp != 0 {
		if n, ok := c.retractPush(s2); ok {
			if fuseOp == 0 || !c.fuseGetLocal(fuseOp, n) {
				c.emit(t, immOp, n)
			}
			if negate {
				c.emit(t, vm.Not)
			}
			return
		}
	}
	c.emit(t, numericOp(baseOp, typ))
	if negate {
		c.emit(t, vm.Not)
	}
}

// labelSym returns the symbol at a label key only if it is a jump target
// (a label or a function entry); other kinds never are.
func (c *Compiler) labelSym(key string) (*symbol.Symbol, bool) {
	s, ok := c.Symbols[key]
	if !ok || (s.Kind != symbol.Label && s.Kind != symbol.Func) {
		return nil, false
	}
	return s, true
}

func (c *Compiler) resolveLabel(t goparser.Token, fixList *goparser.Tokens) int {
	if s, ok := c.labelSym(c.qualifyLabel(t.Str)); ok && c.labelResolvedThisPass(s) {
		return int(s.Value.Int()) - len(c.Code)
	}
	if s, ok := c.labelSym(t.Str); ok && c.labelResolvedThisPass(s) {
		return int(s.Value.Int()) - len(c.Code)
	}
	t.Arg = []any{len(c.Code)}
	*fixList = append(*fixList, t)
	return 0
}

// labelResolvedThisPass reports whether a jump target's address is usable now.
// A Label below genStart is stale (left in c.Symbols by a prior committed compile
// of the same unit, since the drop-retry loop reuses one Compiler); a forward
// jump to it must defer to fixList, else it keeps the prior pass's offset.
// Func entries are cross-unit targets and always resolve.
func (c *Compiler) labelResolvedThisPass(s *symbol.Symbol) bool {
	if s.Kind == symbol.Func {
		return true
	}
	return s.Value.IsValid() && int(s.Value.Int()) >= c.genStart
}

func (c *Compiler) qualifyLabel(name string) string {
	if c.CompilingPkg == "" {
		return name
	}
	return goparser.QualifyName(c.CompilingPkg, name)
}

func (c *Compiler) emitJump(t goparser.Token, fixList *goparser.Tokens, op vm.Op) {
	c.emit(t, op, c.resolveLabel(t, fixList))
}

// PrintCode pretty prints the generated code.
func (c *Compiler) PrintCode() {
	labels := map[int][]string{} // labels indexed by code location
	data := map[int]string{}     // data indexed by frame location

	for name, sym := range c.Symbols {
		if sym.Kind == symbol.Label || sym.Kind == symbol.Func {
			if !sym.Value.IsValid() {
				continue
			}
			i := int(sym.Value.Int())
			labels[i] = append(labels[i], name)
		}
		if sym.Used {
			data[sym.Index] = name
		}
	}

	fmt.Fprintln(os.Stderr, "# Code:")
	for i, l := range c.Code {
		for _, label := range labels[i] {
			fmt.Fprintln(os.Stderr, label+":")
		}
		extra := ""
		switch l.Op {
		case vm.Jump, vm.JumpFalse, vm.JumpTrue, vm.JumpSetFalse, vm.JumpSetTrue:
			if d, ok := labels[i+int(l.A)]; ok {
				extra = "// " + d[0]
			}
		case vm.Get, vm.GetLocal, vm.GetGlobal, vm.SetLocal, vm.SetGlobal, vm.CallImm, vm.CellGet, vm.CellSet:
			if d, ok := data[int(l.A)]; ok {
				extra = "// " + d
			}
		}
		fmt.Fprintf(os.Stderr, "%4d %v %v\n", i, l, extra)
	}

	for _, label := range labels[len(c.Code)] {
		fmt.Fprintln(os.Stderr, label+":")
	}
	fmt.Fprintln(os.Stderr, "# End code")
}

type entry struct {
	name string
	*symbol.Symbol
}

func (e entry) String() string { return fmt.Sprintf("name: %s, sym: %v", e.name, e.Symbol) }

// PrintData pretty prints the generated global data symbols in compiler.
func (c *Compiler) PrintData() {
	dict := c.symbolsByIndex()

	fmt.Fprintln(os.Stderr, "# Data:")
	for i, d := range c.Data {
		if d.IsValid() {
			fmt.Fprintf(os.Stderr, "%4d %T %v, Symbol: %v\n", i, d.Interface(), d.Reflect(), dict[i])
		} else {
			fmt.Fprintf(os.Stderr, "%4d %v %v\n", i, d.Reflect(), dict[i])
		}
	}
}

func (c *Compiler) symbolsByIndex() map[int]entry {
	dict := map[int]entry{}
	for name, sym := range c.Symbols {
		if sym.Index == symbol.UnsetAddr {
			continue
		}
		dict[sym.Index] = entry{name, sym}
	}
	return dict
}

// BuildDebugInfo constructs a DebugInfo from the compiler's symbol table
// and source registry. The result can be passed to DumpFrame/DumpCallStack.
func (c *Compiler) BuildDebugInfo() *vm.DebugInfo {
	di := vm.NewDebugInfo()
	di.Sources = c.Sources

	for name, sym := range c.Symbols {
		switch {
		case sym.Kind == symbol.Func:
			if !sym.Value.IsValid() {
				continue
			}
			addr := int(sym.Value.Int())
			// Prefer qualified names so diagnostic output can show
			// fully-qualified function names.
			// Among same-class candidates, keep the shortest.
			existing, ok := di.Labels[addr]
			isQual := strings.Contains(name, ".")
			existQual := strings.Contains(existing, ".")
			better := !ok ||
				(isQual && !existQual) ||
				(isQual == existQual && len(name) < len(existing))
			if better {
				di.Labels[addr] = name
			}

		case sym.Kind == symbol.LocalVar && sym.Used && sym.Index != symbol.UnsetAddr:
			// Extract function scope and short variable name from scoped name.
			// Scoped name format: "main/foo/for0/x" -> funcScope = closest Func ancestor.
			shortName := name
			if i := strings.LastIndex(name, "/"); i >= 0 {
				shortName = name[i+1:]
			}
			// Walk up the scope to find the enclosing function.
			funcName := enclosingFunc(name, c.Symbols)
			di.Locals[funcName] = append(di.Locals[funcName], vm.LocalVar{
				Offset: sym.Index,
				Name:   shortName,
			})
		}
	}
	for idx, e := range c.symbolsByIndex() {
		if e.Kind != symbol.LocalVar {
			di.Globals[idx] = e.name
		}
	}
	di.Funcs = c.FuncRanges
	return di
}

func enclosingFunc(scopedName string, syms symbol.SymMap) string {
	scope := scopedName
	for {
		i := strings.LastIndex(scope, "/")
		if i < 0 {
			return ""
		}
		scope = scope[:i]
		if s, ok := syms[scope]; ok && s.Kind == symbol.Func {
			return scope
		}
	}
}

func (c *Compiler) fixPtrFnewE(typ *vm.Type, index int) {
	if typ == nil || typ.Kind() != reflect.Pointer {
		return
	}
	// Bounded at genStart like the remove* helpers: the FnewE was emitted in
	// this generate() pass, so the scan must not reach a prior function's FnewE.
	for i := len(c.Code) - 1; i >= c.genStart; i-- {
		if c.Code[i].Op == vm.FnewE {
			di := int(c.Code[i].A)
			if di == index || c.slotMatchesType(di, typ) {
				c.Code[i].Op = vm.Fnew
				return
			}
		}
	}
}

// slotMatchesType reports whether Data slot di holds a zero value of typ,
// comparing *Type identity (via zeroSlotType) so it never reads a deferred
// slot's nil rtype; rtype equality is the fallback for eager/native slots.
func (c *Compiler) slotMatchesType(di int, typ *vm.Type) bool {
	if st := c.zeroSlotType[di]; st != nil {
		return st == typ || st.Identical(typ)
	}
	return c.Data[di].IsValid() && c.Data[di].Type() == c.rtype(typ)
}

// patchNilFnewLen fills a composite literal's nil slice/map Fnew (B=-1) with its length.
// It matches the literal's own type-symbol slot, else any same-type slot; an already-filled Fnew is skipped so nested literals patch their own.
// Bounded at genStart like the remove* helpers below.
func (c *Compiler) patchNilFnewLen(idx int32, typ *vm.Type, length int) {
	for i := len(c.Code) - 1; i >= c.genStart; i-- {
		in := c.Code[i]
		if in.Op != vm.Fnew || in.B != -1 {
			continue
		}
		if in.A == idx || (typ != nil && c.slotMatchesType(int(in.A), typ)) {
			c.Code[i].B = int32(length)
			return
		}
	}
}

// remove* helpers retract a load/Fnew that the current operand emitted.
// The backward scan stops at c.genStart, the start of this generate() pass, so it never reaches an already-compiled function's live load for the same slot.
// genStart is a safe over-approximation of the precise per-function start (funcStartStack top); it suffices only because every retraction finds its own load first.
func (c *Compiler) removeFnew(index int) {
	for i := len(c.Code) - 1; i >= c.genStart; i-- {
		op := c.Code[i].Op
		if (op == vm.Fnew || op == vm.FnewE) && int(c.Code[i].A) == index {
			c.Code[i] = vm.Instruction{Op: vm.Nop}
			return
		}
	}
}

func (c *Compiler) removeGetLocal(index int) {
	for i := len(c.Code) - 1; i >= c.genStart; i-- {
		op := c.Code[i].Op
		if (op == vm.GetLocal || op == vm.CellGet) && int(c.Code[i].A) == index {
			c.Code[i] = vm.Instruction{Op: vm.Nop}
			return
		}
		if op == vm.GetLocal2 && int(c.Code[i].A) == index {
			// Key is at A; value is at B. Unfuse: keep only GetLocal(B).
			c.Code[i].Op = vm.GetLocal
			c.Code[i].A = c.Code[i].B
			c.Code[i].B = 0
			return
		}
	}
}

func (c *Compiler) removeGetGlobal(index int) bool {
	for i := len(c.Code) - 1; i >= c.genStart; i-- {
		if c.Code[i].Op == vm.GetGlobal && int(c.Code[i].A) == index {
			// Skip if followed by Swap (method dispatch: GetGlobal + Swap + MkClosure).
			if i+1 < len(c.Code) && c.Code[i+1].Op == vm.Swap {
				return false
			}
			c.Code[i] = vm.Instruction{Op: vm.Nop}
			return true
		}
	}
	return false
}

func (c *Compiler) compileBuiltin(
	s *symbol.Symbol, narg int, t goparser.Token,
	stack *[]*symbol.Symbol, push func(*symbol.Symbol), pop func() *symbol.Symbol, _ func() *symbol.Symbol,
) (bool, error) {
	if s.Kind != symbol.Builtin && !strings.HasPrefix(s.Name, "unsafe.") {
		// Catch user functions shadowing a builtin.
		return false, nil
	}
	switch s.Name {
	case "trap":
		if narg != 0 {
			return true, errors.New("too many arguments to trap")
		}
		pop() // trap symbol
		c.emit(t, vm.Trap)
		return true, nil

	case "panic":
		if narg != 1 {
			return true, errors.New("too many arguments to panic")
		}
		pop() // argument
		pop() // panic symbol
		c.emit(t, vm.Panic)
		return true, nil

	case "recover":
		if narg != 0 {
			return true, errors.New("too many arguments to recover")
		}
		pop() // recover symbol
		push(&symbol.Symbol{Type: c.Symbols["any"].Type})
		c.emit(t, vm.Recover)
		return true, nil

	case "len", "cap":
		if narg != 1 {
			return true, fmt.Errorf("invalid argument count for %s", s.Name)
		}
		pop() // argument
		pop() // builtin symbol
		push(&symbol.Symbol{Type: c.Symbols["int"].Type})
		op := vm.Len
		if s.Name == "cap" {
			op = vm.Cap
		}
		c.emit(t, op, 0)
		c.emit(t, vm.Swap, 0, 1)
		c.emit(t, vm.Pop, 1)
		return true, nil

	case "append":
		if narg < 2 {
			return true, errors.New("missing arguments to append")
		}
		nvals := narg - 1 // number of values to append
		valSyms := make([]*symbol.Symbol, nvals)
		copy(valSyms, (*stack)[len(*stack)-nvals:])
		for range nvals {
			pop()
		}
		sliceSym := pop() // slice argument
		pop()             // append symbol
		push(sliceSym)    // result is same slice type
		elemTyp := sliceSym.Type.Elem()
		elemIdx := c.typeSym(elemTyp).Index
		isSpread := len(t.Arg) > 1 && t.Arg[1].(int) != 0
		// Wrap concrete values in Iface when appending to interface-typed slices.
		// Skipped in spread mode -- the lone value is the source slice, not an
		// element, so wrapping it would box the whole slice as an Iface{Typ:[]E}.
		if elemTyp.Kind() == reflect.Interface && !isSpread {
			for i, vs := range valSyms {
				if vs.Type == nil || vs.Type.IsInterface() {
					continue
				}
				c.emitIfaceWrapAt(t, elemTyp, vs.Type, nvals-1-i)
			}
		}
		if elemTyp.Kind() == reflect.Func && nvals > 1 {
			// Pre-wrap func values so AppendSlice can extract MvmFunc.GF without
			// calling wrapForFunc at runtime. Not needed for nvals==1; Append handles it.
			funcTypeIdx := c.typeIndex(elemTyp)
			for i := range nvals {
				c.emit(t, vm.WrapFunc, funcTypeIdx, nvals-1-i)
			}
		}
		switch {
		case isSpread:
			c.emit(t, vm.AppendSlice, 0, elemIdx) // 0 signals spread mode
		case nvals == 1:
			c.emit(t, vm.Append, 1, elemIdx)
		default:
			c.emit(t, vm.AppendSlice, nvals, elemIdx)
		}
		return true, nil

	case "copy":
		if narg != 2 {
			return true, errors.New("invalid argument count for copy")
		}
		pop() // src
		pop() // dst
		pop() // copy symbol
		push(&symbol.Symbol{Type: c.Symbols["int"].Type})
		c.emit(t, vm.CopySlice)
		return true, nil

	case "delete":
		if narg != 2 {
			return true, errors.New("invalid argument count for delete")
		}
		pop() // key
		pop() // map
		pop() // delete symbol
		c.emit(t, vm.DeleteMap)
		c.emit(t, vm.Pop, 1) // delete is void; discard stale map value
		return true, nil

	case "clear":
		if narg != 1 {
			return true, errors.New("invalid argument count for clear")
		}
		pop() // map or slice
		pop() // clear symbol
		c.emit(t, vm.Clear)
		return true, nil

	case "new":
		if narg != 1 {
			return true, errors.New("invalid argument count for new")
		}
		typeSym := (*stack)[len(*stack)-1]
		if typeSym.Kind != symbol.Type {
			return true, c.errAt(t, "first argument to new must be a type")
		}
		c.removeFnew(typeSym.Index)
		pop() // type arg
		pop() // new symbol
		push(&symbol.Symbol{Kind: symbol.Value, Type: vm.PointerTo(typeSym.Type)})
		c.emit(t, vm.PtrNew, typeSym.Index)
		return true, nil

	case "make":
		if narg < 1 || narg > 3 {
			return true, errors.New("invalid argument count for make")
		}
		typeSym := (*stack)[len(*stack)-narg]
		if typeSym.Kind != symbol.Type {
			return true, errors.New("first argument to make must be a type")
		}
		c.removeFnew(typeSym.Index)
		for range narg {
			pop()
		}
		pop() // make symbol
		push(&symbol.Symbol{Kind: symbol.Value, Type: typeSym.Type})
		switch typeSym.Type.Kind() {
		case reflect.Slice:
			// make([]T, len) or make([]T, len, cap).
			// Use the canonical mvm-level element type so a post-compile
			// synth-rtype attach (which upgrades the named element in place)
			// is observed via FillTypeSlots; a detached {Rtype: Elem()}
			// snapshot would freeze the pre-attach placeholder rtype and
			// MkSlice's reflect.SliceOf would diverge from the var slot type.
			elemIdx := c.typeSym(makeElemType(typeSym.Type)).Index
			c.emit(t, vm.MkSlice, -(narg - 1), elemIdx)
		case reflect.Map:
			// Slot for the full map type, not a rebuilt MapOf(key, val): keeps a
			// named map type's identity so make(T) stays assignable to a T field/elem.
			mapIdx := c.typeSym(typeSym.Type).Index
			if narg >= 2 {
				// make(map, size): negate the index to flag a size hint on the stack.
				mapIdx = -(mapIdx + 1)
			}
			c.emit(t, vm.MkMap, mapIdx, 0)
		case reflect.Chan:
			elemIdx := c.typeSym(typeSym.Type.ElemType).Index
			if narg == 2 {
				// make(chan T, bufSize): buffer size is already on stack
				c.emit(t, vm.MkChan, elemIdx, -1)
			} else {
				// make(chan T): unbuffered
				c.emit(t, vm.MkChan, elemIdx, 0)
			}
		default:
			return true, fmt.Errorf("cannot make type %s", typeSym.Type)
		}
		return true, nil

	case "close":
		if narg != 1 {
			return true, errors.New("invalid argument count for close")
		}
		pop() // channel
		pop() // close symbol
		c.emit(t, vm.ChanClose)
		return true, nil

	case "print", "println":
		for range narg {
			pop()
		}
		pop() // builtin symbol
		op := vm.Print
		if s.Name == "println" {
			op = vm.Println
		}
		c.emit(t, op, narg)
		return true, nil

	case "complex":
		switch {
		case narg < 2:
			return true, fmt.Errorf("invalid operation: not enough arguments for %s (expected 2, found %d)", s.Name, narg)
		case narg > 2:
			return true, fmt.Errorf("invalid operation: too many arguments for %s (expected 2, found %d)", s.Name, narg)
		}
		deref := func(sym *symbol.Symbol) (reflect.Kind, bool) {
			if sym.IsConst() {
				k := sym.Type.Kind()
				if reflect.Int <= k && k <= reflect.Float64 {
					return reflect.Float64, true
				}
			}
			if sym.Type != nil {
				return sym.Type.Kind(), false
			}
			return sym.Value.Type().Kind(), false
		}
		imagKind, iconst := deref(pop()) // imag part
		realKind, rconst := deref(pop()) // real part
		pop()                            // complex symbol

		var kind reflect.Kind
		switch {
		case iconst:
			kind = realKind
		case rconst:
			kind = imagKind
		case imagKind == realKind:
			kind = realKind
		default:
			return true, fmt.Errorf("invalid operation: mismatched types %s and %s", realKind, imagKind)
		}

		switch kind {
		case reflect.Float32:
			kind = reflect.Complex64
		case reflect.Float64:
			kind = reflect.Complex128
		default:
			return true, fmt.Errorf("invalid argument: type %s, expected floating-point", kind)
		}

		push(&symbol.Symbol{Type: c.Symbols[kind.String()].Type})
		c.emit(t, vm.Complex, int(kind))
		return true, nil

	case "real", "imag":
		switch {
		case narg < 1:
			return true, fmt.Errorf("not enough arguments for %s", s.Name)
		case narg > 1:
			return true, fmt.Errorf("too many arguments for %s", s.Name)
		}
		argSym := (*stack)[len(*stack)-narg]
		pop() // operand
		pop() // real/imag symbol
		op := vm.Real
		if s.Name == "imag" {
			op = vm.Imag
		}
		kind := argSym.Type.Kind()
		switch kind {
		case reflect.Complex64:
			kind = reflect.Float32
		case reflect.Complex128:
			kind = reflect.Float64
		default:
			return true, fmt.Errorf("invalid argument for %s (%s)", s.Name, kind)
		}
		push(&symbol.Symbol{Type: c.Symbols[kind.String()].Type})
		c.emit(t, op, int(kind))
		return true, nil

	case "min", "max":
		if narg < 1 {
			return true, fmt.Errorf("not enough arguments for %s", s.Name)
		}
		// An untyped const arg has Type nil; the result type is the first
		// concretely typed arg (min(nbmax, n) with const nbmax yields n's type),
		// or the default const type when every arg is an untyped const.
		argSym := (*stack)[len(*stack)-narg]
		for i := 1; i < narg && argSym.Type == nil; i++ {
			argSym = (*stack)[len(*stack)-narg+i]
		}
		typ := argSym.Type
		if typ == nil && argSym.Cval != nil {
			typ = goparser.DefaultConstType(argSym.Cval, c.Symbols)
		}
		for range narg {
			pop()
		}
		pop() // min/max symbol
		if narg > 1 && typ == nil {
			return true, c.errAt(t, "internal: %s argument has no type", s.Name)
		}
		push(&symbol.Symbol{Type: typ})
		if narg == 1 {
			return true, nil // single arg: value already on stack
		}
		op := vm.Min
		if s.Name == "max" {
			op = vm.Max
		}
		c.emit(t, op, narg, int(typ.Kind()))
		return true, nil

	case "unsafe.Sizeof", "unsafe.Alignof":
		if narg != 1 {
			return true, fmt.Errorf("invalid argument count for %s", s.Name)
		}
		argSym := (*stack)[len(*stack)-1]
		if argSym.Type == nil || argSym.Type.Kind() == reflect.Invalid {
			return true, fmt.Errorf("%s: argument has no type", s.Name)
		}
		var val uintptr
		if s.Name == "unsafe.Sizeof" {
			val = argSym.Type.Size()
		} else {
			val = uintptr(argSym.Type.Align())
		}
		pop() // argument
		pop() // fn symbol
		push(&symbol.Symbol{Kind: symbol.Const, Value: vm.ValueOf(val), Type: c.Symbols["uintptr"].Type})
		// Remove the GetGlobal that loaded the stub function reference.
		c.removeGetGlobal(s.Index)
		// The argument was evaluated onto the runtime stack; discard it and
		// push the computed uintptr constant in its place.
		c.emit(t, vm.Pop, 1)
		di := len(c.Data)
		c.Data = append(c.Data, vm.ValueOf(val))
		c.emit(t, vm.GetGlobal, di)
		return true, nil

	case "unsafe.Offsetof":
		if narg != 1 {
			return true, fmt.Errorf("invalid argument count for %s", s.Name)
		}
		argSym := (*stack)[len(*stack)-1]
		if !argSym.HasFieldOffset {
			return true, errors.New("unsafe.Offsetof: argument must be a struct field selector")
		}
		val := argSym.FieldOffset
		pop() // argument
		pop() // fn symbol
		push(&symbol.Symbol{Kind: symbol.Const, Value: vm.ValueOf(val), Type: c.Symbols["uintptr"].Type})
		c.removeGetGlobal(s.Index)
		c.emit(t, vm.Pop, 1)
		di := len(c.Data)
		c.Data = append(c.Data, vm.ValueOf(val))
		c.emit(t, vm.GetGlobal, di)
		return true, nil

	case "unsafe.Slice", "unsafe.SliceData":
		// The stubs in stdlib/unsafe.go return `any` (their result type depends
		// on the pointer/slice arg and can't be expressed in a Go signature),
		// so after the call the value is an interface wrapper. Unwrap it with
		// a TypeAssert to the statically-known result type.
		var resultType *vm.Type
		if s.Name == "unsafe.Slice" {
			if narg != 2 {
				return true, fmt.Errorf("invalid argument count for %s", s.Name)
			}
			ptrSym := (*stack)[len(*stack)-2]
			if ptrSym.Type == nil || ptrSym.Type.Kind() != reflect.Pointer {
				return true, errors.New("unsafe.Slice: first argument must be a pointer")
			}
			resultType = vm.SymSlice(ptrSym.Type.Elem())
		} else {
			if narg != 1 {
				return true, fmt.Errorf("invalid argument count for %s", s.Name)
			}
			argSym := (*stack)[len(*stack)-1]
			if argSym.Type == nil || argSym.Type.Kind() != reflect.Slice {
				return true, errors.New("unsafe.SliceData: argument must be a slice")
			}
			resultType = vm.SymPtr(argSym.Type.Elem())
		}
		for range narg + 1 {
			pop()
		}
		push(&symbol.Symbol{Kind: symbol.Value, Type: resultType})
		c.emit(t, vm.Call, narg, 1)
		c.emit(t, vm.TypeAssert, c.typeIndex(resultType), 0)
		return true, nil
	}

	return false, nil
}

// zeroTypeSlot returns the Data slot holding a zero VALUE of typ (what Fnew
// copies to instantiate).
// Shared across emit sites so a name-keyed type ident, a carried-type ident,
// and a composite all patch the same Fnew.
// Keyed on *vm.Type pointer identity; convergence between the type-ident
// emitter (compiler.go:1926) and the composite-literal length patcher
// (compiler.go:1515) relies on canonical derived *vm.Type from vm.SymSlice
// / vm.SymPtr / vm.SymMap / etc. returning the same instance per shape.
// Distinct from typeSym, which allocates a type-DESCRIPTOR slot (make-elem/key,
// TypeAssert, etc.).
func (c *Compiler) zeroTypeSlot(typ *vm.Type) int {
	if i, ok := c.zeroTypeIdxs[typ]; ok {
		return i
	}
	i := len(c.Data)
	c.Data = append(c.Data, c.typeSlotValue(i, typ, false))
	c.zeroTypeIdxs[typ] = i
	c.zeroSlotType[i] = typ
	return i
}

// makeElemType returns the canonical mvm-level element type of a container
// type, falling back to a fresh wrapper around the reflect element when the
// container was built natively without an mvm-level ElemType link.
func makeElemType(container *vm.Type) *vm.Type {
	if container.ElemType != nil {
		return container.ElemType
	}
	return &vm.Type{Rtype: vm.MaterializeRtype(container).Elem()}
}

// emitVariadicPack MkSlice-packs a deferred or go-spawned variadic call's
// trailing args (as the Call path does), returning the new arg count and whether
// the last arg is now a slice. Only VM funcs are packed; native-direct funcs and
// builtins keep raw spread args, and a spread call already supplies the slice.
func (c *Compiler) emitVariadicPack(t goparser.Token, s *symbol.Symbol, narg int, spread bool, stack []*symbol.Symbol) (int, bool) {
	typ := s.Type
	if typ == nil || !typ.IsVariadic() {
		return narg, false
	}
	if spread {
		return narg, true
	}
	switch s.Kind {
	case symbol.Func, symbol.LocalVar, symbol.Var:
	default:
		return narg, false
	}
	nFixed := typ.NumIn() - 1
	elemTyp := typ.ParamType(nFixed).Elem()
	if elemTyp.Kind() == reflect.Interface {
		for k := nFixed; k < narg; k++ {
			argSym := stack[len(stack)-narg+k]
			if argSym.Type == nil || argSym.Type.IsInterface() {
				continue
			}
			c.emitIfaceWrapAt(t, elemTyp, argSym.Type, narg-1-k)
		}
	}
	c.emit(t, vm.MkSlice, narg-nFixed, c.typeSym(elemTyp).Index)
	return nFixed + 1, true
}

func (c *Compiler) typeSym(t *vm.Type) *symbol.Symbol {
	tsym, ok := c.typeSyms[t]
	if !ok {
		tsym = &symbol.Symbol{Index: symbol.UnsetAddr, Kind: symbol.Type, Type: t}
		c.typeSyms[t] = tsym
	}
	if tsym.Index == symbol.UnsetAddr {
		tsym.Index = len(c.Data)
		c.Data = append(c.Data, c.typeSlotValue(tsym.Index, t, true))
	}
	return tsym
}

// MaterializeAll builds the rtype of every *Type reachable from the compiler's
// symbol table and type dedup maps (recursing into fields/elem/key/params/
// returns/embedded/base). After the flip goparser leaves composite/named-struct
// rtypes nil; this fills them with layout rtypes (reserving a method-bearing synth
// identity for named method types) before run, so the VM never dereferences a nil
// Rtype and the synth attach can fill methods into each identity in place.
func (c *Compiler) MaterializeAll() {
	seen := map[*vm.Type]bool{}
	var visit func(t *vm.Type)
	visit = func(t *vm.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		visit(t.ElemType)
		visit(t.KeyType)
		visit(t.Base)
		for _, f := range t.Fields {
			visit(f)
		}
		for _, p := range t.Params {
			visit(p)
		}
		for _, r := range t.Returns {
			visit(r)
		}
		for _, e := range t.Embedded {
			visit(e.Type)
		}
		for i := range t.Methods {
			visit(t.Methods[i].Sig)
		}
		vm.MaterializeRtype(t)
		// Method.Rtype is the materialize-time projection of the symbolic Sig set
		// at registration (comp builds method signatures symbolically; see the
		// method-registration sites). Fill it once here, before synth attach reads it.
		for i := range t.Methods {
			vm.MaterializeMethod(&t.Methods[i])
		}
	}
	for t := range c.zeroTypeIdxs {
		visit(t)
	}
	for t := range c.typeSyms {
		visit(t)
	}
	for t := range c.typeIdxs {
		visit(t)
	}
	for _, sym := range c.Symbols {
		visit(sym.Type)
	}
	// Patch structs whose layout was deferred because a by-value field was an
	// in-flight placeholder mid-cycle (mutual struct cycle broken by a pointer).
	vm.FinalizeDeferred()
}

// intrinsicInfo describes a VM intrinsic that replaces a native function call.
type intrinsicInfo struct {
	op   vm.Op
	narg int
}

// intrinsicOp maps "pkgPath.funcName" to a VM opcode and its arity.
var intrinsicOp = map[string]intrinsicInfo{
	// math: float64 unary.
	"math.Abs":         {vm.AbsFloat64, 1},
	"math.Sqrt":        {vm.SqrtFloat64, 1},
	"math.Ceil":        {vm.CeilFloat64, 1},
	"math.Floor":       {vm.FloorFloat64, 1},
	"math.Trunc":       {vm.TruncFloat64, 1},
	"math.RoundToEven": {vm.NearestFloat64, 1},
	// math: float64 binary.
	"math.Min":      {vm.MinFloat64, 2},
	"math.Max":      {vm.MaxFloat64, 2},
	"math.Copysign": {vm.CopysignFloat64, 2},
	// math/bits: leading/trailing zeros.
	"math/bits.LeadingZeros":    {vm.Clz64, 1},
	"math/bits.LeadingZeros32":  {vm.Clz32, 1},
	"math/bits.LeadingZeros64":  {vm.Clz64, 1},
	"math/bits.TrailingZeros":   {vm.Ctz64, 1},
	"math/bits.TrailingZeros32": {vm.Ctz32, 1},
	"math/bits.TrailingZeros64": {vm.Ctz64, 1},
	// math/bits: population count.
	"math/bits.OnesCount":   {vm.Popcnt64, 1},
	"math/bits.OnesCount32": {vm.Popcnt32, 1},
	"math/bits.OnesCount64": {vm.Popcnt64, 1},
	// math/bits: rotate.
	"math/bits.RotateLeft":   {vm.Rotl64, 2},
	"math/bits.RotateLeft32": {vm.Rotl32, 2},
	"math/bits.RotateLeft64": {vm.Rotl64, 2},
}

// compileIntrinsic replaces known native function calls with direct VM opcodes,
// avoiding the overhead of reflection-based calls.
func (c *Compiler) compileIntrinsic(
	s *symbol.Symbol, narg int, t goparser.Token,
	push func(*symbol.Symbol), pop func() *symbol.Symbol,
	stack []*symbol.Symbol,
) (bool, error) {
	if s.Kind != symbol.Value {
		return false, nil
	}
	info, ok := intrinsicOp[s.Name]
	if !ok {
		return false, nil
	}
	if narg != info.narg {
		return false, nil
	}
	// Remove the GetGlobal that loaded the function value onto the stack.
	if !c.removeGetGlobal(s.Index) {
		return false, nil
	}
	// Emit numeric conversions for arguments whose type doesn't match the
	// native function parameter type (e.g. int arg passed to float64 param).
	rv := s.Value.Reflect()
	if rv.IsValid() && rv.Type().Kind() == reflect.Func {
		funcType := rv.Type()
		for k := range narg {
			argSym := stack[len(stack)-narg+k]
			paramType := funcType.In(k)
			if argSym.Type == nil || (argSym.Type.Rtype != nil && argSym.Type.Rtype == paramType) {
				continue
			}
			c.emitNumConvert(t, &vm.Type{Rtype: paramType}, argSym.Type, narg-1-k)
		}
	}
	// Pop function symbol and argument symbols, push return type.
	for range narg {
		pop()
	}
	pop() // function symbol
	// Determine the return type from the native function's reflect type.
	if rv.IsValid() && rv.Type().Kind() == reflect.Func && rv.Type().NumOut() > 0 {
		push(&symbol.Symbol{Kind: symbol.Value, Type: &vm.Type{Rtype: rv.Type().Out(0)}})
	}
	c.emit(t, info.op)
	return true, nil
}
