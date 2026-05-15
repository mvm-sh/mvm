// Package symbol implements symbol utilities.
package symbol

import (
	"go/constant"
	"reflect"
	"strings"

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
	Type       *vm.Type       //
	Value      vm.Value       //
	Cval       constant.Value //
	Used       bool           //
	Captured   bool           // true if this variable escapes to a heap cell
	LoopVar    bool           // true if this is a for-init or range variable (snapshot capture)
	CellSlot   bool           // true if the local frame slot holds a heap cell pointer (promoted)
	FreeVars   []string       // closure: scoped names of captured outer-scope locals, in Heap order
	RecvName   string         // for methods: raw receiver variable name
	RecvType   *vm.Type       // for methods: receiver base type resolved at signature time (Phase 1), used to bind the receiver local in Phase 2 without re-resolving the (possibly now-shadowed) bare type name. TODO(qualified-symbols): drop once receiver types resolve via package-qualified keys.
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
	// PassthroughTarget marks a func literal whose body is exactly
	// `return TARGET(params...)` with args matching the literal's params 1:1
	// and no other statements. Holds the qualified name path of TARGET
	// (e.g. ["regexp", "MatchString"]). If TARGET resolves to a native func of
	// the same Go type, the compiler emits a reference to TARGET instead of
	// building the closure, skipping the per-call bridge.
	PassthroughTarget []string
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
func (s *Symbol) IsPtr() bool { return s.Type.Rtype.Kind() == reflect.Pointer }

// IsInt returns true if symbol is an int.
func (s *Symbol) IsInt() bool { return s.Type.Rtype.Kind() == reflect.Int }

// IsParam reports whether s is a function parameter slot.
func (s *Symbol) IsParam() bool { return s.Index < 0 && s.Index != UnsetAddr }

// Vtype returns the VM type of a symbol.
func Vtype(s *Symbol) *vm.Type {
	if s.Type != nil {
		return s.Type
	}
	if s.Value.IsValid() {
		return &vm.Type{Rtype: s.Value.Type()}
	}
	return nil
}

// SymMap is a map of Symbols.
type SymMap map[string]*Symbol

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

// MethodByName returns the method symbol and the field index path to the receiver
// (empty for direct methods, non-empty for promoted methods through embedded fields).
func (sm SymMap) MethodByName(sym *Symbol, name string) (*Symbol, []int) {
	switch sym.Kind {
	case Type:
		if m := methodLookup(sm, sym.Name, name); m != nil {
			return m, nil
		}
		// Pointer type: also try value-receiver methods (*T includes T's method set).
		if strings.HasPrefix(sym.Name, "*") {
			if m := methodLookup(sm, sym.Name[1:], name); m != nil {
				return m, nil
			}
		}
		return sm.promotedMethod(sym.Type, name, nil)
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
			rtype := sym.Type.Rtype
			if rtype.Kind() == reflect.Pointer {
				rtype = rtype.Elem()
			}
			var firstName string
			for k, s := range sm {
				if s.Kind != Type || s.Type == nil || s.Type.Rtype != rtype || k == "" {
					continue
				}
				if firstName == "" {
					firstName = k
				}
				if methodLookup(sm, k, name) != nil || methodLookup(sm, "*"+k, name) != nil {
					typName = k
					break
				}
			}
			if typName == "" {
				typName = firstName
			}
		}
		if m := methodLookup(sm, typName, name); m != nil {
			return m, nil
		}
		if m := methodLookup(sm, "*"+typName, name); m != nil {
			return m, nil
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
			ptype := sym.Type.Rtype
			if ptype.Kind() == reflect.Pointer {
				ptype = ptype.Elem()
			}
			_, nativeVal := sym.Type.Rtype.MethodByName(name)
			_, nativePtr := reflect.PointerTo(ptype).MethodByName(name)
			if !nativeVal && !nativePtr {
				if m := sm.qualifiedMethodLookup(ptype, typName, name); m != nil {
					return m, nil
				}
			}
		}
		return sm.promotedMethod(sym.Type, name, nil)
	}
	return nil, nil
}

