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
