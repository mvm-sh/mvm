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
		{reflect.TypeOf(float64(0)), "f", true},    // float64 is an FP-register word
		{reflect.TypeOf(float32(0)), "", false},    // float32 is sub-word, dropped
		{reflect.TypeOf(complex128(0)), "", false}, // complex dropped
		{reflect.TypeOf([2]int{}), "", false},      // and arrays
		{reflect.TypeOf(struct{ X, Y float64 }{}), "ff", true}, // all-float struct
		// word-sized-leaf structs flatten to their leaves' words.
		{reflect.TypeOf(struct{ a, b int }{}), "ii", true},
		{reflect.TypeOf(struct {
			s string
			n int
		}{}), "pii", true},
		{reflect.TypeOf(time.Time{}), "iip", true}, // {wall uint64; ext int64; loc *Location}
		// a sub-word leaf breaks word-striding -> dropped.
		{reflect.TypeOf(struct{ a, b uint32 }{}), "", false},
		{reflect.TypeOf(struct {
			a bool
			b *int
		}{}), "", false},
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
		{reflect.TypeOf((func() (int, int))(nil)), "", false},
		// a struct with a sub-word leaf is unclassifiable -> drop.
		{reflect.TypeOf((func() struct{ a, b uint32 })(nil)), "", false},
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
// mapping (the core marshaling) independent of the call ABI; run under
// GOARCH=amd64 too for cross-arch coverage.
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
		struct{ X, Y float64 }{1.5, -2.5},             // all-float struct -> "ff"
		struct {                                       // mixed int/float/ptr -> "ifp"
			A int64
			B float64
			C *int
		}{42, 6.022e23, &n},
	}
	for _, v := range values {
		rt := reflect.TypeOf(v)
		classes, ok := classifyType(rt)
		if !ok {
			t.Fatalf("classifyType(%v) not ok", rt)
		}
		pw := make([]unsafe.Pointer, strings.Count(classes, "p"))
		sw := make([]uint64, strings.Count(classes, "i"))
		fw := make([]float64, strings.Count(classes, "f"))

		src := reflect.New(rt)
		src.Elem().Set(reflect.ValueOf(v))
		readWords(rt, classes, src.UnsafePointer(), pw, sw, fw, 0, 0, 0)

		dst := reflect.New(rt)
		writeWords(rt, classes, dst.UnsafePointer(), pw, sw, fw, 0, 0, 0)

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