// qualifiedMethodLookup finds a method registered at a pkg-qualified canonical
// key. It searches sm for a Type Symbol whose underlying Rtype matches rt (after
// stripping a pointer wrapper) and whose key ends in ".<typName>", then probes
// `<key>.<method>` and `*<key>.<method>`. Path B step 2 ([[project_phase2_path_b_step2_funcs_methods]])
// places methods at these keys for imported pkgs.
func (sm SymMap) qualifiedMethodLookup(rt reflect.Type, typName, method string) *Symbol {
	suffix := "." + typName
	for k, s := range sm {
		if k == "" || s.Kind != Type || s.Type == nil {
			continue
		}
		srt := s.Type.Rtype
		if srt != nil && srt.Kind() == reflect.Pointer {
			srt = srt.Elem()
		}
		if srt != rt || !strings.HasSuffix(k, suffix) {
			continue
		}
		if m := sm[k+"."+method]; m != nil {
			return m
		}
		if m := sm["*"+k+"."+method]; m != nil {
			return m
		}
	}
	return nil
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
func (sm SymMap) promotedMethod(typ *vm.Type, name string, path []int) (*Symbol, []int) {
	if typ == nil {
		return nil, nil
	}
	// Pointer types carry no Embedded info themselves; walk into the underlying
	// struct so receivers like *anon-struct see their embedded fields' methods.
	if typ.IsPtr() && typ.ElemType != nil {
		typ = typ.ElemType
	}
	for _, emb := range typ.Embedded {
		embType := emb.Type
		if embType == nil {
			continue
		}
		fieldPath := append(path, emb.FieldIdx) //nolint:gocritic
		if m := sm[embType.Name+"."+name]; m != nil {
			return m, fieldPath
		}
		if m := sm["*"+embType.Name+"."+name]; m != nil {
			return m, fieldPath
		}
		// Embedded type's method may live at a pkg-qualified key (Path B).
		if embType.Rtype != nil && embType.Name != "" {
			ert := embType.Rtype
			if ert.Kind() == reflect.Pointer {
				ert = ert.Elem()
			}
			if m := sm.qualifiedMethodLookup(ert, embType.Name, name); m != nil {
				return m, fieldPath
			}
		}
		if m, p := sm.promotedMethod(embType, name, fieldPath); m != nil {
			return m, p
		}
	}
	return nil, nil
}

// Init fills the symbol map with default Go symbols.
func (sm SymMap) Init() {
	sm["any"] = &Symbol{Name: "any", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*any)(nil)).Elem()}
	sm["bool"] = &Symbol{Name: "bool", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*bool)(nil)).Elem()}
	sm["error"] = &Symbol{Name: "error", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*error)(nil)).Elem()}
	sm["int"] = &Symbol{Name: "int", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*int)(nil)).Elem()}
	sm["int8"] = &Symbol{Name: "int8", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*int8)(nil)).Elem()}
	sm["int16"] = &Symbol{Name: "int16", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*int16)(nil)).Elem()}
	sm["int32"] = &Symbol{Name: "int32", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*int32)(nil)).Elem()}
	sm["int64"] = &Symbol{Name: "int64", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*int64)(nil)).Elem()}
	sm["uint"] = &Symbol{Name: "uint", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*uint)(nil)).Elem()}
	sm["uint8"] = &Symbol{Name: "uint8", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*uint8)(nil)).Elem()}
	sm["uint16"] = &Symbol{Name: "uint16", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*uint16)(nil)).Elem()}
	sm["uint32"] = &Symbol{Name: "uint32", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*uint32)(nil)).Elem()}
	sm["uint64"] = &Symbol{Name: "uint64", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*uint64)(nil)).Elem()}
	sm["uintptr"] = &Symbol{Name: "uintptr", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*uintptr)(nil)).Elem()}
	sm["float32"] = &Symbol{Name: "float32", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*float32)(nil)).Elem()}
	sm["float64"] = &Symbol{Name: "float64", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*float64)(nil)).Elem()}
	sm["complex64"] = &Symbol{Name: "complex64", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*complex64)(nil)).Elem()}
	sm["complex128"] = &Symbol{Name: "complex128", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*complex128)(nil)).Elem()}
	sm["byte"] = sm["uint8"]
	sm["rune"] = sm["int32"]
	sm["string"] = &Symbol{Name: "string", Kind: Type, Index: UnsetAddr, Type: vm.TypeOf((*string)(nil)).Elem()}

	sm["nil"] = &Symbol{Name: "nil", Kind: Value, Index: UnsetAddr}
	sm["iota"] = &Symbol{Name: "iota", Kind: Const, Index: UnsetAddr}
	sm["true"] = &Symbol{Name: "true", Kind: Const, Index: UnsetAddr, Value: vm.ValueOf(true), Type: vm.TypeOf(true), Cval: constant.MakeBool(true)}
	sm["false"] = &Symbol{Name: "false", Kind: Const, Index: UnsetAddr, Value: vm.ValueOf(false), Type: vm.TypeOf(false), Cval: constant.MakeBool(false)}

	sm["print"] = &Symbol{Name: "print", Kind: Builtin, Index: UnsetAddr}
	sm["println"] = &Symbol{Name: "println", Kind: Builtin, Index: UnsetAddr}
	sm["panic"] = &Symbol{Name: "panic", Kind: Builtin, Index: UnsetAddr}
	sm["recover"] = &Symbol{Name: "recover", Kind: Builtin, Index: UnsetAddr}
	sm["len"] = &Symbol{Name: "len", Kind: Builtin, Index: UnsetAddr}
	sm["cap"] = &Symbol{Name: "cap", Kind: Builtin, Index: UnsetAddr}
	sm["append"] = &Symbol{Name: "append", Kind: Builtin, Index: UnsetAddr}
	sm["copy"] = &Symbol{Name: "copy", Kind: Builtin, Index: UnsetAddr}
	sm["delete"] = &Symbol{Name: "delete", Kind: Builtin, Index: UnsetAddr}
	sm["new"] = &Symbol{Name: "new", Kind: Builtin, Index: UnsetAddr}
	sm["make"] = &Symbol{Name: "make", Kind: Builtin, Index: UnsetAddr}
	sm["close"] = &Symbol{Name: "close", Kind: Builtin, Index: UnsetAddr}
	sm["min"] = &Symbol{Name: "min", Kind: Builtin, Index: UnsetAddr}
	sm["max"] = &Symbol{Name: "max", Kind: Builtin, Index: UnsetAddr}
	sm["trap"] = &Symbol{Name: "trap", Kind: Builtin, Index: UnsetAddr}
}
