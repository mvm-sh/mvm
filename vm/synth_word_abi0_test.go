package vm

import (
	"reflect"
	"testing"
	"time"
	"unsafe"
)

type abiPoint struct{ X, Y int32 } // sub-word packed, 8 bytes (fixed.Point26_6 shape)

// abi0AllocWords sizes the p/i/f word slices for a slot-class string.
func abi0AllocWords(classes string) (pw []unsafe.Pointer, sw []uint64, fw []float64) {
	var np, ni, nf int
	for _, c := range classes {
		switch c {
		case 'p':
			np++
		case 'f':
			nf++
		default:
			ni++
		}
	}
	return make([]unsafe.Pointer, np), make([]uint64, ni), make([]float64, nf)
}

func TestABI0ClassifyKeys(t *testing.T) {
	tp := reflect.TypeOf
	cases := []struct {
		name string
		in   []reflect.Type
		out  []reflect.Type
		want string
		drop bool
	}{
		{name: "Stringer", out: []reflect.Type{tp("")}, want: "_pi"},
		{name: "point param packs", in: []reflect.Type{tp(abiPoint{})}, want: "i_"},
		{name: "two points", in: []reflect.Type{tp(abiPoint{}), tp(abiPoint{})}, want: "ii_"},
		{name: "three points (Add3)", in: []reflect.Type{tp(abiPoint{}), tp(abiPoint{}), tp(abiPoint{})}, want: "iii_"},
		{name: "rgba packs", out: []reflect.Type{tp(uint32(0)), tp(uint32(0)), tp(uint32(0)), tp(uint32(0))}, want: "_ii"},
		{name: "time.Time", out: []reflect.Type{tp(time.Time{})}, want: "_iip"},
		{name: "float getter", out: []reflect.Type{tp(float64(0))}, want: "_f"},
		{name: "unary float", in: []reflect.Type{tp(float64(0))}, out: []reflect.Type{tp(float64(0))}, want: "f_f"},
		{name: "reader", in: []reflect.Type{tp([]byte(nil))}, out: []reflect.Type{tp(0), tp((*error)(nil)).Elem()}, want: "pii_ipp"},
		{name: "two int32 params pack", in: []reflect.Type{tp(int32(0)), tp(int32(0))}, want: "i_"},
		{name: "lone int32 param pads to a slot", in: []reflect.Type{tp(int32(0))}, want: "i_"},
		{name: "bool result pads to a slot", out: []reflect.Type{tp(true)}, want: "_i"},
		// On the stack ABI sub-word floats are raw bytes in an 'i' slot; complex128
		// is two float64 halves ("ff", matching the register ABI); arrays pack like
		// structs. The register classifier drops all of these except complex128.
		{name: "float32 packs to a slot", out: []reflect.Type{tp(float32(0))}, want: "_i"},
		{name: "complex128 result", out: []reflect.Type{tp(complex128(0))}, want: "_ff"},
		{name: "complex128 param", in: []reflect.Type{tp(complex128(0))}, want: "ff_"},
		{name: "complex64 packs to a slot", out: []reflect.Type{tp(complex64(0))}, want: "_i"},
		{name: "array [2]int32 packs to one slot", in: []reflect.Type{tp([2]int32{})}, want: "i_"},
		{name: "array [2]int spans two slots", out: []reflect.Type{tp([2]int{})}, want: "_ii"},
		{name: "array [4]byte packs to one slot", in: []reflect.Type{tp([4]byte{})}, want: "i_"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ft := reflect.FuncOf(c.in, c.out, false)
			key, _, ok := abi0ClassifyWordSig(ft)
			if c.drop {
				if ok {
					t.Fatalf("want drop, got key %q", key)
				}
				return
			}
			if !ok {
				t.Fatalf("want key %q, got drop", c.want)
			}
			if key != c.want {
				t.Fatalf("key = %q, want %q", key, c.want)
			}
		})
	}
}

// memBytes returns a copy of v's raw memory image.
func memBytes(v reflect.Value) []byte {
	p := reflect.New(v.Type())
	p.Elem().Set(v)
	return append([]byte(nil), unsafe.Slice((*byte)(p.UnsafePointer()), v.Type().Size())...)
}

