// Package comp implements a byte code generator targeting the vm.
package comp

import (
	"errors"
	"fmt"
	"go/constant"
	"go/token"
	"os"
	"path"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

const debug = false

var builtinDeferOp = map[string]vm.Op{
	"print":   vm.Print,
	"println": vm.Println,
	"close":   vm.ChanClose,
	"delete":  vm.DeleteMap,
	"copy":    vm.CopySlice,
	"clear":   vm.Clear,
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
	labelAtPos   map[int]bool                // code positions occupied by Labels; consulted by fuseCmpJump
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
		labelAtPos:   map[int]bool{},
	}
}

// looksLikePkgPath reports whether name resembles a Go import path: contains
// a slash and isn't a `.go` file (the two name shapes Compile is called with).
func looksLikePkgPath(name string) bool {
	return strings.ContainsRune(name, '/') && !strings.HasSuffix(name, ".go")
}

func ifaceMethodSig(ifaceTyp *vm.Type, methodName string) reflect.Type {
	if rm, ok := ifaceTyp.Rtype.MethodByName(methodName); ok {
		return rm.Type
	}
	for _, im := range ifaceTyp.IfaceMethods {
		if im.Name == methodName && im.Rtype != nil {
			return im.Rtype
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
	if ifaceSig != nil {
		return &symbol.Symbol{Kind: symbol.Value, Type: &vm.Type{Rtype: ifaceSig}}
	}
	return nil
}

func concreteMatchesIface(concrete *vm.Type, ifaceSig reflect.Type) bool {
	if ifaceSig == nil || concrete == nil || concrete.Rtype == nil || concrete.Kind() != reflect.Func {
		return false
	}
	rt := concrete.Rtype
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
	}
}

// Compile parses src and generates code and data, or returns a non-nil error.
// Code and data are added incrementally in c.Code and C.Data.
func (c *Compiler) Compile(name, src string) error {
	// Directory-mode load with a package-path target (e.g. `mvm test
	// golang.org/x/text/language`): mirror importSrc's importingPkg setup so
	// the target's own top-level symbols use canonical pkg-qualified keys
	// rather than bare keys -- otherwise lookups from the target's deferred
	// bodies miss and surface as ErrUndefined. Scoped to ParseAll so Phase 2
	// (compileDeferred) sees the same empty importingPkg it does for any
	// other transitive import, with CompilingPkg driving the lookups.
	var remaining []goparser.DeferredDecl
	var err error
	if src == "" && looksLikePkgPath(name) {
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
	return c.finishCompile(remaining)
}

// CompileFiles compiles several in-memory source files as a single main-package
// unit (Phase 1 across all files, then Phase 2 code-gen), so top-level symbols
// declared in one file are visible to the others regardless of file or
// declaration order. Backs `mvm run f1.go f2.go ...`.
func (c *Compiler) CompileFiles(sources []goparser.PackageSource) error {
	remaining, err := c.ParseAllFiles(sources)
	if err != nil {
		return err
	}
	return c.finishCompile(remaining)
}

// finishCompile runs Phase 2 over the deferred declarations: allocate global
// slots, compile var initializers first (so all var types resolve) then func
// bodies and expression statements, then propagate embedded methods. Shared by
// Compile and CompileFiles.
func (c *Compiler) finishCompile(remaining []goparser.DeferredDecl) error {
	c.allocGlobalSlots()
	var rest []goparser.DeferredDecl
	for _, decl := range remaining {
		if len(decl.Toks) > 0 && decl.Toks[0].Tok == lang.Var {
			if err := c.compileDeferred(decl); err != nil {
				return err
			}
		} else {
			rest = append(rest, decl)
		}
	}
	for _, decl := range rest {
		if err := c.compileDeferred(decl); err != nil {
			return err
		}
	}
	c.propagateEmbeddedMethods()
	return nil
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
				t.Methods[id] = vm.Method{Index: m.Index, Path: newPath, PtrRecv: m.PtrRecv, EmbedIface: m.EmbedIface, Rtype: m.Rtype}
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
					t.Methods[id] = vm.Method{Index: -1, Path: []int{emb.FieldIdx}, EmbedIface: true, Rtype: im.Rtype}
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
			if s.Type != nil {
				// Re-allocate via vm.NewValue at the current rtype: an earlier
				// vm.NewValue call (e.g. addSymVar at parse time) may have used
				// a struct-placeholder rtype whose Size has since grown via
				// SetFields, leaving s.Value's backing memory too small to
				// hold the finalized struct. Reads past the original size hit
				// adjacent memory (the language.Und Tag pExt-garbage bug).
				v = vm.NewValue(s.Type.Rtype)
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

func (c *Compiler) typeIndex(typ *vm.Type) int {
	if i, ok := c.typeIdxs[typ]; ok {
		return i
	}
	i := len(c.Data)
	c.Data = append(c.Data, vm.ValueOf(typ))
	c.typeIdxs[typ] = i
	return i
}

func (c *Compiler) findTypeSym(rtype reflect.Type) *vm.Type {
	for _, sym := range c.Symbols {
		if sym.Kind == symbol.Type && sym.Type != nil && sym.Type.Rtype == rtype {
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

func findEmbeddedIfaceMethod(typ *vm.Type, name string) ([]int, reflect.Type) {
	for _, emb := range typ.Embedded {
		if emb.Type == nil {
			continue
		}
		if emb.Type.IsInterface() {
			emb.Type.EnsureIfaceMethods()
			for _, im := range emb.Type.IfaceMethods {
				if im.Name == name {
					return []int{emb.FieldIdx}, im.Rtype
				}
			}
		}
		if p, mt := findEmbeddedIfaceMethod(emb.Type, name); p != nil {
			return append([]int{emb.FieldIdx}, p...), mt
		}
	}
	return nil, nil
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
		} else if t := c.findTypeSym(typ.Rtype.Elem()); t != nil {
			lookupTyp = t
		}
	} else if typ.Name == "" {
		if t := c.findTypeSym(typ.Rtype); t != nil {
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
		m, fieldPath := c.Symbols.MethodByName(s, im.Name)
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
					typ.Methods[id] = vm.Method{Index: -1, Path: []int{emb.FieldIdx}, EmbedIface: true, Rtype: embIM.Rtype}
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
		var mrtype reflect.Type
		if m.Type != nil && m.Type.Rtype != nil && m.Type.Kind() == reflect.Func {
			mrtype = m.Type.Rtype
		}
		typ.Methods[id] = vm.Method{Index: m.Index, Path: mpath, Rtype: mrtype}
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
	return goparser.ErrConstOverflow{Value: cv.String(), Type: typ.Rtype.String(), Loc: c.Sources.FormatPos(t.Pos), Pos: t.Pos}
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
func paramNeedsDetach(fnType reflect.Type) bool {
	if fnType == nil || fnType.Kind() != reflect.Func {
		return true
	}
	for i, n := 0, fnType.NumIn(); i < n; i++ {
		switch fnType.In(i).Kind() {
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
		case reflect.Slice:
			c.emit(t, vm.Fnew, index, 0)
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
		for j := 0; j < n; j++ {
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
			right, left := pop(), pop()
			if h, err := foldBinaryConst(t, left, right, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			typ := arithmeticOpType(right, left)
			c.emitConstConvert(t, right, typ, 0)
			c.emitConstConvert(t, left, typ, 1)
			push(&symbol.Symbol{Kind: constKind(right, left), Type: typ})
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
			// markAddressed records that the local-frame slot has had its
			// address taken in this function; future GetLocals on it emit
			// GetLocalSync (see lang.Ident handling below).
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
			switch {
			case n > 0 && c.Code[n-1].Op == vm.Index:
				c.Code[n-1].Op = vm.IndexAddr
			case concrete && n > 0 && c.Code[n-1].Op == vm.GetLocal:
				c.Code[n-1].Op = vm.AddrLocal
				markAddressed(int(c.Code[n-1].A))
			case concrete && n > 0 && c.Code[n-1].Op == vm.GetLocal2:
				idx := int(c.Code[n-1].B)
				c.Code[n-1].Op = vm.GetLocal
				c.Code[n-1].B = 0
				c.emit(t, vm.AddrLocal, idx)
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
			s2, s1 := pop(), pop()
			if h, err := foldBinaryConst(t, s1, s2, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			typ := arithmeticOpType(s2, s1)
			c.emitNumConvert(t, typ, s2.Type, 0)
			c.emitNumConvert(t, typ, s1.Type, 1)
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
			s2, s1 := pop(), pop()
			if h, err := foldBinaryConst(t, s1, s2, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			typ := arithmeticOpType(s2, s1)
			c.emitNumConvert(t, typ, s2.Type, 0)
			c.emitNumConvert(t, typ, s1.Type, 1)
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
			// past int64 to uint64 (e.g. 1<<63) or float64 (e.g. 1<<120) -- the
			// only valid non-complex contexts -- which a runtime int64 shift would
			// silently overflow.
			if h, err := foldBinaryConst(t, left, shift, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			leftTyp := shiftLeftType(left, c.Symbols["int"].Type)
			c.emitConstConvert(t, left, leftTyp, 1)
			push(&symbol.Symbol{Kind: constKind(left, shift), Type: leftTyp})
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
				namedMethodful := s.Type.Base != nil || s.Type.Rtype.NumMethod() > 0
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
				nret := typ.Rtype.NumOut()
				for i := 0; i < nret; i++ {
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
				// Wrap concrete args in Iface when the parameter expects an interface type.
				// Use mvm-level Params types (which carry IfaceMethods) when available.
				nIn := typ.Rtype.NumIn()
				nFixed := nIn
				if typ.Rtype.IsVariadic() {
					nFixed = nIn - 1
				}
				for k := 0; k < narg && k < nIn; k++ {
					argSym := stack[len(stack)-narg+k]
					if argSym.Type == nil {
						if k < nFixed || (spread && k == nFixed) {
							c.emitNilCoerce(t, argSym, typ.Rtype.In(k), narg-1-k)
						}
						continue
					}
					if argSym.Type.IsInterface() {
						continue
					}
					var ifaceTyp *vm.Type
					if k < len(typ.Params) {
						ifaceTyp = typ.Params[k]
					} else {
						ifaceTyp = &vm.Type{Rtype: typ.Rtype.In(k)}
					}
					depth := narg - 1 - k
					c.emitIfaceWrapAt(t, ifaceTyp, argSym.Type, depth)
					if !ifaceTyp.IsInterface() {
						c.emitConstConvert(t, argSym, ifaceTyp, depth)
					}
				}
				// Type switches on variadic slice elements require Iface wrapping at the call site.
				// For spread calls (f(s...)), the slice is pre-built; skip per-element wrapping.
				if typ.Rtype.IsVariadic() && !spread {
					nFixed := typ.Rtype.NumIn() - 1
					elemType := typ.Rtype.In(nFixed).Elem()
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
				// Pop function and input arg symbols, push return value symbols.
				pop()
				for i := 0; i < narg; i++ {
					pop()
				}
				nret := typ.Rtype.NumOut()
				for i := 0; i < nret; i++ {
					push(&symbol.Symbol{Kind: symbol.Value, Type: typ.ReturnType(i)})
				}
				callNarg := narg
				if typ.Rtype.IsVariadic() {
					nFixed := typ.Rtype.NumIn() - 1
					if !spread {
						// Pack trailing arguments into a slice for the variadic parameter.
						nExtra := narg - nFixed
						elemType := typ.Rtype.In(nFixed).Elem()
						elemIdx := c.typeSym(&vm.Type{Rtype: elemType}).Index
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
					if !paramNeedsDetach(typ.Rtype) {
						op = vm.CallImmFast
					}
					c.emit(t, op, s.Index, callNarg<<16|nret)
				} else {
					callNret := nret
					if typ.Rtype.IsVariadic() && !spread {
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
				rtyp = s.Type.Rtype
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
							c.emitNilCoerce(t, argSym, rtyp.In(k), narg-1-k)
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
			callNret := nret
			if spread && rtyp != nil && rtyp.IsVariadic() {
				callNret |= int(vm.CallSpreadFlag)
			}
			c.emit(t, vm.Call, narg, callNret)

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
				ts = &symbol.Symbol{Kind: symbol.Value, Type: &vm.Type{Rtype: ts.Type.Rtype.Elem()}}
			}
			// `key: value` in a map composite literal.
			// The key may be any kind of expression, so this must come before the ks.Kind switch.
			if ts.Type != nil && ts.Type.Kind() == reflect.Map {
				elemTyp := ts.Type.Elem()
				c.emitNumConvert(t, ts.Type.Key(), ks.Type, 1)
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
			if sliceLen > 0 {
				// Patch the matching Fnew by the type's canonical zero-value slot
				// (the same slot the type ident emitted), shared by rtype whether
				// that ident carried its type or was resolved by name.
				sym := c.Symbols[t.Str]
				var idx int32
				if sym != nil && sym.Type != nil {
					idx = int32(c.zeroTypeSlot(sym.Type))
				} else if sym != nil {
					idx = int32(sym.Index)
				}
				// Skip Fnews already claimed by a nested composite of the
				// same type (B != 0 marks the patched length); without this,
				// `[]E{x, &T{Errors: []E{y}}}` re-patches the inner []E's
				// Fnew and leaves the outer one at length 0.
				for i := len(c.Code) - 1; i >= 0; i-- {
					if c.Code[i].Op == vm.Fnew && c.Code[i].A == idx && c.Code[i].B == 0 {
						c.Code[i].B = int32(sliceLen)
						break
					}
				}
			}
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
			// Allocate a heap cell for each captured named return so a
			// capturing (deferred) closure shares the slot. Runs after Grow
			// zeroes the slots and before zero-init/body; CellSlot was already
			// set on the symbols in goparser so body refs use the cell.
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

		case lang.Define:
			showStack(stack)
			n := t.Arg[0].(int)
			if err := checkTopN(t, n); err != nil {
				return err
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
						if !r.Value.Reflect().IsValid() {
							return c.errUndef(t, lhs[i].Name)
						}
						typ = vm.TypeOf(r.Value.Interface())
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
					if lhs[i].NeedsCell() {
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
					c.Data[lhs[i].Index] = vm.NewValue(lhs[i].Type.Rtype)
				} else {
					lhs[i].Type = typ
					c.Data[lhs[i].Index] = vm.NewValue(typ.Rtype)
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
				// Captured variable write inside closure body: use HeapSet.
				if idx := freeVarIndex(lhs.Name); idx >= 0 {
					c.emit(t, vm.HeapSet, idx)
					c.emit(t, vm.Pop, 1) // pop stale value pushed by HeapGet in Ident
					break
				}
				// Param slots alias the caller's pushed Value, so SetLocal would write through dst.ref to the caller.
				// New detaches the slot, but the Used optimization skips it on later compile-emitted assigns;
				// re-emit per branch since runtime may take a branch the optimizer didn't pick first.
				if !lhs.Used || lhs.IsParam() {
					if !lhs.NeedsCell() {
						typeIdx := c.typeSym(lhs.Type).Index
						c.fixPtrFnewE(lhs.Type, typeIdx)
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
				if v := c.Data[lhs.Index]; !v.IsValid() && rhs.Type != nil {
					c.Data[lhs.Index] = vm.NewValue(rhs.Type.Rtype)
					if sym := c.Symbols[lhs.Name]; sym != nil {
						sym.Type = rhs.Type
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
				c.emit(t, vm.SetS, n)
			}

		case lang.DerefAssign:
			if err := checkTopN(t, 2); err != nil { // check rhs and pointer target
				return err
			}
			pop() // rhs
			pop() // lhs (pointer, not yet dereferenced)
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
			switch kind {
			case reflect.Array, reflect.Slice:
				c.emit(t, vm.IndexSet)
			case reflect.Map:
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
			s2, s1 := pop(), pop()
			if h, err := foldBinaryConst(t, s1, s2, leftStart); err != nil {
				return err
			} else if h {
				break
			}
			typ := arithmeticOpType(s2, s1)
			c.emitNumConvert(t, typ, s2.Type, 0)
			c.emitNumConvert(t, typ, s1.Type, 1)
			push(&symbol.Symbol{Type: booleanOpType(s2, s1)})
			c.emit(t, vm.Equal)

		case lang.EqualSet:
			if err := checkTopN(t, 2); err != nil {
				return err
			}
			push(&symbol.Symbol{Type: booleanOpType(pop(), pop())})
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
				if _, ok := c.Symbols[qk]; ok {
					labelKey = qk
				}
			}
			if s, ok := c.Symbols[labelKey]; ok {
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
							var mrtype reflect.Type
							if s.Type != nil && s.Type.Rtype != nil && s.Type.Kind() == reflect.Func {
								mrtype = s.Type.Rtype
							}
							ts.Type.Methods[id] = vm.Method{Index: s.Index, PtrRecv: strings.HasPrefix(parts[0], "*"), Rtype: mrtype}
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
				if strings.HasSuffix(t.Str, "_end") {
					base := strings.TrimSuffix(t.Str, "_end")
					endKey := base
					if qk := c.qualifyLabel(base); qk != base {
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
			pop()
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
				if m, fieldPath := c.Symbols.MethodByName(s, t.Str[1:]); m != nil {
					// Method expression: Type.Method yields a func with receiver as first arg.
					// A composite literal (T{}.Method) is a value, not a method expression.
					if s.Kind == symbol.Type && !s.Composite {
						if !s.NoFnew {
							c.removeFnew(s.Index)
						}
						push(&symbol.Symbol{
							Kind:       symbol.Func,
							Name:       m.Name,
							Index:      m.Index,
							Type:       m.Type,
							MethodExpr: true,
						})
						break
					}
					push(m)
					// Extract embedded receiver if method is promoted through embedded fields.
					if len(fieldPath) > 0 {
						c.emitField(t, fieldPath)
					}
					// Determine if auto-deref or auto-addr is needed.
					methodWantsPtr := strings.HasPrefix(m.Name, "*")
					recvRtype := s.Type.Rtype
					if len(fieldPath) > 0 {
						for _, idx := range fieldPath {
							if recvRtype.Kind() == reflect.Pointer {
								recvRtype = recvRtype.Elem()
							}
							recvRtype = recvRtype.Field(idx).Type
						}
					}
					recvIsPtr := recvRtype.Kind() == reflect.Pointer
					switch {
					case methodWantsPtr && !recvIsPtr:
						c.emit(t, vm.Addr)
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
					if mfunc, ok := s.Type.Rtype.MethodByName(mname); ok {
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
				typ := s.Type.Rtype
				isPtr := typ.Kind() == reflect.Pointer
				if isPtr {
					typ = typ.Elem()
				}
				if typ.Kind() == reflect.Struct {
					// Look up struct type in symbol table to get mvm-level Fields/Params info.
					structType := c.findTypeSym(typ)
					if structType == nil {
						if isPtr {
							structType = s.Type.Elem()
						} else {
							structType = s.Type
						}
					}
					fieldName := t.Str[1:]
					fieldPath, ft := structType.FieldLookup(fieldName)
					if fieldPath == nil {
						// reflect-side fallback: covers cases where the receiver's
						// Rtype carries the layout but the mvm-level Type lacks the
						// matching Fields slot (e.g. types accessed only through
						// reflect.StructOf).
						if f, ok := typ.FieldByName(fieldName); ok {
							fieldPath = f.Index
							ft = structType.FieldType(fieldName)
						}
					}
					if fieldPath != nil {
						push(&symbol.Symbol{
							Kind:           symbol.Var,
							Index:          symbol.UnsetAddr,
							Type:           ft,
							HasFieldOffset: true,
							FieldOffset:    fieldPathOffset(typ, fieldPath),
						})
						c.emitField(t, fieldPath)
						break
					}
				}
				// Native method on concrete reflect type: use IfaceCall for
				// reflect-based dispatch at runtime.
				methodName := t.Str[1:]
				rtype := s.Type.Rtype
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
					// Without this, types obtained through field access (FieldType) may lack
					// Embedded metadata, preventing findEmbeddedMethod from finding promoted methods.
					if ft := c.findTypeSym(lr); ft != nil {
						lookupTyp = ft
					}
					if fieldPath, mt := findEmbeddedIfaceMethod(lookupTyp, methodName); fieldPath != nil {
						c.emitField(t, fieldPath)
						// mt is the embedded interface's bound method type (no
						// receiver). Validate findConcreteFuncSym against it so
						// an unrelated `.M` Func can't hijack the dispatch.
						methodSym := c.findConcreteFuncSym(methodName)
						if methodSym != nil && mt != nil && mt.Kind() == reflect.Func && !concreteMatchesIface(methodSym.Type, mt) {
							methodSym = nil
						}
						if methodSym == nil {
							symType := &vm.Type{Rtype: vm.AnyRtype}
							if mt != nil && mt.Kind() == reflect.Func {
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
			initRangeVar := func(s *symbol.Symbol, typ *vm.Type) {
				s.Type = typ
				if s.Kind == symbol.LocalVar {
					c.emit(t, vm.New, s.Index, c.typeSym(s.Type).Index)
				} else {
					c.Data[s.Index] = vm.NewValue(s.Type.Rtype)
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
				c.emit(t, vm.Pull)
			case reflect.Array, reflect.Slice, reflect.String:
				var vType *vm.Type
				if rangeKind == reflect.String {
					vType = c.Symbols["rune"].Type
				} else {
					vType = vt.Elem()
				}
				switch n {
				case 0:
					c.emit(t, vm.Pull, copyArray)
				case 1:
					initRangeVar(stack[len(stack)-2], c.Symbols["int"].Type)
					c.emit(t, vm.Pull, copyArray)
				case 2:
					k, v := stack[len(stack)-3], stack[len(stack)-2]
					initRangeVar(k, c.Symbols["int"].Type)
					initRangeVar(v, vType)
					c.emit(t, vm.Pull2, copyArray)
				}
			case reflect.Map:
				keyType := vt.Key()
				switch n {
				case 0:
					c.emit(t, vm.Pull)
				case 1:
					initRangeVar(stack[len(stack)-2], keyType)
					c.emit(t, vm.Pull)
				case 2:
					k, v := stack[len(stack)-3], stack[len(stack)-2]
					initRangeVar(k, keyType)
					initRangeVar(v, vt.Elem())
					c.emit(t, vm.Pull2)
				}
			case reflect.Chan:
				if n > 1 {
					return c.errAt(t, "range over channel permits only one iteration variable")
				}
				switch n {
				case 0:
					c.emit(t, vm.Pull)
				case 1:
					initRangeVar(stack[len(stack)-2], vt.Elem())
					c.emit(t, vm.Pull)
				}
			case reflect.Func:
				// Range-over-func: subject must be func(yield func(V) bool)
				// or func(yield func(K, V) bool).
				ft := vt.Rtype
				if ft.NumIn() != 1 || ft.NumOut() != 0 {
					return c.errAt(t, "cannot range over %s (must be func(yield func(...) bool))", ft)
				}
				yieldType := ft.In(0)
				if yieldType.Kind() != reflect.Func || yieldType.NumOut() != 1 ||
					yieldType.Out(0).Kind() != reflect.Bool {
					return c.errAt(t, "cannot range over %s (yield must return bool)", ft)
				}
				yieldArity := yieldType.NumIn()
				if yieldArity < 1 || yieldArity > 2 {
					return c.errAt(t, "cannot range over %s (yield must take 1 or 2 args)", ft)
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
					initRangeVar(k, &vm.Type{Rtype: yieldType.In(0)})
					initRangeVar(v, &vm.Type{Rtype: yieldType.In(1)})
					op = vm.Pull2
				case 1:
					initRangeVar(stack[len(stack)-2], &vm.Type{Rtype: yieldType.In(0)})
				}
				c.emit(t, op, 0, funcTypeIdx)
			default:
				// Unhandled range type. n == 0 degrades to a no-op iteration
				// (used by some upstream paths that emit a range over a value
				// of unresolved/degenerate type, e.g. an empty composite
				// literal). n > 0 is a Go spec violation -- emit a clean error
				// rather than miscompiling.
				if n > 0 {
					return c.errAt(t, "cannot range over %v", topSym.Type.Rtype)
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
			s := stack[len(stack)-1-narg]
			isX := 0
			switch s.Kind {
			case symbol.Type:
				return c.errAt(t, "cannot defer a type conversion")
			case symbol.Value:
				isX = 1
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
			pop() // function
			for i := 0; i < narg; i++ {
				pop()
			}
			c.emit(t, vm.DeferPush, narg, isX)

		case lang.Go:
			narg := t.Arg[0].(int)
			s := stack[len(stack)-1-narg]
			if s.Kind == symbol.Type {
				return c.errAt(t, "cannot use a type conversion as a goroutine")
			}
			pop() // function
			for i := 0; i < narg; i++ {
				pop()
			}
			if s.Kind == symbol.Func && len(s.FreeVars) == 0 && c.removeGetGlobal(s.Index) {
				c.emit(t, vm.GoCallImm, s.Index, narg)
			} else {
				c.emit(t, vm.GoCall, narg)
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
				for i := 0; i < numOut; i++ {
					stackSym := stack[len(stack)-numOut+i]
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
				rtype := resType.Rtype
				if rtype.Kind() == reflect.Pointer && rtype.Elem().Kind() == reflect.Array {
					rtype = rtype.Elem()
				}
				if rtype.Kind() == reflect.Array {
					resType = &vm.Type{Rtype: reflect.SliceOf(rtype.Elem())}
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
					c.Data = append(c.Data, vm.NewValue(typ.Rtype))
				default:
					c.Data[s.Index] = vm.NewValue(typ.Rtype)
				}
				return s.Index
			}
			// Pop stack entries in reverse (LIFO) to collect channel element types.
			chanTypes := make([]*vm.Type, len(descs))
			for i := len(descs) - 1; i >= 0; i-- {
				switch descs[i].Dir {
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
		s, ok := c.Symbols[c.qualifyLabel(t.Str)]
		if !ok {
			s, ok = c.Symbols[t.Str]
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
func (c *Compiler) emitNilCoerce(t goparser.Token, argSym *symbol.Symbol, paramRtype reflect.Type, depth int) {
	if argSym.Type != nil || paramRtype == nil {
		return
	}
	switch paramRtype.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Chan, reflect.Func:
		c.emit(t, vm.Convert, c.typeSym(&vm.Type{Rtype: paramRtype}).Index, depth)
	}
}

func (c *Compiler) emitNumConvert(t goparser.Token, lhsType, rhsType *vm.Type, depth int) {
	if lhsType == nil || rhsType == nil || lhsType.Rtype == rhsType.Rtype {
		return
	}
	if isNumericConvType(lhsType) && isNumericConvType(rhsType) {
		c.emit(t, vm.Convert, c.typeSym(lhsType).Index, depth)
	}
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
	if typ != nil {
		if k := typ.Kind(); k >= reflect.Int && k <= reflect.Int64 {
			if v := val.Int(); v >= -1<<31 && v < 1<<31 {
				c.emit(t, vm.Push, int(v))
				return
			}
		}
	}
	di := len(c.Data)
	c.Data = append(c.Data, val)
	c.emit(t, vm.GetGlobal, di)
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

// isNumericConvType reports whether typ is a non-complex numeric type (including
// named types like time.Duration), so a constant conversion to it can be folded.
func isNumericConvType(typ *vm.Type) bool {
	if typ == nil {
		return false
	}
	k := typ.Kind()
	return k >= reflect.Int && k <= reflect.Float64
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

func (c *Compiler) resolveLabel(t goparser.Token, fixList *goparser.Tokens) int {
	if s, ok := c.Symbols[c.qualifyLabel(t.Str)]; ok {
		return int(s.Value.Int()) - len(c.Code)
	}
	if s, ok := c.Symbols[t.Str]; ok {
		return int(s.Value.Int()) - len(c.Code)
	}
	t.Arg = []any{len(c.Code)}
	*fixList = append(*fixList, t)
	return 0
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
	for i := len(c.Code) - 1; i >= 0; i-- {
		if c.Code[i].Op == vm.FnewE {
			di := int(c.Code[i].A)
			if di == index || c.Data[di].Type() == typ.Rtype {
				c.Code[i].Op = vm.Fnew
				return
			}
		}
	}
}

func (c *Compiler) removeFnew(index int) {
	for i := len(c.Code) - 1; i >= 0; i-- {
		op := c.Code[i].Op
		if (op == vm.Fnew || op == vm.FnewE) && int(c.Code[i].A) == index {
			c.Code[i] = vm.Instruction{Op: vm.Nop}
			return
		}
	}
}

func (c *Compiler) removeGetLocal(index int) {
	for i := len(c.Code) - 1; i >= 0; i-- {
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
	for i := len(c.Code) - 1; i >= 0; i-- {
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
		elemType := sliceSym.Type.Rtype.Elem()
		elemIdx := c.typeSym(&vm.Type{Rtype: elemType}).Index
		isSpread := len(t.Arg) > 1 && t.Arg[1].(int) != 0
		// Wrap concrete values in Iface when appending to interface-typed slices.
		// Skipped in spread mode -- the lone value is the source slice, not an
		// element, so wrapping it would box the whole slice as an Iface{Typ:[]E}.
		if elemType.Kind() == reflect.Interface && !isSpread {
			elemTyp := &vm.Type{Rtype: elemType}
			for i, vs := range valSyms {
				if vs.Type == nil || vs.Type.IsInterface() {
					continue
				}
				c.emitIfaceWrapAt(t, elemTyp, vs.Type, nvals-1-i)
			}
		}
		if elemType.Kind() == reflect.Func && nvals > 1 {
			// Pre-wrap func values so AppendSlice can extract MvmFunc.GF without
			// calling wrapForFunc at runtime. Not needed for nvals==1; Append handles it.
			funcTypeIdx := c.typeIndex(&vm.Type{Rtype: elemType})
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
			return true, errors.New("first argument to new must be a type")
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
			// is observed via RefreshSynthRtype; a detached {Rtype: Elem()}
			// snapshot would freeze the pre-attach placeholder rtype and
			// MkSlice's reflect.SliceOf would diverge from the var slot type.
			elemIdx := c.typeSym(makeElemType(typeSym.Type)).Index
			c.emit(t, vm.MkSlice, -(narg - 1), elemIdx)
		case reflect.Map:
			// Canonical key type: the cascade rebuilds t-as-key map rtypes, so
			// the var slot becomes map[synthKey]V; RefreshSynthRtype keeps this
			// in step. The value stays a detached snapshot: t-as-element maps are
			// not rebuilt, so the slot keeps its placeholder value.
			keyIdx := c.typeSym(makeKeyType(typeSym.Type)).Index
			valType := typeSym.Type.Rtype.Elem()
			valIdx := c.typeSym(&vm.Type{Rtype: valType}).Index
			c.emit(t, vm.MkMap, keyIdx, valIdx)
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
			return true, fmt.Errorf("cannot make type %s", typeSym.Type.Rtype)
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
		argSym := (*stack)[len(*stack)-narg]
		for range narg {
			pop()
		}
		pop() // min/max symbol
		push(&symbol.Symbol{Type: argSym.Type})
		if narg == 1 {
			return true, nil // single arg: value already on stack
		}
		op := vm.Min
		if s.Name == "max" {
			op = vm.Max
		}
		c.emit(t, op, narg, int(argSym.Type.Kind()))
		return true, nil

	case "unsafe.Sizeof", "unsafe.Alignof":
		if narg != 1 {
			return true, fmt.Errorf("invalid argument count for %s", s.Name)
		}
		argSym := (*stack)[len(*stack)-1]
		if argSym.Type == nil || argSym.Type.Rtype == nil {
			return true, fmt.Errorf("%s: argument has no type", s.Name)
		}
		var val uintptr
		if s.Name == "unsafe.Sizeof" {
			val = argSym.Type.Rtype.Size()
		} else {
			val = uintptr(argSym.Type.Rtype.Align())
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
			if ptrSym.Type == nil || ptrSym.Type.Rtype == nil || ptrSym.Type.Kind() != reflect.Pointer {
				return true, errors.New("unsafe.Slice: first argument must be a pointer")
			}
			resultType = &vm.Type{Rtype: reflect.SliceOf(ptrSym.Type.Rtype.Elem())}
		} else {
			if narg != 1 {
				return true, fmt.Errorf("invalid argument count for %s", s.Name)
			}
			argSym := (*stack)[len(*stack)-1]
			if argSym.Type == nil || argSym.Type.Rtype == nil || argSym.Type.Kind() != reflect.Slice {
				return true, errors.New("unsafe.SliceData: argument must be a slice")
			}
			resultType = &vm.Type{Rtype: reflect.PointerTo(argSym.Type.Rtype.Elem())}
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
// (compiler.go:1515) relies on canonical derived *vm.Type from vm.SliceOf
// / vm.PointerTo / vm.MapOf / etc. returning the same instance per shape.
// Distinct from typeSym, which allocates a type-DESCRIPTOR slot (make-elem/key,
// TypeAssert, etc.).
func (c *Compiler) zeroTypeSlot(typ *vm.Type) int {
	if i, ok := c.zeroTypeIdxs[typ]; ok {
		return i
	}
	i := len(c.Data)
	c.Data = append(c.Data, vm.NewValue(typ.Rtype))
	c.zeroTypeIdxs[typ] = i
	return i
}

// makeElemType returns the canonical mvm-level element type of a container
// type, falling back to a fresh wrapper around the reflect element when the
// container was built natively without an mvm-level ElemType link.
func makeElemType(container *vm.Type) *vm.Type {
	if container.ElemType != nil {
		return container.ElemType
	}
	return &vm.Type{Rtype: container.Rtype.Elem()}
}

// makeKeyType returns the canonical mvm-level key type of a map type, falling
// back to a fresh wrapper around the reflect key when the map was built
// natively without an mvm-level KeyType link.
func makeKeyType(container *vm.Type) *vm.Type {
	if container.KeyType != nil {
		return container.KeyType
	}
	return &vm.Type{Rtype: container.Rtype.Key()}
}

func (c *Compiler) typeSym(t *vm.Type) *symbol.Symbol {
	tsym, ok := c.typeSyms[t]
	if !ok {
		tsym = &symbol.Symbol{Index: symbol.UnsetAddr, Kind: symbol.Type, Type: t}
		c.typeSyms[t] = tsym
	}
	if tsym.Index == symbol.UnsetAddr {
		tsym.Index = len(c.Data)
		c.Data = append(c.Data, vm.TypeValue(t.Rtype))
	}
	return tsym
}

// RefreshSynthRtype re-emits c.Data slots whose stored rtype no longer matches
// the *vm.Type's current Rtype.
// Called after vm.AttachSynthMethods swaps a type's Rtype (and the in-Type
// cascade has propagated through the derived chain) so Fnew sources, type
// descriptors, and var slots all observe the post-attach rtype.
// Slots already in sync are left untouched; var values keep their numeric
// payload across the rtype rebuild via vm.FromReflect (the synth swap
// preserves layout, so the underlying storage stays valid against the new
// rtype).
func (c *Compiler) RefreshSynthRtype() {
	for t, idx := range c.zeroTypeIdxs {
		rt := liveSynthRtype(t)
		if !c.Data[idx].IsValid() || c.Data[idx].Type() == rt {
			continue
		}
		c.Data[idx] = vm.NewValue(rt)
	}
	for _, sym := range c.typeSyms {
		if sym.Index == symbol.UnsetAddr || sym.Type == nil {
			continue
		}
		rt := liveSynthRtype(sym.Type)
		if !c.Data[sym.Index].IsValid() || c.Data[sym.Index].Type() == rt {
			continue
		}
		c.Data[sym.Index] = vm.TypeValue(rt)
	}
	for _, sym := range c.Symbols {
		if sym.Kind != symbol.Var || sym.Index == symbol.UnsetAddr || sym.Type == nil {
			continue
		}
		rt := liveSynthRtype(sym.Type)
		if !c.Data[sym.Index].IsValid() || c.Data[sym.Index].Type() == rt {
			continue
		}
		c.Data[sym.Index] = vm.NewValue(rt)
	}
}

// liveSynthRtype upgrades a field-copy's frozen Rtype to its named source's
// post-synth-attach rtype, so `w := o.Weight` (a copy of `type Grams int`)
// keeps Grams's methods. Canonical types (Base == nil) are already live.
// Basic-kind only: composite copies carry element identity that
// CanonicalType's underlying-type walk would discard.
func liveSynthRtype(t *vm.Type) reflect.Type {
	if t.Base != nil && isBasicSynthKind(t.Kind()) {
		// Require a differing, method-bearing canonical rtype: a ptr-receiver
		// named type (`cv2 := customValue(10)`, empty value method set) must
		// keep its own rtype so &cv2 reaches the *customValue methods.
		if ct := vm.CanonicalType(t); ct != nil && ct.Rtype != nil &&
			ct.Rtype != t.Rtype && ct.Rtype.NumMethod() > 0 {
			return ct.Rtype
		}
	}
	return t.Rtype
}

func isBasicSynthKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return true
	}
	return false
}

// RebuildSynthStructRtypes walks every interpreted struct *vm.Type reachable
// from compiler-tracked symbols and patches in-place any field whose Live
// rtype no longer matches the rtype currently embedded in t.Rtype.Field(i).
// Struct rtype identity is preserved (no reflect.StructOf rebuild) so any
// compile-time captures of t.Rtype stay aligned AND the AttachPtrMethods *T
// wired into t.Rtype.PtrToThis stays valid (a full RefreshRtype cascade
// would overwrite d.ptr.Rtype with a fresh methodless synth.PointerTo
// result, destroying the ptr-recv method attachment).
// Called from interp/synth.go after the per-type AttachSynthMethods cascade
// and BEFORE RefreshSynthRtype so the slot-level sweep observes any field
// type swap.
// Layout safety + concurrency are handled by vm.PatchSynthStructFields, which
// serializes the in-place field-rtype writes under the same lock StructOf uses
// (a struct *Type is shared across concurrently-compiling Interps via the
// global StructOf cache, so the patch must not race reflect.StructOf reads).
func (c *Compiler) RebuildSynthStructRtypes() {
	structs := c.collectSynthStructs()
	for t := range structs {
		vm.PatchSynthStructFields(t)
	}
}

// RebuildSynthSliceRtypes refreshes the frozen element of every named synth
// slice type (e.g. `type ByAge []Person` with a method); the in-Type cascade
// reaches the unnamed []Person but not the named slice. Mirrors
// RebuildSynthStructRtypes; called alongside it, before RefreshSynthRtype.
func (c *Compiler) RebuildSynthSliceRtypes() {
	for t := range c.collectSynthSlices() {
		vm.PatchSynthSliceElem(t)
	}
}

// collectSynthSlices returns the named slice *Types reachable from the dedup
// maps, var symbols, and nested fields/elem/key. The synth gate lives in
// PatchSynthSliceElem, so non-synth entries collected here are no-ops.
func (c *Compiler) collectSynthSlices() map[*vm.Type]bool {
	seen := map[*vm.Type]bool{}
	out := map[*vm.Type]bool{}
	var visit func(t *vm.Type)
	visit = func(t *vm.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		if t.Base != nil && t.Rtype != nil && t.Kind() == reflect.Slice {
			out[t] = true
		}
		for _, f := range t.Fields {
			visit(f)
		}
		visit(t.ElemType)
		visit(t.KeyType)
	}
	for t := range c.zeroTypeIdxs {
		visit(t)
	}
	for t := range c.typeSyms {
		visit(t)
	}
	for _, sym := range c.Symbols {
		visit(sym.Type)
	}
	return out
}

// collectSynthStructs returns the set of distinct interpreted struct *Types
// reachable from the compiler's type dedup maps (zeroTypeIdxs + typeSyms)
// and from var symbols' Types.
// Field types whose Base points elsewhere are followed: the canonical
// struct *Type (the one whose rebuild matters) is what we collect.
func (c *Compiler) collectSynthStructs() map[*vm.Type]bool {
	seen := map[*vm.Type]bool{}
	var visit func(t *vm.Type)
	visit = func(t *vm.Type) {
		if t == nil {
			return
		}
		canonical := t
		for canonical.Base != nil {
			canonical = canonical.Base
		}
		if canonical.Rtype == nil || canonical.Kind() != reflect.Struct {
			return
		}
		if seen[canonical] {
			return
		}
		seen[canonical] = true
		for _, f := range canonical.Fields {
			visit(f)
		}
	}
	for t := range c.zeroTypeIdxs {
		visit(t)
	}
	for t := range c.typeSyms {
		visit(t)
	}
	for _, sym := range c.Symbols {
		if sym.Type != nil {
			visit(sym.Type)
		}
	}
	return seen
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
		for k := 0; k < narg; k++ {
			argSym := stack[len(stack)-narg+k]
			paramType := funcType.In(k)
			if argSym.Type == nil || argSym.Type.Rtype == paramType {
				continue
			}
			c.emitNumConvert(t, &vm.Type{Rtype: paramType}, argSym.Type, narg-1-k)
		}
	}
	// Pop function symbol and argument symbols, push return type.
	for i := 0; i < narg; i++ {
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
