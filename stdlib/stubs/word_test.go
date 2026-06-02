package stubs

import (
	"reflect"
	"runtime"
	"testing"
	"unsafe"
)

// These tests call a word-shaped synth method through a real native interface,
// so they exercise the generated stub ABI (scatter/gather + register layout) on
// the running arch. Run under GOARCH=amd64 as well for cross-arch coverage.

type sizer interface{ Size() int64 }

// TestWordShapeIntResult routes an int64 through the "_i" result word.
func TestWordShapeIntResult(t *testing.T) {
	const want = int64(0x1122334455667788)
	core := func(_ unsafe.Pointer, _ []unsafe.Pointer, _ []uint64, _ []unsafe.Pointer, rsw []uint64) {
		rsw[0] = uint64(want)
	}
	rt, err := mkSynth(reflect.TypeOf(int(0)), "SizT", "test", []Method{{
		Name: "Size", Exported: true,
		Sig:     reflect.TypeOf((func() int64)(nil)),
		WordKey: "_i", Core: core,
	}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	s, ok := reflect.New(rt).Elem().Interface().(sizer)
	if !ok {
		t.Fatal("synth type does not satisfy sizer")
	}
	if got := s.Size(); got != want {
		t.Errorf("Size() = %#x, want %#x", got, want)
	}
}

type opener interface {
	Open(string) (any, error)
}

// TestWordShapeStringParamIfaceResult routes a string param in (data+len words)
// and an interface result out (type+data words) through "pi_pppp", then forces a
// GC to surface a mis-typed (unscanned) pointer word.
func TestWordShapeStringParamIfaceResult(t *testing.T) {
	core := func(_ unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, rpw []unsafe.Pointer, _ []uint64) {
		in := unsafe.String((*byte)(pw[0]), int(sw[0]))
		var boxed any = "got:" + in // fresh heap allocation
		w := *(*[2]unsafe.Pointer)(unsafe.Pointer(&boxed))
		rpw[0], rpw[1] = w[0], w[1] // result 0 (any); result 1 (error) stays nil
	}
	rt, err := mkSynth(reflect.TypeOf(int(0)), "OpenT", "test", []Method{{
		Name: "Open", Exported: true,
		Sig:     reflect.TypeOf((func(string) (any, error))(nil)),
		WordKey: "pi_pppp", Core: core,
	}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	o, ok := reflect.New(rt).Elem().Interface().(opener)
	if !ok {
		t.Fatal("synth type does not satisfy opener")
	}
	got, gotErr := o.Open("hello")
	if gotErr != nil {
		t.Fatalf("Open err = %v", gotErr)
	}
	// Churn the heap and GC: the result's data word must have been carried in a
	// pointer-typed slot, else this reads freed memory.
	for range 4 {
		_ = make([]byte, 1<<16)
	}
	runtime.GC()
	if s, ok := got.(string); !ok || s != "got:hello" {
		t.Errorf("Open(\"hello\") = %v (%T), want \"got:hello\"", got, got)
	}
}

// triple is a word-sized-leaf struct (like time.Time): three 8-byte fields, the
// last a pointer. It flattens to word-shape "_iip".
type triple struct {
	a uint64
	b int64
	c *int
}

type tripler interface{ M() triple }

// TestWordShapeStructResult routes a word-sized-leaf struct out through the
// "_iip" result words (two integers + a pointer), then GCs to confirm the
// pointer field travelled in a scanned slot.
func TestWordShapeStructResult(t *testing.T) {
	target := 99
	want := triple{a: 0xAABBCCDD, b: -42, c: &target}
	core := func(_ unsafe.Pointer, _ []unsafe.Pointer, _ []uint64, rpw []unsafe.Pointer, rsw []uint64) {
		rsw[0] = want.a
		rsw[1] = uint64(want.b)
		rpw[0] = unsafe.Pointer(want.c)
	}
	rt, err := mkSynth(reflect.TypeOf(int(0)), "TripT", "test", []Method{{
		Name: "M", Exported: true,
		Sig:     reflect.TypeOf((func() triple)(nil)),
		WordKey: "_iip", Core: core,
	}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	tr, ok := reflect.New(rt).Elem().Interface().(tripler)
	if !ok {
		t.Fatal("synth type does not satisfy tripler")
	}
	got := tr.M()
	for range 4 {
		_ = make([]byte, 1<<16)
	}
	runtime.GC()
	if got.a != want.a || got.b != want.b || got.c == nil || *got.c != target {
		t.Errorf("M() = %+v (*c=%v), want a=%#x b=%d *c=%d", got, got.c, want.a, want.b, target)
	}
}
