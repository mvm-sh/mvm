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
	InNames    []string       // raw input param names, cached from Phase 1 for Phase 2
	OutNames   []string       // raw output param names, cached from Phase 1 for Phase 2
	MethodExpr bool           // true if this is a method expression (Type.Method)
	Composite  bool           // true if this symbol is a composite literal value (T{})
	// FieldOffset and HasFieldOffset are set when this symbol is produced by a
	// struct field selector chain; FieldOffset is the accumulated byte offset
	// from the outermost operand to this field. Used by unsafe.Offsetof.
	HasFieldOffset bool
	FieldOffset    uintptr
	Data           any // optional extra data (e.g. generic template)
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
		if m := sm[sym.Name+"."+name]; m != nil {
			return m, nil
		}
		// Pointer type: also try value-receiver methods (*T includes T's method set).
		if strings.HasPrefix(sym.Name, "*") {
			if m := sm[sym.Name[1:]+"."+name]; m != nil {
				return m, nil
			}
		}
		return sm.promotedMethod(sym.Type, name, nil)
	case Var, LocalVar, Value:
		if sym.Type == nil {
			return nil, nil
		}
		typName := sym.Type.Name
		// For types with no Name (e.g. mvm-created structs, or pointer types),
		// search the symbol table for a named Type with a matching Rtype.
		if typName == "" {
			rtype := sym.Type.Rtype
			if rtype.Kind() == reflect.Pointer {
				rtype = rtype.Elem()
			}
			for k, s := range sm {
				if s.Kind == Type && s.Type != nil && s.Type.Rtype == rtype && k != "" {
					typName = k
					break
				}
			}
		}
		if m := sm[typName+"."+name]; m != nil {
			return m, nil
		}
		if m := sm["*"+typName+"."+name]; m != nil {
			return m, nil
		}
		return sm.promotedMethod(sym.Type, name, nil)
	}
	return nil, nil
}

// promotedMethod searches for a method promoted through embedded fields recorded in typ.Embedded.
// It returns the method symbol and the field index path to reach the embedded receiver.
func (sm SymMap) promotedMethod(typ *vm.Type, name string, path []int) (*Symbol, []int) {
	if typ == nil {
		return nil, nil
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
