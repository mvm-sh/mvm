package mtype

import (
	"reflect"
	"testing"
)

// erasedIface is the unnamed empty interface a forward-ref component erases to.
func erasedIface() *Type {
	return &Type{kind: reflect.Interface, Rtype: AnyRtype}
}

// namedIface builds a named interface with a distinct rtype (not Identical to erasedIface).
func namedIface(name string) *Type {
	return &Type{
		kind:         reflect.Interface,
		Name:         name,
		PkgPath:      "google.golang.org/protobuf/reflect/protoreflect",
		Rtype:        reflect.TypeFor[interface{ marker() }](),
		IfaceMethods: []IfaceMethod{{Name: "marker", ID: -1}},
	}
}

func TestCompatibleTypeErasedInterface(t *testing.T) {
	empty := erasedIface()
	named := namedIface("MessageType")
	if empty.Identical(named) {
		t.Fatal("precondition: empty interface should not be Identical to a named interface")
	}
	if !compatibleType(empty, named) {
		t.Error("erased empty interface should match a named interface (want side erased)")
	}
	if !compatibleType(named, empty) {
		t.Error("erased empty interface should match a named interface (have side erased)")
	}
	// An erased interface must NOT match a non-interface.
	structT := &Type{kind: reflect.Struct, Name: "S", PkgPath: "p", Rtype: reflect.TypeFor[struct{ A int }]()}
	if compatibleType(empty, structT) {
		t.Error("erased empty interface must not match a concrete struct")
	}
}

// TestCompatibleTypeCrossUniverseDup mirrors protoiface.Methods: a named struct
// with a named func field, materialized twice with distinct rtypes.
// Identical rejects it (baseRoot differs); the signature check accepts it.
func TestCompatibleTypeCrossUniverseDup(t *testing.T) {
	intT := func() *Type { return &Type{kind: reflect.Int, Rtype: reflect.TypeFor[int]()} }
	field := func(funcRt reflect.Type) *Type {
		return &Type{
			kind:    reflect.Func,
			Name:    "Unmarshal", // struct field name carried on the field *Type
			Rtype:   funcRt,
			Params:  []*Type{intT()},
			Returns: []*Type{intT()},
		}
	}
	methods := func(structRt, funcRt reflect.Type) *Type {
		return &Type{
			kind:    reflect.Struct,
			Name:    "Methods",
			PkgPath: "google.golang.org/protobuf/runtime/protoiface",
			Rtype:   structRt,
			Fields:  []*Type{field(funcRt)},
		}
	}
	a := methods(reflect.TypeFor[struct{ X int }](), reflect.TypeFor[func(int8)]())
	b := methods(reflect.TypeFor[struct{ Y int }](), reflect.TypeFor[func(int16)]())
	if a.Identical(b) {
		t.Fatal("precondition: cross-universe dup should not be Identical")
	}
	if !compatibleType(a, b) {
		t.Error("same-named struct with same-named func field should be compatible across universes")
	}
	// A genuine shape difference must still be rejected.
	c := &Type{kind: reflect.Struct, Name: "Methods", PkgPath: "google.golang.org/protobuf/runtime/protoiface",
		Rtype: reflect.TypeFor[struct{ Z int }](), Fields: []*Type{{kind: reflect.Int, Rtype: reflect.TypeFor[int]()}}}
	if compatibleType(a, c) {
		t.Error("struct whose field is int, not the func, must not be compatible")
	}
}

func TestCompatibleTypeNegatives(t *testing.T) {
	errT := &Type{kind: reflect.Interface, Name: "error", Rtype: reflect.TypeFor[error]()}
	sliceErr := &Type{kind: reflect.Slice, ElemType: errT, Rtype: reflect.TypeFor[[]error]()}
	// Unwrap() []error must not satisfy interface{ Unwrap() error }.
	if compatibleType(sliceErr, errT) {
		t.Error("[]error must not be compatible with error")
	}
	reader := &Type{kind: reflect.Interface, Name: "Reader", PkgPath: "io", Rtype: reflect.TypeFor[interface{ r() }]()}
	writer := &Type{kind: reflect.Interface, Name: "Writer", PkgPath: "io", Rtype: reflect.TypeFor[interface{ w() }]()}
	if compatibleType(reader, writer) {
		t.Error("differently-named interfaces must not be compatible")
	}
}

// TestSigTypeCompatibleErasedReturn is the protoreflect.Message.Type() MessageType
// shape: the interface sig erases the return to interface{}, the concrete keeps it.
func TestSigTypeCompatibleErasedReturn(t *testing.T) {
	imSig := &Type{kind: reflect.Func, Returns: []*Type{erasedIface()}}
	mSig := &Type{kind: reflect.Func, Returns: []*Type{namedIface("MessageType")}}
	if !sigTypeCompatible(imSig, mSig) {
		t.Error("func() interface{} (erased) should match func() MessageType")
	}
}
