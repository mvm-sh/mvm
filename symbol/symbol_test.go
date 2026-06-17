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

func TestMethodByNameIndexMatchesFullScan(t *testing.T) {
	rt := reflect.TypeOf(struct{ X int }{})
	innerType := &vm.Type{Name: "Tag", PkgName: "inner", Rtype: rt}
	enum := &vm.Type{Name: "Enum", PkgName: "filedesc", Rtype: reflect.TypeOf(struct{ a int }{})}
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

func TestMethodLookupCrossUniverse(t *testing.T) {
	const pkg = "google.golang.org/protobuf/internal/filedesc"
	for round := 0; round < 200; round++ {
		// regType owns the methods; recvVal is a same-Go-type instance from
		// another compile universe (distinct *Type and distinct rtype).
		regType := &vm.Type{Name: "Enum", PkgName: "filedesc", Rtype: reflect.TypeOf(struct{ a int }{})}
		recvVal := &vm.Type{Name: "Enum", PkgName: "filedesc", Rtype: reflect.TypeOf(struct{ b int }{})}

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

func TestMethodLookupDistinctPkgSameShortName(t *testing.T) {
	const grpcPkg = "google.golang.org/grpc/internal/status"
	const genPkg = "google.golang.org/genproto/googleapis/rpc/status"
	for round := 0; round < 200; round++ {
		// grpcStatus owns Details(); protoStatus is a different type, no Details.
		grpcStatus := &vm.Type{Name: "Status", PkgName: "status", ImportPath: grpcPkg, Rtype: reflect.TypeOf(struct{ a int }{})}
		protoStatus := &vm.Type{Name: "Status", PkgName: "status", ImportPath: genPkg, Rtype: reflect.TypeOf(struct{ b int }{})}

		sm := SymMap{
			grpcPkg + ".Status":               &Symbol{Kind: Type, Type: grpcStatus},
			genPkg + ".Status":                &Symbol{Kind: Type, Type: protoStatus},
			"Status":                          &Symbol{Kind: Type, Type: grpcStatus},
			"*" + grpcPkg + ".Status.Details": &Symbol{Kind: Func, Name: "Details", Index: 100},
		}
		seg := segOf(sm)

		recv := &Symbol{Kind: Value, Type: vm.SymPtr(protoStatus)}
		if got, _ := sm.MethodByName(recv, "Details", seg); got != nil {
			t.Fatalf("round %d: proto Status hijacked grpc Status.Details (got Index=%d)", round, got.Index)
		}
		// The grpc Status itself still resolves its own method.
		recvG := &Symbol{Kind: Value, Type: vm.SymPtr(grpcStatus)}
		if got, _ := sm.MethodByName(recvG, "Details", seg); got == nil || got.Index != 100 {
			t.Fatalf("round %d: grpc Status.Details unresolved: got %v", round, got)
		}
	}
	// Cross-universe dup (same ImportPath, distinct *Type+rtype): must resolve.
	for round := 0; round < 50; round++ {
		const pkg = "example.com/dup"
		reg := &vm.Type{Name: "T", PkgName: "dup", ImportPath: pkg, Rtype: reflect.TypeOf(struct{ a int }{})}
		recvT := &vm.Type{Name: "T", PkgName: "dup", ImportPath: pkg, Rtype: reflect.TypeOf(struct{ b int }{})}
		sm := SymMap{
			pkg + ".T":         &Symbol{Kind: Type, Type: reg},
			"*" + pkg + ".T.M": &Symbol{Kind: Func, Name: "M", Index: 7},
		}
		seg := segOf(sm)
		recv := &Symbol{Kind: Value, Type: vm.SymPtr(recvT)}
		if got, _ := sm.MethodByName(recv, "M", seg); got == nil || got.Index != 7 {
			t.Fatalf("round %d: cross-universe dup (same ImportPath) failed to resolve: got %v", round, got)
		}
	}
	// Main/REPL dup (no ImportPath): the lenient short-PkgPath fallback resolves it.
	for round := 0; round < 50; round++ {
		reg := &vm.Type{Name: "M", PkgName: "main", Rtype: reflect.TypeOf(struct{ a int }{})}
		recvM := &vm.Type{Name: "M", PkgName: "main", Rtype: reflect.TypeOf(struct{ b int }{})}
		sm := SymMap{
			"main.M":      &Symbol{Kind: Type, Type: reg},
			"*main.M.Inc": &Symbol{Kind: Func, Name: "Inc", Index: 9},
		}
		seg := segOf(sm)
		recv := &Symbol{Kind: Value, Type: vm.SymPtr(recvM)}
		if got, _ := sm.MethodByName(recv, "Inc", seg); got == nil || got.Index != 9 {
			t.Fatalf("round %d: main-pkg dup (no ImportPath) failed to resolve: got %v", round, got)
		}
	}
}

func TestPromotedMethodViaCloneCanonical(t *testing.T) {
	const pkg = "golang.org/x/net/http2"

	// FrameHeader owns the unexported promoted method `invalidate` (ptr recv),
	// registered at a pkg-qualified key as for any imported package.
	fhType := &vm.Type{Name: "FrameHeader", PkgName: "http2", Rtype: reflect.TypeOf(struct{ valid bool }{})}
	// canonHF carries the embedded FrameHeader; cloneHF is the field-access clone
	// with no Embedded and Rtype nil, linked to canonHF via Base.
	canonHF := &vm.Type{
		Name: "HeadersFrame", PkgName: "http2",
		Rtype:    reflect.TypeOf(struct{ a int }{}),
		Embedded: []vm.EmbeddedField{{FieldIdx: 0, Type: fhType}},
	}
	cloneHF := &vm.Type{Name: "HeadersFrame", PkgName: "http2", Base: canonHF}

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
