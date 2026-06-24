//go:build !wasm

// These tests pin the register-ABI word path: classifyType/wordLayoutOf/
// writeWords/readWords and classifyWordSig (which resolves to the register
// classifier only when wordABI0 is false). The wasm/ABI0 path is covered by
// synth_word_abi0_test.go (arch-independent), and its end-to-end dispatch by
// interptest under a wasm runtime.

package vm

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestClassifyType(t *testing.T) {
	cases := []struct {
		rt      reflect.Type
		classes string
		ok      bool
	}{
		{reflect.TypeOf(int(0)), "i", true},
		{reflect.TypeOf(int32(0)), "i", true},
		{reflect.TypeOf(uint8(0)), "i", true},
		{reflect.TypeOf(false), "i", true},
		{reflect.TypeOf(uintptr(0)), "i", true},
		{reflect.TypeOf((*int)(nil)), "p", true},
		{reflect.TypeOf((chan int)(nil)), "p", true},
		{reflect.TypeOf((map[int]int)(nil)), "p", true},
		{reflect.TypeOf((func())(nil)), "p", true},
		{reflect.TypeOf(""), "pi", true},
		{reflect.TypeOf([]int(nil)), "pii", true},
		{reflect.TypeOf((*any)(nil)).Elem(), "pp", true},
		{reflect.TypeOf((*error)(nil)).Elem(), "pp", true},
		{reflect.TypeOf(float64(0)), "f", true},                // float64 is an FP-register word
		{reflect.TypeOf(complex128(0)), "ff", true},            // two float64 in two FP registers
		{reflect.TypeOf(float32(0)), "", false},                // float32 is sub-word, dropped
		{reflect.TypeOf(complex64(0)), "", false},              // complex64 (two float32) dropped
		{reflect.TypeOf([2]int{}), "", false},                  // arrays of len > 1 are stack-passed
		{reflect.TypeOf(struct{ X, Y float64 }{}), "ff", true}, // all-float struct
		// word-sized-leaf structs flatten to their leaves' words.
		{reflect.TypeOf(struct{ a, b int }{}), "ii", true},
		{reflect.TypeOf(struct {
			s string
			n int
		}{}), "pii", true},
		{reflect.TypeOf(time.Time{}), "iip", true}, // {wall uint64; ext int64; loc *Location}
		// sub-word-packed and padded leaves each take one register word.
		{reflect.TypeOf(struct{ a, b uint32 }{}), "ii", true}, // fixed.Point26_6-like
		{reflect.TypeOf(struct {
			a bool
			b *int
		}{}), "ip", true}, // bool@0, ptr@8 (padding between)
	}
	for _, tc := range cases {
		c, ok := classifyType(tc.rt)
		if c != tc.classes || ok != tc.ok {
			t.Errorf("classifyType(%v) = %q,%v want %q,%v", tc.rt, c, ok, tc.classes, tc.ok)
		}
	}
}

func TestDetectWordShape(t *testing.T) {
	cases := []struct {
		sig reflect.Type
		key string
		ok  bool
	}{
		{reflect.TypeOf((func() int64)(nil)), "_i", true},
		{reflect.TypeOf((func() any)(nil)), "_pp", true},
		{reflect.TypeOf((func(string) (any, error))(nil)), "pi_pppp", true},
		{reflect.TypeOf((func(string) ([]int, error))(nil)), "pi_piipp", true},
		{reflect.TypeOf((func() time.Time)(nil)), "_iip", true}, // word-sized-leaf struct result
		// no generated pool for this word-shape -> drop (not error).
		{reflect.TypeOf((func() (int, int, int))(nil)), "", false},
		// a sub-word-packed struct param flattens to its leaves' words (ii) -> "ii_i".
		{reflect.TypeOf((func(struct{ X, Y int32 }) int32)(nil)), "ii_i", true},
		// over the register-word budget -> drop.
		{reflect.TypeOf((func(*int, *int, *int, *int, *int, *int, *int))(nil)), "", false},
		{nil, "", false},
	}
	for _, tc := range cases {
		key, ok := detectWordShape(tc.sig)
		if key != tc.key || ok != tc.ok {
			t.Errorf("detectWordShape(%v) = %q,%v want %q,%v", tc.sig, key, ok, tc.key, tc.ok)
		}
	}
}

