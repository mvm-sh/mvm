package vm

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
)

var stringerSig = reflect.TypeOf((func() string)(nil))

func makeSynthRtype(t *testing.T, name string) reflect.Type {
	t.Helper()
	rt, err := stubs.AttachStructMethods(
		reflect.StructOf([]reflect.StructField{
			{Name: "V", Type: reflect.TypeOf(int(0))},
		}),
		name, "test",
		[]stubs.Method{{
			Name: "String", Exported: true, Sig: stringerSig,
			Handler: func(_ unsafe.Pointer) string { return "" },
		}},
	)
	if err != nil {
		t.Fatalf("AttachStructMethods: %v", err)
	}
	return rt
}

// TestRefreshRtypeCascadesPtrSliceChan: derived ptr/slice/chan rtypes
// captured before a synth swap get refreshed to track the new elem rtype.
func TestRefreshRtypeCascadesPtrSliceChan(t *testing.T) {
	nativeRT := reflect.StructOf([]reflect.StructField{
		{Name: "V", Type: reflect.TypeOf(int(0))},
	})
	tt := &Type{Name: "Cascade", Rtype: nativeRT}

	ptr := PointerTo(tt)
	slice := SliceOf(tt)
	ch := ChanOf(reflect.BothDir, tt)

	if ptr.Rtype.Kind() != reflect.Pointer || ptr.Rtype.Elem() != nativeRT {
		t.Fatalf("pre-refresh ptr elem mismatch")
	}
	if slice.Rtype.Elem() != nativeRT {
		t.Fatalf("pre-refresh slice elem mismatch")
	}
	if ch.Rtype.Elem() != nativeRT {
		t.Fatalf("pre-refresh chan elem mismatch")
	}

	synthRT := makeSynthRtype(t, "Cascade")
	tt.RefreshRtype(synthRT)

	if tt.Rtype != synthRT {
		t.Fatalf("tt.Rtype not updated")
	}
	if ptr.Rtype.Elem() != synthRT {
		t.Errorf("ptr elem = %v, want synth", ptr.Rtype.Elem())
	}
	if slice.Rtype.Elem() != synthRT {
		t.Errorf("slice elem = %v, want synth", slice.Rtype.Elem())
	}
	if ch.Rtype.Elem() != synthRT {
		t.Errorf("chan elem = %v, want synth", ch.Rtype.Elem())
	}
}

// TestRefreshRtypeCascadesArrayMap: array and t-as-key map entries cascade
// to the new elem rtype.
func TestRefreshRtypeCascadesArrayMap(t *testing.T) {
	nativeRT := reflect.StructOf([]reflect.StructField{
		{Name: "V", Type: reflect.TypeOf(int(0))},
	})
	tt := &Type{Name: "CascadeAM", Rtype: nativeRT}

	arr := ArrayOf(3, tt)
	elemT := &Type{Rtype: reflect.TypeOf(int(0))}
	// Map keyed on tt (tt is the key, int is the elem).
	mp := MapOf(tt, elemT)

	synthRT := makeSynthRtype(t, "CascadeAM")
	tt.RefreshRtype(synthRT)

	if arr.Rtype.Elem() != synthRT {
		t.Errorf("array elem = %v, want synth", arr.Rtype.Elem())
	}
	if arr.Rtype.Len() != 3 {
		t.Errorf("array Len = %d, want 3", arr.Rtype.Len())
	}
	if mp.Rtype.Key() != synthRT {
		t.Errorf("map key = %v, want synth", mp.Rtype.Key())
	}
	if mp.Rtype.Elem() != elemT.Rtype {
		t.Errorf("map elem changed unexpectedly")
	}
}

// TestRefreshRtypeCascadesNested: a refresh on tt propagates through *T,
// and *T's own derived (**T, []*T) get rebuilt against the new *T rtype.
func TestRefreshRtypeCascadesNested(t *testing.T) {
	nativeRT := reflect.StructOf([]reflect.StructField{
		{Name: "V", Type: reflect.TypeOf(int(0))},
	})
	tt := &Type{Name: "Nested", Rtype: nativeRT}

	ptr := PointerTo(tt)       // *T
	dptr := PointerTo(ptr)     // **T
	sliceOfPtr := SliceOf(ptr) // []*T

	synthRT := makeSynthRtype(t, "Nested")
	tt.RefreshRtype(synthRT)

	// *T must point at synthRT.
	if ptr.Rtype.Elem() != synthRT {
		t.Errorf("ptr elem = %v, want synth", ptr.Rtype.Elem())
	}
	// **T must point at the new *T rtype.
	if dptr.Rtype.Elem() != ptr.Rtype {
		t.Errorf("**T elem = %v, want refreshed *T = %v", dptr.Rtype.Elem(), ptr.Rtype)
	}
	// []*T must point at the new *T rtype.
	if sliceOfPtr.Rtype.Elem() != ptr.Rtype {
		t.Errorf("[]*T elem = %v, want refreshed *T = %v", sliceOfPtr.Rtype.Elem(), ptr.Rtype)
	}
}

// TestRefreshRtypeNoDerivedIsNoop: refreshing a Type that never had derived
// entries created is safe and just swaps Rtype.
func TestRefreshRtypeNoDerivedIsNoop(t *testing.T) {
	nativeRT := reflect.TypeOf(int(0))
	tt := &Type{Name: "NoDerived", Rtype: nativeRT}
	tt.RefreshRtype(reflect.TypeOf(int64(0)))
	if tt.Rtype.Kind() != reflect.Int64 {
		t.Errorf("Rtype not refreshed")
	}
}

// TestRefreshRtypeSameRtypeIsNoop: passing the same rtype as a refresh value
// does not cascade through derived entries.
func TestRefreshRtypeSameRtypeIsNoop(t *testing.T) {
	nativeRT := reflect.StructOf([]reflect.StructField{
		{Name: "V", Type: reflect.TypeOf(int(0))},
	})
	tt := &Type{Name: "Same", Rtype: nativeRT}
	ptr := PointerTo(tt)
	prevPtrRT := ptr.Rtype
	tt.RefreshRtype(nativeRT)
	if ptr.Rtype != prevPtrRT {
		t.Errorf("ptr rtype changed on no-op refresh")
	}
}
