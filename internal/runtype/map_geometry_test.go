package runtype

import (
	"reflect"
	"testing"
	"unsafe"
)

// TestMapGeometryMatchesRuntime checks that setMapGroup computes the group
// size and the key and elem geometry that the runtime gives native map
// types, so that the abiMapType mirror matches the running Go version.
func TestMapGeometryMatchesRuntime(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeFor[map[string]int](),
		reflect.TypeFor[map[int8]struct{ A, B int16 }](),
		reflect.TypeFor[map[[200]byte]int64](), // indirect key
		reflect.TypeFor[map[int][300]byte](),   // indirect elem
	} {
		mt := (*abiMapType)(unsafe.Pointer(rtypePtr(rt)))
		key, elem := rt.Key(), rt.Elem()
		if key.Size() > mapMaxKeyBytes {
			key = reflect.PointerTo(key)
		}
		if elem.Size() > mapMaxElemBytes {
			elem = reflect.PointerTo(elem)
		}
		got := *mt
		setMapGroup(&got, key, elem, "probe")

		// Compare everything from GroupSize to the end of the struct.
		start, end := unsafe.Offsetof(mt.GroupSize), unsafe.Sizeof(*mt)
		want := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(mt), start)), end-start)
		have := unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(&got), start)), end-start)
		if string(want) != string(have) {
			t.Errorf("%v: map geometry %x, runtime has %x; abiMapType layout drift?", rt, have, want)
		}
	}
}