// abi0Image extracts the slot-word byte image of the single-value region [v.Type()]
// in slot order. For an ABI0-correct decomposition this must equal v's memory image
// (the wasm stack passes a value as exactly its memory bytes).
func abi0Image(t *testing.T, v reflect.Value) []byte {
	t.Helper()
	r, ok := classifyABI0Region([]reflect.Type{v.Type()})
	if !ok {
		t.Fatalf("classify %v failed", v.Type())
	}
	pw, sw, fw := abi0AllocWords(r.classes)
	abi0MarshalResults(r, []reflect.Value{v}, pw, sw, fw)
	var img []byte
	var pi, si, fi int
	for _, c := range r.classes {
		var b [8]byte
		switch c {
		case 'p':
			*(*unsafe.Pointer)(unsafe.Pointer(&b)) = pw[pi]
			pi++
		case 'f':
			*(*float64)(unsafe.Pointer(&b)) = fw[fi]
			fi++
		default:
			*(*uint64)(unsafe.Pointer(&b)) = sw[si]
			si++
		}
		img = append(img, b[:]...)
	}
	return img
}

// TestABI0MemoryImage proves the slot decomposition reproduces the value's exact
// memory bytes -- the ABI0 contract (args are passed as their memory image).
func TestABI0MemoryImage(t *testing.T) {
	hi := []byte("hello, wasm")
	nine := 9
	vals := []reflect.Value{
		reflect.ValueOf(abiPoint{X: -7, Y: 0x12345678}),
		reflect.ValueOf("a string value"),
		reflect.ValueOf(hi), // []byte (3-word slice)
		reflect.ValueOf(struct {
			A int64
			B *int
		}{5, &nine}),
		reflect.ValueOf(struct {
			A bool
			P *byte
		}{true, &hi[0]}),
		reflect.ValueOf(time.Unix(1700000000, 42)),
		reflect.ValueOf(float32(-1.5)),
		reflect.ValueOf(complex128(complex(1.5, -2.5))),
		reflect.ValueOf(complex64(complex(float32(3), float32(-4)))),
		reflect.ValueOf([3]int32{-1, 2, -3}),    // packed array spanning 2 slots
		reflect.ValueOf([2]float64{1.25, -6.5}), // float-leaf array -> "ff"
		reflect.ValueOf([2]*int{&nine, &nine}),  // pointer array (GC-visible slots)
		reflect.ValueOf(struct { // float32 packed beside an int32
			A float32
			B int32
		}{-2.5, 7}),
	}
	for _, v := range vals {
		if _, ok := classifyABI0Region([]reflect.Type{v.Type()}); !ok {
			t.Fatalf("%v: unexpectedly unclassifiable", v.Type())
		}
		got := abi0Image(t, v)
		want := memBytes(v)
		// The region rounds up to whole 8-byte slots, so a sub-8-multiple value
		// (float32, [3]int32) leaves trailing frame padding; the value occupies the
		// low Size bytes, which must equal its memory image.
		if string(got[:v.Type().Size()]) != string(want) {
			t.Fatalf("%v: slot image %x != memory image %x", v.Type(), got[:v.Type().Size()], want)
		}
	}
}

// TestABI0RoundTrip extracts values into words and reconstructs them, exercising
// packed and shared slots (point, rgba, mixed).
func TestABI0RoundTrip(t *testing.T) {
	cases := [][]reflect.Value{
		{reflect.ValueOf(abiPoint{X: 11, Y: -22})},
		{reflect.ValueOf(abiPoint{1, 2}), reflect.ValueOf(abiPoint{3, 4}), reflect.ValueOf(abiPoint{5, 6})},
		{reflect.ValueOf(uint32(0xAA)), reflect.ValueOf(uint32(0xBB)), reflect.ValueOf(uint32(0xCC)), reflect.ValueOf(uint32(0xDD))},
		{reflect.ValueOf(int64(-1234567)), reflect.ValueOf(int32(99)), reflect.ValueOf(int32(-99))},
		{reflect.ValueOf(3.14159), reflect.ValueOf(2.71828)},
		{reflect.ValueOf(float32(-1.5)), reflect.ValueOf(float32(2.25))},
		{reflect.ValueOf(complex128(complex(1.5, -2.5))), reflect.ValueOf(complex64(complex(float32(3), float32(-4))))},
		{reflect.ValueOf([3]int32{-1, 2, -3}), reflect.ValueOf([2]float64{1.25, -6.5})},
	}
	for ci, vals := range cases {
		types := make([]reflect.Type, len(vals))
		for i, v := range vals {
			types[i] = v.Type()
		}
		r, ok := classifyABI0Region(types)
		if !ok {
			t.Fatalf("case %d: classify failed", ci)
		}
		pw, sw, fw := abi0AllocWords(r.classes)
		abi0MarshalResults(r, vals, pw, sw, fw)
		got := abi0MarshalArgs(r, pw, sw, fw)
		if len(got) != len(vals) {
			t.Fatalf("case %d: got %d values, want %d", ci, len(got), len(vals))
		}
		for i := range vals {
			if got[i].Interface() != vals[i].Interface() {
				t.Fatalf("case %d arg %d: round-trip %v != %v", ci, i, got[i], vals[i])
			}
		}
	}
}