// TestWordMarshalRoundTrip verifies readWords and writeWords are inverse for
// every classifiable type: a value's register words, read out and written back
// into a fresh allocation, reproduce the value. This pins the word<->memory
// mapping (the core marshaling) independent of the call ABI, including sub-word
// packing and inter-field padding; run under GOARCH=amd64 too for cross-arch.
func TestWordMarshalRoundTrip(t *testing.T) {
	n := 7
	values := []any{
		int64(-123456789),
		uint32(0xdeadbeef),
		true,
		"hello, word-class",
		[]int{1, 2, 3},
		&n,
		reflect.TypeOf(0), // a non-nil interface value
		errors.New("boom"),
		time.Date(2026, 6, 2, 10, 30, 0, 0, time.UTC), // word-sized-leaf struct
		float64(3.14159265358979),                     // 'f' word
		complex128(complex(1.5, -2.5)),                // two float64 halves -> "ff"
		struct{ X, Y float64 }{1.5, -2.5},             // all-float struct -> "ff"
		struct { // mixed int/float/ptr -> "ifp"
			A int64
			B float64
			C *int
		}{42, 6.022e23, &n},
		struct{ X, Y int32 }{7, -9}, // sub-word packed (two int32 in one word) -> "ii"
		struct { // sub-word + padding before the pointer -> "iip"
			A int16
			B int64
			C *int
		}{-5, 1 << 40, &n},
		struct { // bool@0, ptr@8 (padding between) -> "ip"
			Flag bool
			P    *int
		}{true, &n},
	}
	for _, v := range values {
		rt := reflect.TypeOf(v)
		lay, ok := wordLayoutOf(rt)
		if !ok {
			t.Fatalf("wordLayoutOf(%v) not ok", rt)
		}
		pw := make([]unsafe.Pointer, strings.Count(lay.classes, "p"))
		sw := make([]uint64, strings.Count(lay.classes, "i"))
		fw := make([]float64, strings.Count(lay.classes, "f"))

		src := reflect.New(rt)
		src.Elem().Set(reflect.ValueOf(v))
		readWords(lay, src.UnsafePointer(), pw, sw, fw, 0, 0, 0)

		dst := reflect.New(rt)
		writeWords(lay, dst.UnsafePointer(), pw, sw, fw, 0, 0, 0)

		if !reflect.DeepEqual(dst.Elem().Interface(), v) {
			t.Errorf("round-trip %v: got %v want %v", rt, dst.Elem().Interface(), v)
		}
	}
}

// TestSetResultValueNilInterface checks the reflectToError-style nil handling:
// a nil concrete reference assigned to an interface slot stays a nil interface,
// not a boxed typed-nil.
func TestSetResultValueNilInterface(t *testing.T) {
	dst := reflect.New(reflect.TypeOf((*error)(nil)).Elem()).Elem()
	setResultValue(dst, reflect.ValueOf((*myErr)(nil)))
	if got := dst.Interface(); got != nil {
		t.Errorf("nil *myErr into error = %v (%T), want nil interface", got, got)
	}
}

type myErr struct{}

func (*myErr) Error() string { return "myErr" }

// TestABI0MatchesRegabiExceptPacked checks the wasm/ABI0 classifier against the
// register-ABI classifier: every word-aligned signature must yield the same key
// (so both arches share the generated pools), while a sub-word-packed aggregate
// must differ (the ABI0 key packs leaves into 8-byte slots).
func TestABI0MatchesRegabiExceptPacked(t *testing.T) {
	tp := reflect.TypeOf
	errT := tp((*error)(nil)).Elem()
	same := []reflect.Type{
		reflect.FuncOf(nil, []reflect.Type{tp("")}, false),                                  // func() string
		reflect.FuncOf([]reflect.Type{tp([]byte(nil))}, []reflect.Type{tp(0), errT}, false), // Reader
		reflect.FuncOf(nil, []reflect.Type{tp(time.Time{})}, false),                         // time.Time (all 8-byte)
		reflect.FuncOf([]reflect.Type{tp(float64(0))}, []reflect.Type{tp(float64(0))}, false),
		reflect.FuncOf([]reflect.Type{errT}, []reflect.Type{tp(true)}, false),                       // func(error) bool
		reflect.FuncOf([]reflect.Type{tp(complex128(0))}, []reflect.Type{tp(complex128(0))}, false), // complex128 -> "ff" both
	}
	for _, ft := range same {
		r, _, rok := classifyWordSig(ft)
		a, _, aok := abi0ClassifyWordSig(ft)
		if rok != aok || r != a {
			t.Errorf("%v: regabi=%q(%v) abi0=%q(%v); want equal", ft, r, rok, a, aok)
		}
	}
	packed := []reflect.Type{
		reflect.FuncOf([]reflect.Type{tp(abiPoint{})}, nil, false),                                             // regabi "ii_" vs abi0 "i_"
		reflect.FuncOf(nil, []reflect.Type{tp(uint32(0)), tp(uint32(0)), tp(uint32(0)), tp(uint32(0))}, false), // "_iiii" vs "_ii"
	}
	for _, ft := range packed {
		r, _, _ := classifyWordSig(ft)
		a, _, _ := abi0ClassifyWordSig(ft)
		if r == a {
			t.Errorf("%v: regabi and abi0 keys both %q; want differ", ft, r)
		}
	}
}
