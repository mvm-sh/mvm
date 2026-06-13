package symbol

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/vm"
)

// TestQualifiedMethodLookupPrefersExactType reproduces the
// [[project_isroot_iface_dispatch_recursion]] flake: two distinct *vm.Type
// values share the same reflect.Type (the `type Tag compact.Tag` shape in
// x/text/language). With the old rtype-only matcher, qualifiedMethodLookup
// would pick whichever same-rtype Type Symbol Go's map iteration visited
// first; ~half the runs the wrong method codeAddr was wired in and the call
// site recursed back into itself.
//
// This test exercises 1000 iterations against a fresh SymMap each round so
// the map's internal bucket layout (which seeds map-iter randomness) is
// different every time, and checks that MethodByName always returns the
// method belonging to the receiver's Type identity, never the sibling's.
func TestQualifiedMethodLookupPrefersExactType(t *testing.T) {
	rt := reflect.TypeOf(struct{ X int }{})

	for round := 0; round < 1000; round++ {
		// Two distinct *vm.Type values sharing the same Rtype, modeling
		// compact.Tag (inner) and language.Tag (outer alias) in x/text.
		innerType := &vm.Type{Name: "Tag", Rtype: rt}
		outerType := &vm.Type{Name: "Tag", Rtype: rt}

		innerMethod := &Symbol{Kind: Func, Name: "IsRoot", Index: 100}
		outerMethod := &Symbol{Kind: Func, Name: "IsRoot", Index: 200}

		sm := SymMap{
			"example.com/inner.Tag":        &Symbol{Kind: Type, Type: innerType},
			"example.com/inner.Tag.IsRoot": innerMethod,
			"example.com/outer.Tag":        &Symbol{Kind: Type, Type: outerType},
			"example.com/outer.Tag.IsRoot": outerMethod,
		}

		// Receiver carrying innerType: must resolve to innerMethod (Index=100),
		// never outerMethod (Index=200).
		recv := &Symbol{Kind: Value, Type: innerType}
		got, _ := sm.MethodByName(recv, "IsRoot")
		if got == nil {
			t.Fatalf("round %d: MethodByName returned nil for inner receiver", round)
		}
		if got.Index != 100 {
			t.Fatalf("round %d: inner receiver dispatched to wrong method: got Index=%d, want 100 (outer's was 200)", round, got.Index)
		}

		// Symmetric: outer receiver must resolve to outerMethod.
		recv2 := &Symbol{Kind: Value, Type: outerType}
		got2, _ := sm.MethodByName(recv2, "IsRoot")
		if got2 == nil {
			t.Fatalf("round %d: MethodByName returned nil for outer receiver", round)
		}
		if got2.Index != 200 {
			t.Fatalf("round %d: outer receiver dispatched to wrong method: got Index=%d, want 200", round, got2.Index)
		}
	}
}

// TestMethodLookupCrossUniverse models the duplicated-type case seen compiling
// google.golang.org/protobuf/internal/filedesc: mvm can build distinct *vm.Type
// instances (and rtypes) for one Go type across file-by-file / multi-pass
// compilation. The Enum reached through a slice-element field was a different
// *Type than the registered Enum owning the pkg-qualified method keys, so a
// method call resolved by *Type / canonical / rtype identity reported
// "undefined: <method>". MethodByName now falls back to package-path + name,
// which uniquely identifies a Go type, for both value and pointer receivers.
func TestMethodLookupCrossUniverse(t *testing.T) {
	const pkg = "google.golang.org/protobuf/internal/filedesc"
	for round := 0; round < 200; round++ {
		// regType owns the methods; recvVal is a same-Go-type instance from
		// another compile universe (distinct *Type and distinct rtype).
		regType := &vm.Type{Name: "Enum", PkgPath: "filedesc", Rtype: reflect.TypeOf(struct{ a int }{})}
		recvVal := &vm.Type{Name: "Enum", PkgPath: "filedesc", Rtype: reflect.TypeOf(struct{ b int }{})}

		valMethod := &Symbol{Kind: Func, Name: "unmarshalSeed", Index: 100}
		ptrMethod := &Symbol{Kind: Func, Name: "Number", Index: 200}

		sm := SymMap{
			pkg + ".Enum":                     &Symbol{Kind: Type, Type: regType},
			"Enum":                            &Symbol{Kind: Type, Type: regType},
			"*" + pkg + ".Enum.unmarshalSeed": valMethod,
			"*" + pkg + ".Enum.Number":        ptrMethod,
		}

		// Value receiver: fd...Enums.List[i].unmarshalSeed(...).
		recv := &Symbol{Kind: Value, Type: recvVal}
		got, _ := sm.MethodByName(recv, "unmarshalSeed")
		if got == nil || got.Index != 100 {
			t.Fatalf("round %d: value receiver: got %v, want method Index=100", round, got)
		}

		// Pointer receiver: d := &p.List[i]; d.Number().
		recvPtr := &Symbol{Kind: Value, Type: vm.SymPtr(recvVal)}
		got2, _ := sm.MethodByName(recvPtr, "Number")
		if got2 == nil || got2.Index != 200 {
			t.Fatalf("round %d: pointer receiver: got %v, want method Index=200", round, got2)
		}
	}
}
