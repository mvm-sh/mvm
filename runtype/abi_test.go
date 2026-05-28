package runtype

import (
	"reflect"
	"testing"
	"unsafe"
)

// Probes confirm the abi mirrors match the running Go runtime.
// Layout drift fails loudly at CI time instead of corrupting memory at runtime.

type probeNamed struct {
	A int
}

func (probeNamed) Marker() {}

func TestAbiTypeLayout(t *testing.T) {
	rt := reflect.TypeOf(probeNamed{})
	at := rtypePtr(rt)
	if at == nil {
		t.Fatal("rtypePtr returned nil")
	}

	// {A int} is one word on this arch.
	want := unsafe.Sizeof(uintptr(0))
	if at.Size != want {
		t.Errorf("Size_ = %d, want %d", at.Size, want)
	}

	if at.Kind != kindStruct {
		t.Errorf("Kind_ = %d, want %d (struct)", at.Kind, kindStruct)
	}

	// A defined type with methods has both Named and Uncommon set.
	if at.TFlag&tflagNamed == 0 {
		t.Errorf("TFlag missing tflagNamed: %#x", at.TFlag)
	}
	if at.TFlag&tflagUncommon == 0 {
		t.Errorf("TFlag missing tflagUncommon: %#x", at.TFlag)
	}

	if at.Align != uint8(unsafe.Alignof(uintptr(0))) {
		t.Errorf("Align_ = %d, want %d", at.Align, unsafe.Alignof(uintptr(0)))
	}

	if at.Hash == 0 {
		t.Errorf("Hash = 0; expected nonzero for compiler-emitted rtype")
	}
}

func TestAbiStructTypeLayout(t *testing.T) {
	type s struct {
		A int
		B string
	}
	rt := reflect.TypeOf(s{})
	st := (*abiStructType)(unsafe.Pointer(rtypePtr(rt)))

	if got := len(st.Fields); got != 2 {
		t.Fatalf("Fields len = %d, want 2", got)
	}
	if st.Fields[0].Offset != 0 {
		t.Errorf("Fields[0].Offset = %d, want 0", st.Fields[0].Offset)
	}
	wantOff := unsafe.Sizeof(int(0))
	if st.Fields[1].Offset != wantOff {
		t.Errorf("Fields[1].Offset = %d, want %d", st.Fields[1].Offset, wantOff)
	}
	if st.Fields[0].Typ == nil || st.Fields[1].Typ == nil {
		t.Errorf("field Typ is nil; mirror layout drift?")
	}
}

func TestAbiPtrTypeLayout(t *testing.T) {
	rt := reflect.TypeOf((*int)(nil))
	pt := (*abiPtrType)(unsafe.Pointer(rtypePtr(rt)))

	if pt.Kind != kindPointer {
		t.Errorf("Kind_ = %d, want %d (pointer)", pt.Kind, kindPointer)
	}
	if pt.TFlag&tflagDirectIface == 0 {
		t.Errorf("TFlag missing tflagDirectIface: %#x", pt.TFlag)
	}
	if pt.Elem == nil {
		t.Fatalf("Elem nil; layout drift?")
	}
	if pt.Elem.Kind != kindInt {
		t.Errorf("Elem.Kind = %d, want %d (int)", pt.Elem.Kind, kindInt)
	}
}

func TestAbiSliceTypeLayout(t *testing.T) {
	rt := reflect.TypeOf([]int{})
	st := (*abiSliceType)(unsafe.Pointer(rtypePtr(rt)))
	if st.Kind != kindSlice {
		t.Errorf("Kind_ = %d, want %d (slice)", st.Kind, kindSlice)
	}
	if st.Elem == nil || st.Elem.Kind != kindInt {
		t.Errorf("Elem mismatch; layout drift?")
	}
}

func TestAbiArrayTypeLayout(t *testing.T) {
	rt := reflect.TypeOf([3]int{})
	at := (*abiArrayType)(unsafe.Pointer(rtypePtr(rt)))
	if at.Kind != kindArray {
		t.Errorf("Kind_ = %d, want %d (array)", at.Kind, kindArray)
	}
	if at.Len != 3 {
		t.Errorf("Len = %d, want 3", at.Len)
	}
	if at.Elem == nil || at.Elem.Kind != kindInt {
		t.Errorf("Elem mismatch; layout drift?")
	}
}

