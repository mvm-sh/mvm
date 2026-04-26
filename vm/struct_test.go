package vm

import (
	"reflect"
	"testing"
)

func TestNewStructTypeSetFields(t *testing.T) {
	t.Run("self_referential", func(t *testing.T) {
		// Simulate: type Node struct { V int; Next *Node }
		placeholder := NewStructType()
		ptrType := PointerTo(placeholder)

		fields := []*Type{
			{Name: "V", Rtype: reflect.TypeOf(0)},
			{Name: "Next", Rtype: ptrType.Rtype, ElemType: placeholder},
		}
		placeholder.SetFields(StructOf(fields, nil, nil))

		// Verify Size is non-zero (int + pointer).
		if placeholder.Rtype.Size() == 0 {
			t.Fatal("expected non-zero size after SetFields")
		}

		// Verify fields are accessible via reflect.
		if n := placeholder.Rtype.NumField(); n != 2 {
			t.Fatalf("expected 2 fields, got %d", n)
		}
		if f := placeholder.Rtype.Field(0); f.Name != "V" {
			t.Fatalf("expected field 0 name V, got %s", f.Name)
		}
		if f := placeholder.Rtype.Field(1); f.Name != "Next" {
			t.Fatalf("expected field 1 name Next, got %s", f.Name)
		}

		// Verify reflect.New allocates and field access works.
		v := reflect.New(placeholder.Rtype).Elem()
		v.Field(0).SetInt(42)
		if got := v.Field(0).Int(); got != 42 {
			t.Fatalf("expected 42, got %d", got)
		}
		// Next field should be a nil pointer.
		if !v.Field(1).IsNil() {
			t.Fatal("expected nil Next field")
		}
	})

	t.Run("pointer_elem", func(t *testing.T) {
		// Verify that PointerTo(placeholder).Elem() returns the finalized struct.
		placeholder := NewStructType()
		ptrRtype := reflect.PointerTo(placeholder.Rtype)

		fields := []*Type{
			{Name: "X", Rtype: reflect.TypeOf(0)},
			{Name: "Self", Rtype: ptrRtype, ElemType: placeholder},
		}
		placeholder.SetFields(StructOf(fields, nil, nil))

		// The pointer type's Elem should now have the real struct fields.
		elem := ptrRtype.Elem()
		if elem.NumField() != 2 {
			t.Fatalf("expected 2 fields via pointer elem, got %d", elem.NumField())
		}
		if elem.Field(0).Name != "X" {
			t.Fatalf("expected field X, got %s", elem.Field(0).Name)
		}
	})

	t.Run("size_and_align", func(t *testing.T) {
		// After SetFields, size and alignment match a direct StructOf.
		placeholder := NewStructType()
		fields := []*Type{
			{Name: "A", Rtype: reflect.TypeOf(int64(0))},
			{Name: "B", Rtype: reflect.TypeOf(true)},
		}
		placeholder.SetFields(StructOf(fields, nil, nil))

		direct := StructOf(fields, nil, nil)
		if placeholder.Rtype.Size() != direct.Rtype.Size() {
			t.Fatalf("size mismatch: %d vs %d", placeholder.Rtype.Size(), direct.Rtype.Size())
		}
		if placeholder.Rtype.Align() != direct.Rtype.Align() {
			t.Fatalf("align mismatch: %d vs %d", placeholder.Rtype.Align(), direct.Rtype.Align())
		}
	})

	t.Run("non_recursive", func(t *testing.T) {
		// SetFields also works for normal (non-recursive) structs.
		placeholder := NewStructType()
		fields := []*Type{
			{Name: "Name", Rtype: reflect.TypeOf("")},
			{Name: "Age", Rtype: reflect.TypeOf(0)},
		}
		placeholder.SetFields(StructOf(fields, nil, nil))

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
		// Verify a patched placeholder can be used as a field type
		// in a subsequent reflect.StructOf without crashing (rtype.String).
		placeholder := NewStructType()
		fields := []*Type{
			{Name: "X", Rtype: reflect.TypeOf(0)},
			{Name: "Y", Rtype: reflect.TypeOf("")},
		}
		placeholder.SetFields(StructOf(fields, nil, nil))

		s := placeholder.Rtype.String()
		if s == "" {
			t.Fatal("expected non-empty type string after SetFields")
		}

		outer := StructOf([]*Type{
			{Name: "Inner", Rtype: placeholder.Rtype},
			{Name: "Z", Rtype: reflect.TypeOf(0)},
		}, nil, nil)
		if outer.Rtype.NumField() != 2 {
			t.Fatalf("expected 2 fields, got %d", outer.Rtype.NumField())
		}
	})

	t.Run("linked_list_reflect", func(t *testing.T) {
		// Build a two-node linked list using reflect and verify traversal.
		placeholder := NewStructType()
		ptrType := PointerTo(placeholder)
		fields := []*Type{
			{Name: "V", Rtype: reflect.TypeOf(0)},
			{Name: "Next", Rtype: ptrType.Rtype, ElemType: placeholder},
		}
		placeholder.SetFields(StructOf(fields, nil, nil))

		// Create node2 = &Node{V: 2, Next: nil}
		node2 := reflect.New(placeholder.Rtype)
		node2.Elem().Field(0).SetInt(2)

		// Create node1 = &Node{V: 1, Next: node2}
		node1 := reflect.New(placeholder.Rtype)
		node1.Elem().Field(0).SetInt(1)
		node1.Elem().Field(1).Set(node2)

		// Traverse: node1.Next.V == 2
		next := node1.Elem().Field(1).Elem()
		if got := next.Field(0).Int(); got != 2 {
			t.Fatalf("expected 2, got %d", got)
		}
	})
}
