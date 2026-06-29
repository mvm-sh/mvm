package vm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/derive"
	"github.com/mvm-sh/mvm/mtype"
)

// These tests cover the post-flip flow: NewStructType yields a symbolic
// placeholder (Rtype nil), SetFields adopts the symbolic shape, and
// MaterializeRtype builds the rtype, breaking pointer cycles via a placeholder
// rtype patched in place.
func TestNewStructTypeSetFields(t *testing.T) {
	// ptrField returns a *placeholder field type named name.
	ptrField := func(name string, elem *mtype.Type) *mtype.Type {
		f := *derive.SymPtr(elem)
		f.Name = name
		f.Base = derive.SymPtr(elem)
		return &f
	}

	t.Run("self_referential", func(t *testing.T) {
		// Simulate: type T struct { V int; Next *T }
		placeholder := mtype.NewStructType("T")
		fields := []*mtype.Type{
			{Name: "V", Rtype: reflect.TypeOf(0)},
			ptrField("Next", placeholder),
		}
		placeholder.SetFields(mtype.SymStruct(fields, nil, nil))
		derive.MaterializeRtype(placeholder)

		if placeholder.Rtype.Size() == 0 {
			t.Fatal("expected non-zero size after materialize")
		}
		if n := placeholder.Rtype.NumField(); n != 2 {
			t.Fatalf("expected 2 fields, got %d", n)
		}
		if f := placeholder.Rtype.Field(0); f.Name != "V" {
			t.Fatalf("expected field 0 name V, got %s", f.Name)
		}
		if f := placeholder.Rtype.Field(1); f.Name != "Next" {
			t.Fatalf("expected field 1 name Next, got %s", f.Name)
		}

		v := reflect.New(placeholder.Rtype).Elem()
		v.Field(0).SetInt(42)
		if got := v.Field(0).Int(); got != 42 {
			t.Fatalf("expected 42, got %d", got)
		}
		if !v.Field(1).IsNil() {
			t.Fatal("expected nil Next field")
		}
	})

	t.Run("pointer_elem", func(t *testing.T) {
		// PointerTo(placeholder).Elem() returns the finalized struct.
		placeholder := mtype.NewStructType("T")
		fields := []*mtype.Type{
			{Name: "X", Rtype: reflect.TypeOf(0)},
			ptrField("Self", placeholder),
		}
		placeholder.SetFields(mtype.SymStruct(fields, nil, nil))
		derive.MaterializeRtype(placeholder)
		ptrRtype := reflect.PointerTo(placeholder.Rtype)

		elem := ptrRtype.Elem()
		if elem.NumField() != 2 {
			t.Fatalf("expected 2 fields via pointer elem, got %d", elem.NumField())
		}
		if elem.Field(0).Name != "X" {
			t.Fatalf("expected field X, got %s", elem.Field(0).Name)
		}
	})

	t.Run("size_and_align", func(t *testing.T) {
		placeholder := mtype.NewStructType("T")
		fields := []*mtype.Type{
			{Name: "A", Rtype: reflect.TypeOf(int64(0))},
			{Name: "B", Rtype: reflect.TypeOf(true)},
		}
		placeholder.SetFields(mtype.SymStruct(fields, nil, nil))
		derive.MaterializeRtype(placeholder)

		direct := mtype.StructOf(fields, nil, nil)
		if placeholder.Rtype.Size() != direct.Rtype.Size() {
			t.Fatalf("size mismatch: %d vs %d", placeholder.Rtype.Size(), direct.Rtype.Size())
		}
		if placeholder.Rtype.Align() != direct.Rtype.Align() {
			t.Fatalf("align mismatch: %d vs %d", placeholder.Rtype.Align(), direct.Rtype.Align())
		}
	})

	t.Run("non_recursive", func(t *testing.T) {
		placeholder := mtype.NewStructType("T")
		fields := []*mtype.Type{
			{Name: "Name", Rtype: reflect.TypeOf("")},
			{Name: "Age", Rtype: reflect.TypeOf(0)},
		}
		placeholder.SetFields(mtype.SymStruct(fields, nil, nil))
		derive.MaterializeRtype(placeholder)

		v := reflect.New(placeholder.Rtype).Elem()
		v.Field(0).SetString("hello")
		v.Field(1).SetInt(30)
		if got := v.Field(0).String(); got != "hello" {
			t.Fatalf("expected hello, got %s", got)
		}
		if got := v.Field(1).Int(); got != 30 {
			t.Fatalf("expected 30, got %d", got)
		}
	})

	t.Run("patched_as_field_type", func(t *testing.T) {
		placeholder := mtype.NewStructType("T")
		fields := []*mtype.Type{
			{Name: "X", Rtype: reflect.TypeOf(0)},
			{Name: "Y", Rtype: reflect.TypeOf("")},
		}
		placeholder.SetFields(mtype.SymStruct(fields, nil, nil))
		derive.MaterializeRtype(placeholder)

		if placeholder.Rtype.String() == "" {
			t.Fatal("expected non-empty type string after materialize")
		}
		outer := mtype.StructOf([]*mtype.Type{
			{Name: "Inner", Rtype: placeholder.Rtype},
			{Name: "Z", Rtype: reflect.TypeOf(0)},
		}, nil, nil)
		if outer.Rtype.NumField() != 2 {
			t.Fatalf("expected 2 fields, got %d", outer.Rtype.NumField())
		}
	})

	t.Run("name_in_string", func(t *testing.T) {
		// The placeholder cycle-breaking rtype embeds the type name in its sole
		// field, and patchRtype preserves Str, so the materialized type's String()
		// keeps identifying the interpreted type in native diagnostics.
		placeholder := mtype.NewStructType("Vector")
		placeholder.SetFields(mtype.SymStruct([]*mtype.Type{{Name: "X", Rtype: reflect.TypeOf(0)}}, nil, nil))
		derive.MaterializeRtype(placeholder)
		if s := placeholder.Rtype.String(); !strings.Contains(s, "Vector") {
			t.Fatalf("finalized type string %q does not identify the type name", s)
		}
	})

	t.Run("linked_list_reflect", func(t *testing.T) {
		placeholder := mtype.NewStructType("T")
		fields := []*mtype.Type{
			{Name: "V", Rtype: reflect.TypeOf(0)},
			ptrField("Next", placeholder),
		}
		placeholder.SetFields(mtype.SymStruct(fields, nil, nil))
		derive.MaterializeRtype(placeholder)

		node2 := reflect.New(placeholder.Rtype)
		node2.Elem().Field(0).SetInt(2)
		node1 := reflect.New(placeholder.Rtype)
		node1.Elem().Field(0).SetInt(1)
		node1.Elem().Field(1).Set(node2)

		next := node1.Elem().Field(1).Elem()
		if got := next.Field(0).Int(); got != 2 {
			t.Fatalf("expected 2, got %d", got)
		}
	})
}
