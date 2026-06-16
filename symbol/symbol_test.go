package symbol

import (
	"reflect"
	"slices"
	"testing"

	"github.com/mvm-sh/mvm/vm"
)

// segOf builds a SegIndex over sm, as the Parser keeps in sync in production.
func segOf(sm SymMap) SegIndex {
	seg := SegIndex{}
	for k := range sm {
		seg.Add(k)
	}
	return seg
}

func TestSegIndexAddDel(t *testing.T) {
	idx := SegIndex{}
	idx.Add("pkg.T")
	idx.Add("pkg.T") // idempotent
	idx.Add("*pkg.T")
	idx.Add("T")
	if got := len(idx["T"]); got != 3 {
		t.Fatalf("bucket T: got %d keys, want 3 (pkg.T,*pkg.T,T)", got)
	}
	idx.Del("*pkg.T")
	if got := len(idx["T"]); got != 2 {
		t.Fatalf("after Del: got %d keys, want 2", got)
	}
	if slices.Contains(idx["T"], "*pkg.T") {
		t.Fatal("Del left *pkg.T in the bucket")
	}
	if LastSeg("a.b.C") != "C" || LastSeg("bare") != "bare" {
		t.Fatal("LastSeg")
	}
}

// TestMethodByNameIndexMatchesFullScan asserts the indexed path resolves
// identically to the unindexed full scan -- the invariant the speedup rests on.
func TestMethodByNameIndexMatchesFullScan(t *testing.T) {
	rt := reflect.TypeOf(struct{ X int }{})
	innerType := &vm.Type{Name: "Tag", PkgPath: "inner", Rtype: rt}
	enum := &vm.Type{Name: "Enum", PkgPath: "filedesc", Rtype: reflect.TypeOf(struct{ a int }{})}
	sm := SymMap{
		"inner.Tag":             &Symbol{Kind: Type, Type: innerType},
		"inner.Tag.IsRoot":      &Symbol{Kind: Func, Name: "IsRoot", Index: 1},
		"filedesc.Enum":         &Symbol{Kind: Type, Type: enum},
		"*filedesc.Enum.Number": &Symbol{Kind: Func, Name: "Number", Index: 2},
		"local":                 &Symbol{Kind: Type, Name: "local", Type: &vm.Type{Name: "local"}},
		"local.Hi":              &Symbol{Kind: Func, Name: "Hi", Index: 3},
	}
	seg := segOf(sm)
	cases := []struct {
		recv   *Symbol
		method string
	}{
		{&Symbol{Kind: Value, Type: innerType}, "IsRoot"},
		{&Symbol{Kind: Value, Type: vm.SymPtr(enum)}, "Number"},
		{&Symbol{Kind: Value, Type: enum}, "Number"},
		{&Symbol{Kind: Type, Name: "local", Type: sm["local"].Type}, "Hi"},
		{&Symbol{Kind: Value, Type: innerType}, "Missing"},
	}
	for _, c := range cases {
		want, _ := sm.MethodByName(c.recv, c.method, nil) // full scan
		got, _ := sm.MethodByName(c.recv, c.method, seg)  // indexed
		if got != want {
			t.Fatalf("%s.%s: indexed=%v full-scan=%v (must match)", c.recv.Type.Name, c.method, got, want)
		}
	}
}

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
		seg := segOf(sm)
		recv := &Symbol{Kind: Value, Type: innerType}
		got, _ := sm.MethodByName(recv, "IsRoot", seg)
		if got == nil {
			t.Fatalf("round %d: MethodByName returned nil for inner receiver", round)
		}
		if got.Index != 100 {
			t.Fatalf("round %d: inner receiver dispatched to wrong method: got Index=%d, want 100 (outer's was 200)", round, got.Index)
		}

		// Symmetric: outer receiver must resolve to outerMethod.
		recv2 := &Symbol{Kind: Value, Type: outerType}
		got2, _ := sm.MethodByName(recv2, "IsRoot", seg)
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
		seg := segOf(sm)
		recv := &Symbol{Kind: Value, Type: recvVal}
		got, _ := sm.MethodByName(recv, "unmarshalSeed", seg)
		if got == nil || got.Index != 100 {
			t.Fatalf("round %d: value receiver: got %v, want method Index=100", round, got)
		}

		// Pointer receiver: d := &p.List[i]; d.Number().
		recvPtr := &Symbol{Kind: Value, Type: vm.SymPtr(recvVal)}
		got2, _ := sm.MethodByName(recvPtr, "Number", seg)
		if got2 == nil || got2.Index != 200 {
			t.Fatalf("round %d: pointer receiver: got %v, want method Index=200", round, got2)
		}
	}
}

// TestPromotedMethodViaCloneCanonical guards the x/net/http2 "undefined:
// invalidate" blocker: a field-access clone *Type drops Embedded, so an
// unexported promoted method (resolvable only via the symbol table, since
// reflect hides unexported methods) must resolve via the canonical (Base).
func TestPromotedMethodViaCloneCanonical(t *testing.T) {
	const pkg = "golang.org/x/net/http2"

	// FrameHeader owns the unexported promoted method `invalidate` (ptr recv),
	// registered at a pkg-qualified key as for any imported package.
	fhType := &vm.Type{Name: "FrameHeader", PkgPath: "http2", Rtype: reflect.TypeOf(struct{ valid bool }{})}
	// canonHF carries the embedded FrameHeader; cloneHF is the field-access clone
	// with no Embedded and Rtype nil, linked to canonHF via Base.
	canonHF := &vm.Type{
		Name: "HeadersFrame", PkgPath: "http2",
		Rtype:    reflect.TypeOf(struct{ a int }{}),
		Embedded: []vm.EmbeddedField{{FieldIdx: 0, Type: fhType}},
	}
	cloneHF := &vm.Type{Name: "HeadersFrame", PkgPath: "http2", Base: canonHF}

	invalidate := &Symbol{Kind: Func, Name: "invalidate", Index: 42}
	sm := SymMap{
		pkg + ".FrameHeader":                  &Symbol{Kind: Type, Type: fhType},
		"*" + pkg + ".FrameHeader.invalidate": invalidate,
		pkg + ".HeadersFrame":                 &Symbol{Kind: Type, Type: canonHF},
		"HeadersFrame":                        &Symbol{Kind: Type, Type: canonHF},
	}
	seg := segOf(sm)

	// mh.HeadersFrame.invalidate(): receiver is *HeadersFrame (the clone).
	recv := &Symbol{Kind: Value, Type: vm.SymPtr(cloneHF)}
	got, path := sm.MethodByName(recv, "invalidate", seg)
	if got == nil || got.Index != 42 {
		t.Fatalf("promoted unexported method via clone: got %v, want method Index=42", got)
	}
	if len(path) != 1 || path[0] != 0 {
		t.Fatalf("promoted field path: got %v, want [0]", path)
	}
}
