package runtype

import (
	"reflect"
	"testing"
)

// A zero StubPC must wire the method's Ifn/Tfn to the -1 unreachable sentinel,
// not register a code PC in reflect's GC-scanned reflectOffs table (the wasm GC
// hazard). Reflect introspection (NumMethod/MethodByName/Implements) reads only
// the name and Mtyp, so it stays correct with the sentinel.
func TestZeroStubPCUsesUnreachableSentinel(t *testing.T) {
	if got := textOffForPC(0); got != unreachableTextOff {
		t.Fatalf("textOffForPC(0) = %#x, want %#x", got, unreachableTextOff)
	}

	r, err := ReserveMethods(reflect.TypeOf(int(0)), "Tagged", "pkg")
	if err != nil {
		t.Fatal(err)
	}
	rt := r.Type()
	if err := r.Fill([]MethodSpec{{
		Name: "String", Exported: true,
		Sig:    reflect.TypeOf(func() string { return "" }),
		StubPC: 0, // never invoked: introspection only
	}}); err != nil {
		t.Fatal(err)
	}

	if got := rt.NumMethod(); got != 1 {
		t.Fatalf("NumMethod = %d, want 1", got)
	}
	m, ok := rt.MethodByName("String")
	if !ok {
		t.Fatal("MethodByName(String) not found")
	}
	if m.Type.NumOut() != 1 || m.Type.Out(0).Kind() != reflect.String {
		t.Fatalf("method signature = %v, want func() string", m.Type)
	}
	if !rt.Implements(reflect.TypeOf((*stringerLike)(nil)).Elem()) {
		t.Fatal("sentinel-Tfn type does not implement stringerLike")
	}
}
