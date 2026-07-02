// Package symbol implements symbol utilities.
package symbol

import (
	"go/constant"
	"reflect"
	"slices"
	"strings"

	"github.com/mvm-sh/mvm/internal/derive"
	"github.com/mvm-sh/mvm/internal/runtype"
	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/vm"
)

// Kind represents the symbol kind.
type Kind int

// Symbol kinds.
const (
	Unset    Kind = iota
	Value         // a value defined in the runtime
	Type          // a type
	Label         // a label indicating a position in the VM code
	Const         // a constant
	Var           // a variable in global data
	LocalVar      // a variable in the local call frame
	Func          // a function, located in the VM code
	Pkg           // a package
	Builtin       // a built-in function (len, cap, append, etc.)
	Generic       // a generic function or type template
)

//go:generate stringer -type=Kind

// UnsetAddr denotes an unset symbol index (vs 0).
const UnsetAddr = -65535

// Symbol structure used in parser and compiler.
type Symbol struct {
	Kind       Kind
	Name       string         //
	Index      int            // address of symbol in frame
	PkgPath    string         //
	Type       *mtype.Type    //
	Value      vm.Value       //
	Cval       constant.Value //
	Used       bool           //
	AutoImport bool           // Pkg bound by AutoImportPackages (ambient convenience), not an explicit import statement
	Captured   bool           // true if this variable escapes to a heap cell
	LoopVar    bool           // true if this is a for-init or range variable (snapshot capture)
	CellSlot   bool           // true if the local frame slot holds a heap cell pointer (promoted)
	FreeVars   []string       // closure: scoped names of captured outer-scope locals, in Heap order
	RecvName   string         // for methods: raw receiver variable name
	InNames    []string       // raw input param names, cached from Phase 1 for Phase 2
	OutNames   []string       // raw output param names, cached from Phase 1 for Phase 2
	MethodExpr bool           // true if this is a method expression (Type.Method)
	Composite  bool           // true if this symbol is a composite literal value (T{})
	NoFnew     bool           // true if this Type-kind stack entry was pushed without an Fnew emit (T(x), T.M, etc.); consumers must skip removeFnew
	// FieldOffset and HasFieldOffset are set when this symbol is produced by a
	// struct field selector chain; FieldOffset is the accumulated byte offset
	// from the outermost operand to this field. Used by unsafe.Offsetof.
	HasFieldOffset bool
	FieldOffset    uintptr
	Data           any              // optional extra data (e.g. generic template)
	Reads          map[*Symbol]bool // for Func: package-level Var symbols read transitively by the body
}

// NeedsCell reports whether this variable should be promoted to a heap cell
// (captured by a closure and not a loop iteration variable).
func (s *Symbol) NeedsCell() bool { return s.Captured && !s.LoopVar }

// FreeVarIndex returns the index of name in FreeVars, or -1 if not found.
func (s *Symbol) FreeVarIndex(name string) int {
	for i, fv := range s.FreeVars {
		if fv == name {
			return i
		}
	}
	return -1
}

// func (s *Symbol) String() string {
//  	return fmt.Sprintf("{Kind: %v, Name: %v, Index: %v, Type: %v}\n", s.Kind, s.Name, s.Index, s.Type)
//}

// IsConst returns true if symbol is a constant.
func (s *Symbol) IsConst() bool { return s.Kind == Const }

// IsType returns true if symbol is a type.
func (s *Symbol) IsType() bool { return s.Kind == Type }

// IsFunc returns true if symbol is a function.
func (s *Symbol) IsFunc() bool { return s.Kind == Func }

// IsPtr returns true if symbol is a pointer.
func (s *Symbol) IsPtr() bool { return s.Type.Kind() == reflect.Pointer }

// IsInt returns true if symbol is an int.
func (s *Symbol) IsInt() bool { return s.Type.Kind() == reflect.Int }

// IsParam reports whether s is a function parameter slot.
func (s *Symbol) IsParam() bool { return s.Index < 0 && s.Index != UnsetAddr }

// Vtype returns the VM type of a symbol.
func Vtype(s *Symbol) *mtype.Type {
	if s.Type != nil {
		return s.Type
	}
	if s.Value.IsValid() {
		return &mtype.Type{Rtype: s.Value.Type()}
	}
	return nil
}

// SymMap is a map of Symbols.
type SymMap map[string]*Symbol