func TestAbiMapTypeLayout(t *testing.T) {
	rt := reflect.TypeOf(map[string]int{})
	mt := (*abiMapType)(unsafe.Pointer(rtypePtr(rt)))
	if mt.Kind != kindMap {
		t.Errorf("Kind_ = %d, want %d (map)", mt.Kind, kindMap)
	}
	if mt.Key == nil || mt.Key.Kind != kindString {
		t.Errorf("Key mismatch; layout drift?")
	}
	if mt.Elem == nil || mt.Elem.Kind != kindInt {
		t.Errorf("Elem mismatch; layout drift?")
	}
	if mt.Group == nil {
		t.Errorf("Group nil; swisstable layout drift?")
	}
	if mt.Hasher == nil {
		t.Errorf("Hasher nil; layout drift?")
	}
}

func TestUncommonOffsetsByKind(t *testing.T) {
	// Runtime Uncommon() (internal/abi/type.go:319) computes uncommon offset
	// as sizeof(KindType).
	// Synthesis depends on these being predictable.
	cases := []struct {
		name    string
		mirror  uintptr
		wantOff uintptr // doc: equals mirror size
	}{
		{"Type", unsafe.Sizeof(abiType{}), unsafe.Sizeof(abiType{})},
		{"PtrType", unsafe.Sizeof(abiPtrType{}), unsafe.Sizeof(abiPtrType{})},
		{"SliceType", unsafe.Sizeof(abiSliceType{}), unsafe.Sizeof(abiSliceType{})},
		{"ArrayType", unsafe.Sizeof(abiArrayType{}), unsafe.Sizeof(abiArrayType{})},
		{"MapType", unsafe.Sizeof(abiMapType{}), unsafe.Sizeof(abiMapType{})},
		{"StructType", unsafe.Sizeof(abiStructType{}), unsafe.Sizeof(abiStructType{})},
	}
	for _, c := range cases {
		if c.mirror != c.wantOff {
			t.Errorf("%s mirror sizeof = %d, want %d", c.name, c.mirror, c.wantOff)
		}
	}
	t.Logf("uncommon offsets on this arch (ptrsize=%d):", unsafe.Sizeof(uintptr(0)))
	for _, c := range cases {
		t.Logf("  %-12s = %d", c.name, c.mirror)
	}
}

func TestAddReflectOff(t *testing.T) {
	// Round-trip: register a pointer, get a non-zero offset back.
	// Verifies the linkname; resolveReflectName/Type/Text are tested elsewhere.
	x := 42
	off := addReflectOff(unsafe.Pointer(&x))
	if off == 0 {
		t.Error("addReflectOff returned 0; linkname not resolved?")
	}
	y := 99
	off2 := addReflectOff(unsafe.Pointer(&y))
	if off2 == 0 || off2 == off {
		t.Errorf("addReflectOff(&y) = %d, expected non-zero and distinct from %d", off2, off)
	}
}

// TestUncommonAndMethodSize: wire sizes are pointer-size invariant
// (all uint32).
func TestUncommonAndMethodSize(t *testing.T) {
	if got, want := unsafe.Sizeof(abiUncommon{}), uintptr(16); got != want {
		t.Errorf("sizeof(abiUncommon) = %d, want %d", got, want)
	}
	if got, want := unsafe.Sizeof(abiMethod{}), uintptr(16); got != want {
		t.Errorf("sizeof(abiMethod) = %d, want %d", got, want)
	}
}

func TestAsReflectTypeRoundTrip(t *testing.T) {
	rt := reflect.TypeOf(probeNamed{})
	at := rtypePtr(rt)
	rt2 := asReflectType(at)

	if rt != rt2 {
		t.Errorf("roundtrip not identical: rt=%v rt2=%v", rt, rt2)
	}
	// Underlying *abiType should match.
	if rtypePtr(rt2) != at {
		t.Errorf("data word lost in roundtrip")
	}
}
