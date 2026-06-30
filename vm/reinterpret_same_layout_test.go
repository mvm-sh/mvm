package vm

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/internal/runtype"
	"github.com/mvm-sh/mvm/mtype"
)

// namedRtype builds a distinct named struct rtype over layout, mirroring how
// file-by-file compilation mints a separate identity for one Go type.
func namedRtype(name string, layout reflect.Type) reflect.Type {
	t := mtype.NewPlaceholderRtype("T")
	mtype.PatchRtype(t, layout)
	runtype.StampName(t, name)
	return t
}

// Two distinct rtypes for one Go type coerce by reinterpretation; different types do not.
func TestReinterpretSameLayout(t *testing.T) {
	layout := reflect.StructOf([]reflect.StructField{{Name: "X", Type: reflect.TypeOf(0)}})
	a := namedRtype("filedesc.Enum", layout)
	b := namedRtype("filedesc.Enum", layout)
	if a == b {
		t.Fatal("expected two distinct rtypes for one name")
	}

	// []a -> []b: same printed type, kind, size -> reinterpret.
	srcSlice := reflect.MakeSlice(reflect.SliceOf(a), 2, 4)
	srcSlice.Index(0).Field(0).SetInt(7)
	got, ok := reinterpretSameLayout(srcSlice, reflect.SliceOf(b))
	if !ok {
		t.Fatal("reinterpretSameLayout: want ok for []filedesc.Enum -> []filedesc.Enum")
	}
	if got.Type() != reflect.SliceOf(b) {
		t.Fatalf("reinterpreted type = %v, want %v", got.Type(), reflect.SliceOf(b))
	}
	if got.Len() != 2 || got.Cap() != 4 || got.Index(0).Field(0).Int() != 7 {
		t.Fatalf("reinterpret lost slice data: len=%d cap=%d x=%d", got.Len(), got.Cap(), got.Index(0).Field(0).Int())
	}
	dst := reflect.New(reflect.StructOf([]reflect.StructField{
		{Name: "F", Type: reflect.SliceOf(b)},
	})).Elem().Field(0)
	dst.Set(got) // would panic without the fix

	// Different type -> no reinterpret (must not mask a real mismatch).
	if _, ok := reinterpretSameLayout(reflect.ValueOf(0), reflect.TypeOf(int64(0))); ok {
		t.Fatal("reinterpretSameLayout: want !ok for int -> int64")
	}
	other := namedRtype("other.Enum", layout)
	if _, ok := reinterpretSameLayout(reflect.New(a).Elem(), other); ok {
		t.Fatal("reinterpretSameLayout: want !ok for filedesc.Enum -> other.Enum")
	}
}