// SegIndex maps a key's last dot-segment to the keys sharing it, so method
// resolution probes a few candidates instead of scanning the whole table (O(n),
// quadratic on 60k-symbol units like protobuf). nil means "no index": full scan.
type SegIndex map[string][]string

// LastSeg returns key's text after the final '.', or key itself if there is none.
func LastSeg(key string) string {
	if i := strings.LastIndexByte(key, '.'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// Add records key under its last segment (idempotent).
func (idx SegIndex) Add(key string) {
	s := LastSeg(key)
	if slices.Contains(idx[s], key) {
		return
	}
	idx[s] = append(idx[s], key)
}

// Del removes key from its segment bucket.
func (idx SegIndex) Del(key string) {
	s := LastSeg(key)
	b := idx[s]
	for i, k := range b {
		if k == key {
			b[i] = b[len(b)-1]
			idx[s] = b[:len(b)-1]
			return
		}
	}
}

// Get searches for an existing symbol starting from the deepest scope.
func (sm SymMap) Get(name, scope string) (sym *Symbol, sc string, ok bool) {
	for {
		if sym, ok = sm[scope+"/"+name]; ok {
			return sym, scope, ok
		}
		i := strings.LastIndex(scope, "/")
		if i == -1 {
			i = 0
		}
		if scope = scope[:i]; scope == "" {
			break
		}
	}
	sym, ok = sm[name]
	return sym, scope, ok
}

// bareKeyTypeMatches reports whether the Type symbol at the bare key (if any)
// is the receiver's own type, so a same-named unit-local type cannot hijack
// the unqualified method probe for an imported receiver (cmp_test.Stringer
// answering a testprotos.Stringer call). A missing or non-Type bare symbol
// allows the probe: there is no conflicting type to resolve against.
func (sm SymMap) bareKeyTypeMatches(key string, recv *mtype.Type) bool {
	s, ok := sm[key]
	if !ok || s.Kind != Type || s.Type == nil || recv == nil {
		return true
	}
	rv := recv
	if rv.IsPtr() && rv.ElemType != nil {
		rv = rv.ElemType
	}
	recvRt := rv.Rtype
	if recvRt != nil && recvRt.Kind() == reflect.Pointer {
		recvRt = recvRt.Elem()
	}
	if sameNamedType(s.Type, rv, derive.CanonicalType(rv), recvRt) {
		return true
	}
	// Linkage unprovable (a Base-less clone probed before materialization):
	// fall back to import-path + name (a same-package clone keeps its bare probe;
	// a foreign same-short-named receiver is refused).
	if recvRt == nil || s.Type.Rtype == nil {
		return s.Type.SameNamedType(rv)
	}
	return false
}

// sameNamedType matches cand against the receiver by *Type identity (recvCanon
// is recv's Base-walked canonical, so a clone matches its source), falling back
// to rtype equality only when both already carry one -- never materializing.
func sameNamedType(cand, recv, recvCanon *mtype.Type, recvRt reflect.Type) bool {
	if cand == recv || derive.CanonicalType(cand) == recvCanon {
		return true
	}
	return recvRt != nil && cand.Rtype != nil && cand.Rtype == recvRt
}

// Ambiguous is the sentinel MethodByName returns for a promoted method two or more embeds provide at the same shallowest depth (Go's "ambiguous selector"); callers treat it as unresolved.
var Ambiguous = &Symbol{Name: "<ambiguous>"}

// MethodByName returns the method symbol and the field index path to the receiver
// (empty for direct methods, non-empty for promoted methods through embedded fields).
// seg is the candidate index; nil falls back to a full scan.
func (sm SymMap) MethodByName(sym *Symbol, name string, seg SegIndex) (*Symbol, []int) {
	// Every resolution below returns a concrete method symbol keyed
	// "<recv>.name" (optionally "*"-prefixed) and of Kind Func. If no such
	// method exists anywhere, no receiver search can find one -- so skip it.
	// This turns the dominant compile cost on large packages (a whole-table
	// scan per `x.field` selector whose receiver type is anonymous, ubiquitous
	// in ccgo-generated code like modernc.org/sqlite) into an O(1) index probe.
	if seg != nil && !sm.hasMethodNamed(name, seg) {
		return nil, nil
	}
	switch sym.Kind {
	case Type:
		// Guard the bare probe: a dot-imported or alias-copied symbol keeps a
		// bare Name a same-named unit-local type would otherwise hijack.
		if sm.bareKeyTypeMatches(strings.TrimPrefix(sym.Name, "*"), sym.Type) {
			if m := methodLookup(sm, sym.Name, name); m != nil {
				return m, nil
			}
			// Pointer type: also try value-receiver methods (*T includes T's method set).
			if strings.HasPrefix(sym.Name, "*") {
				if m := methodLookup(sm, sym.Name[1:], name); m != nil {
					return m, nil
				}
			}
		}
		// Probe pkg-qualified method keys too, so a composite-literal receiver
		// like `pkg.T{}.M()` resolves, not just a variable of that type.
		// An unnamed pointer type takes its elem's name (e.g. (*T).M).
		if sym.Type != nil {
			recvName := sym.Type.Name
			if recvName == "" && sym.Type.IsPtr() && sym.Type.ElemType != nil {
				recvName = sym.Type.ElemType.Name
			}
			if recvName != "" {
				if m := sm.qualifiedMethodLookup(sym.Type, recvName, name, seg); m != nil {
					return m, nil
				}
			}
		}
		return sm.promotedMethod(sym.Type, name, nil, seg)
	case Var, LocalVar, Value, Const:
		// A typed constant has the method set of its named type, just like a
		// variable (e.g. `const idx tag.Index = "..."; idx.Index(key)`).
		if sym.Type == nil {
			return nil, nil
		}
		typName := sym.Type.Name
		// For types with no Name (e.g. mvm-created structs, or pointer types),
		// search the symbol table for a named Type with a matching Rtype.
		// Multiple keys can map to the same type (e.g. importSrc registers a
		// qualified alias `pkgpath.T` aside the short `T`, and zeroInitLocals
		// can register an anonymous-struct stringification like `struct{...}`).
		// Methods are registered under the short receiver name (`*T.M`), so
		// prefer whichever candidate key actually has a registered method.
		if typName == "" {
			// Strip a pointer wrapper symbolically (interpreted ptrs carry ElemType).
			recv := sym.Type
			if recv.IsPtr() && recv.ElemType != nil {
				recv = recv.ElemType
			}
			if recv.Name != "" {
				// Named element (e.g. a `*T` local): the probes below resolve it
				// by name/identity, so skip the whole-table scan (the dominant
				// compile cost on large packages).
				typName = recv.Name
			} else {
				// Anonymous receiver (an mvm-created struct): no name to key on,
				// so match a named Type symbol by *Type identity / rtype.
				recvCanon := derive.CanonicalType(recv)
				recvRt := recv.Rtype
				if recvRt != nil && recvRt.Kind() == reflect.Pointer {
					recvRt = recvRt.Elem()
				}
				// Candidates rank by closeness: exact *Type identity beats a
				// same-Name canonical match (a clone), which beats any other
				// canonical match (e.g. `type Y X` and X share a canonical but
				// Y's methods are NOT X's; map order must not pick between them).
				var firstName, exactName, namedName, canonName string
				for k, s := range sm {
					if s.Kind != Type || s.Type == nil || k == "" {
						continue
					}
					if !sameNamedType(s.Type, recv, recvCanon, recvRt) {
						continue
					}
					if firstName == "" {
						firstName = k
					}
					if methodLookup(sm, k, name) == nil && methodLookup(sm, "*"+k, name) == nil {
						continue
					}
					switch {
					case s.Type == recv:
						exactName = k
					case s.Type.Name == recv.Name && namedName == "":
						namedName = k
					case canonName == "":
						canonName = k
					}
					if exactName != "" {
						break
					}
				}
				switch {
				case exactName != "":
					typName = exactName
				case namedName != "":
					typName = namedName
				case canonName != "":
					typName = canonName
				case firstName != "":
					typName = firstName
				}
			}
		}
		if sm.bareKeyTypeMatches(typName, sym.Type) {
			if m := methodLookup(sm, typName, name); m != nil {
				return m, nil
			}
			if m := methodLookup(sm, "*"+typName, name); m != nil {
				return m, nil
			}
		}
		// Path B step 2 stores methods at pkg-qualified keys for imported pkgs
		// ("<pkgPath>.<Tag>.<M>" or "*<pkgPath>.<Tag>.<M>"). When the short
		// typName missed AND the rtype has no NATIVE method by this name (so
		// it's not a stdlib bridge like time.Duration.String), search the
		// symbol table for a user Type Symbol whose canonical key ends in
		// ".<typName>" and matches this rtype, then probe that pkg-qualified
		// method key. The native-method check is what keeps a user
		// `type durationValue time.Duration` from hijacking calls intended
		// for the stdlib's *time.Duration.String.
		if typName != "" {
			// Probe for a native method (a stdlib bridge like time.Duration.String)
			// only when the canonical underlying is native; interpreted types have
			// none, so skip the probe (and the materialization it forced).
			nativeVal, nativePtr := false, false
			if canon := derive.CanonicalType(sym.Type); canon != nil && canon.Rtype != nil {
				rt := canon.Rtype
				ptype := rt
				if ptype.Kind() == reflect.Pointer {
					ptype = ptype.Elem()
				}
				// A synth-built rtype carries methods ATTACHED from interpreted
				// code (AttachSynthMethods), not a stdlib bridge; they must not
				// preempt the qualified lookup -- the synth wrapper would mutate
				// a boxed copy of the receiver, losing ptr-recv write-backs.
				if !runtype.IsSynth(ptype) {
					_, nativeVal = rt.MethodByName(name)
					_, nativePtr = reflect.PointerTo(ptype).MethodByName(name)
				}
			}
			if !nativeVal && !nativePtr {
				if m := sm.qualifiedMethodLookup(sym.Type, typName, name, seg); m != nil {
					return m, nil
				}
			}
		}
		return sm.promotedMethod(sym.Type, name, nil, seg)
	}
	return nil, nil
}

// qualifiedMethodLookup finds a method registered at a pkg-qualified canonical
// key (Path B step 2, [[project_phase2_path_b_step2_funcs_methods]]). It searches
// sm for a Type Symbol matching recv and whose key ends in ".<typName>", then
// probes `<key>.<method>` and `*<key>.<method>`.
//
// Matching is by *Type identity, not rtype: two distinct mvm Types can share the
// same Rtype (e.g. `compact.Tag` and the outer `language.Tag`, both `struct{P30
// int}` in x/text via `type Tag compact.Tag`), and rtype-only matching would pick
// one at random -- the root cause of [[project_isroot_iface_dispatch_recursion]].
// An rtype-equality match is kept only as a fallback for native types.
func (sm SymMap) qualifiedMethodLookup(recv *mtype.Type, typName, method string, seg SegIndex) *Symbol {
	if recv == nil {
		return nil
	}
	// Methods register under the value type: strip a pointer wrapper symbolically
	// (interpreted ptrs carry ElemType) and canonicalize so a clone matches its
	// source. Identity matching below avoids materializing an interpreted type.
	rv := recv
	if rv.IsPtr() && rv.ElemType != nil {
		rv = rv.ElemType
	}
	rvCanon := derive.CanonicalType(rv)
	rt := rv.Rtype // native value-type rtype, fallback only
	if rt == nil && rvCanon != nil {
		rt = rvCanon.Rtype
	}
	if rt != nil && rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	suffix := "." + typName
	probe := func(k string) *Symbol {
		if m := sm[k+"."+method]; m != nil {
			return m
		}
		return sm["*"+k+"."+method]
	}
	var fallback, nameFallback *Symbol
	// consider returns a definitive identity match for candidate k, else nil,
	// recording rtype/name fallbacks along the way.
	consider := func(k string, s *Symbol) *Symbol {
		if k == "" || s == nil || s.Kind != Type || s.Type == nil || !strings.HasSuffix(k, suffix) {
			return nil
		}
		m := probe(k)
		if m == nil {
			return nil
		}
		cv := s.Type
		if cv.IsPtr() && cv.ElemType != nil {
			cv = cv.ElemType
		}
		if cv == rv || derive.CanonicalType(cv) == rvCanon {
			return m
		}
		srt := cv.Rtype
		if srt != nil && srt.Kind() == reflect.Pointer {
			srt = srt.Elem()
		}
		if rt != nil && srt == rt && fallback == nil {
			fallback = m
		}
		// A Go type is identified by package path + name, so a same-name/same-pkg
		// receiver IS this type even when the *Type object identity differs --
		// mvm can build distinct instances for one Go type across file-by-file or
		// multi-pass compilation, so neither pointer nor rtype identity holds.
		if nameFallback == nil && cv.Name != "" && cv.SameNamedType(rv) {
			nameFallback = m
		}
		return nil
	}
	// seg[typName] is a superset of the ".typName" suffix matches, which consider
	// re-checks -- a few probes instead of a whole-table scan. nil seg: full scan.
	if seg != nil {
		for _, k := range seg[typName] {
			if m := consider(k, sm[k]); m != nil {
				return m
			}
		}
	} else {
		for k, s := range sm {
			if m := consider(k, s); m != nil {
				return m
			}
		}
	}
	if fallback != nil {
		return fallback
	}
	return nameFallback
}

// hasMethodNamed reports whether any symbol is a method named `name`: a Func
// keyed "<recv>.name" (optionally "*"-prefixed). seg[name] holds every key whose
// last dot-segment is name, so this is an index probe, not a whole-table scan.
func (sm SymMap) hasMethodNamed(name string, seg SegIndex) bool {
	suffix := "." + name
	for _, k := range seg[name] {
		if !strings.HasSuffix(k, suffix) {
			continue // a bare "name" (top-level decl), not "<recv>.name"
		}
		if s := sm[k]; s != nil && s.Kind == Func {
			return true
		}
	}
	return false
}

// methodLookup finds `<typName>.<method>` in sm. With Path B steps 1+2 done,
// methods live next to their receiver type: bare for main/REPL, pkg-qualified
// for imported pkgs. Callers pass the canonical type key (the key under which
// the receiver type is registered) and methodLookup composes `<key>.<method>`.
func methodLookup(sm SymMap, typName, method string) *Symbol {
	return sm[typName+"."+method]
}

// promotedMethod searches for a method promoted through embedded fields recorded in typ.Embedded.
// It returns the method symbol and the field index path to reach the embedded receiver.
func (sm SymMap) promotedMethod(typ *mtype.Type, name string, path []int, seg SegIndex) (*Symbol, []int) {
	return sm.promotedMethodSeen(typ, name, path, nil, seg)
}

// promotedMethodSeen is promotedMethod with a visited set guarding against
// self-referential embedding (legal in Go, e.g. `type A struct{ *A }`), which
// would otherwise recurse until the stack overflows.
// It walks breadth-first so the shallowest embed wins; a depth-first walk would pick a deeper method in an earlier embed.
func (sm SymMap) promotedMethodSeen(typ *mtype.Type, name string, path []int, seen map[*mtype.Type]bool, seg SegIndex) (*Symbol, []int) {
	if typ == nil {
		return nil, nil
	}
	if seen == nil {
		seen = map[*mtype.Type]bool{}
	}
	type node struct {
		typ  *mtype.Type
		path []int
	}
	// A pointer or field-access clone can lack Embedded; walk to the underlying struct or canonical.
	norm := func(t *mtype.Type) *mtype.Type {
		if t.IsPtr() && t.ElemType != nil {
			t = t.ElemType
		}
		if len(t.Embedded) == 0 {
			if canon := derive.CanonicalType(t); canon != t && len(canon.Embedded) > 0 {
				t = canon
			}
		}
		return t
	}
	for level := []node{{norm(typ), path}}; len(level) > 0; {
		var next []node
		var found *Symbol
		var foundPath []int
		for _, nd := range level {
			if seen[nd.typ] {
				continue
			}
			seen[nd.typ] = true
			for _, emb := range nd.typ.Embedded {
				embType := emb.Type
				if embType == nil {
					continue
				}
				fieldPath := append(nd.path[:len(nd.path):len(nd.path)], emb.FieldIdx) //nolint:gocritic
				if m := sm.embedDeclaresMethod(embType, name, seg); m != nil {
					if found != nil {
						return Ambiguous, nil // two providers at this shallowest depth
					}
					found, foundPath = m, fieldPath
					continue // resolved here; don't descend past it
				}
				next = append(next, node{norm(embType), fieldPath})
			}
		}
		if found != nil {
			return found, foundPath
		}
		level = next
	}
	return nil, nil
}

// embedDeclaresMethod returns the method embType declares directly (not promoted from its own embeds), or nil.
func (sm SymMap) embedDeclaresMethod(embType *mtype.Type, name string, seg SegIndex) *Symbol {
	if sm.bareKeyTypeMatches(embType.Name, embType) {
		if m := sm[embType.Name+"."+name]; m != nil {
			return m
		}
		if m := sm["*"+embType.Name+"."+name]; m != nil {
			return m
		}
	}
	if embType.Name != "" { // method may live at a pkg-qualified key (Path B)
		if m := sm.qualifiedMethodLookup(embType, embType.Name, name, seg); m != nil {
			return m
		}
	}
	return nil
}

// Init fills the symbol map with default Go symbols.
func (sm SymMap) Init() {
	sm["any"] = &Symbol{Name: "any", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*any)(nil)).Elem()}
	sm["bool"] = &Symbol{Name: "bool", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*bool)(nil)).Elem()}
	sm["error"] = &Symbol{Name: "error", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*error)(nil)).Elem()}
	sm["int"] = &Symbol{Name: "int", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*int)(nil)).Elem()}
	sm["int8"] = &Symbol{Name: "int8", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*int8)(nil)).Elem()}
	sm["int16"] = &Symbol{Name: "int16", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*int16)(nil)).Elem()}
	sm["int32"] = &Symbol{Name: "int32", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*int32)(nil)).Elem()}
	sm["int64"] = &Symbol{Name: "int64", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*int64)(nil)).Elem()}
	sm["uint"] = &Symbol{Name: "uint", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*uint)(nil)).Elem()}
	sm["uint8"] = &Symbol{Name: "uint8", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*uint8)(nil)).Elem()}
	sm["uint16"] = &Symbol{Name: "uint16", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*uint16)(nil)).Elem()}
	sm["uint32"] = &Symbol{Name: "uint32", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*uint32)(nil)).Elem()}
	sm["uint64"] = &Symbol{Name: "uint64", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*uint64)(nil)).Elem()}
	sm["uintptr"] = &Symbol{Name: "uintptr", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*uintptr)(nil)).Elem()}
	sm["float32"] = &Symbol{Name: "float32", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*float32)(nil)).Elem()}
	sm["float64"] = &Symbol{Name: "float64", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*float64)(nil)).Elem()}
	sm["complex64"] = &Symbol{Name: "complex64", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*complex64)(nil)).Elem()}
	sm["complex128"] = &Symbol{Name: "complex128", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*complex128)(nil)).Elem()}
	sm["byte"] = sm["uint8"]
	sm["rune"] = sm["int32"]
	sm["string"] = &Symbol{Name: "string", Kind: Type, Index: UnsetAddr, Type: mtype.TypeOf((*string)(nil)).Elem()}

	sm["nil"] = &Symbol{Name: "nil", Kind: Value, Index: UnsetAddr}
	sm["iota"] = &Symbol{Name: "iota", Kind: Const, Index: UnsetAddr}
	sm["true"] = &Symbol{Name: "true", Kind: Const, Index: UnsetAddr, Value: vm.ValueOf(true), Type: mtype.TypeOf(true), Cval: constant.MakeBool(true)}
	sm["false"] = &Symbol{Name: "false", Kind: Const, Index: UnsetAddr, Value: vm.ValueOf(false), Type: mtype.TypeOf(false), Cval: constant.MakeBool(false)}

	sm["complex"] = &Symbol{Name: "complex", Kind: Builtin, Index: UnsetAddr}
	sm["real"] = &Symbol{Name: "real", Kind: Builtin, Index: UnsetAddr}
	sm["imag"] = &Symbol{Name: "imag", Kind: Builtin, Index: UnsetAddr}
	sm["print"] = &Symbol{Name: "print", Kind: Builtin, Index: UnsetAddr}
	sm["println"] = &Symbol{Name: "println", Kind: Builtin, Index: UnsetAddr}
	sm["panic"] = &Symbol{Name: "panic", Kind: Builtin, Index: UnsetAddr}
	sm["recover"] = &Symbol{Name: "recover", Kind: Builtin, Index: UnsetAddr}
	sm["len"] = &Symbol{Name: "len", Kind: Builtin, Index: UnsetAddr}
	sm["cap"] = &Symbol{Name: "cap", Kind: Builtin, Index: UnsetAddr}
	sm["append"] = &Symbol{Name: "append", Kind: Builtin, Index: UnsetAddr}
	sm["copy"] = &Symbol{Name: "copy", Kind: Builtin, Index: UnsetAddr}
	sm["delete"] = &Symbol{Name: "delete", Kind: Builtin, Index: UnsetAddr}
	sm["clear"] = &Symbol{Name: "clear", Kind: Builtin, Index: UnsetAddr}
	sm["new"] = &Symbol{Name: "new", Kind: Builtin, Index: UnsetAddr}
	sm["make"] = &Symbol{Name: "make", Kind: Builtin, Index: UnsetAddr}
	sm["close"] = &Symbol{Name: "close", Kind: Builtin, Index: UnsetAddr}
	sm["min"] = &Symbol{Name: "min", Kind: Builtin, Index: UnsetAddr}
	sm["max"] = &Symbol{Name: "max", Kind: Builtin, Index: UnsetAddr}
	sm["trap"] = &Symbol{Name: "trap", Kind: Builtin, Index: UnsetAddr}
}
