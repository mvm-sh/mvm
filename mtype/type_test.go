package mtype

import (
	"reflect"
	"testing"
)

func TestKind(t *testing.T) {
	// Symbolic kind reported without any rtype (the S1 goal: parse-time kind
	// before a runtime type is materialized).
	if got := (&Type{kind: reflect.Struct}).Kind(); got != reflect.Struct {
		t.Errorf("symbolic Kind() = %v, want Struct", got)
	}
	// Falls back to the rtype when kind is unset.
	if got := (&Type{Rtype: reflect.TypeOf([]int(nil))}).Kind(); got != reflect.Slice {
		t.Errorf("fallback Kind() = %v, want Slice", got)
	}
	// Constructors populate kind.
	if got := TypeOf(0).Kind(); got != reflect.Int {
		t.Errorf("TypeOf(0).Kind() = %v, want Int", got)
	}
	if got := StructOf(nil, nil, nil).Kind(); got != reflect.Struct {
		t.Errorf("StructOf().Kind() = %v, want Struct", got)
	}
	if got := FuncOf(nil, nil, false).Kind(); got != reflect.Func {
		t.Errorf("FuncOf().Kind() = %v, want Func", got)
	}
}

// TestSymbolicLayout checks Size/Align computed from the symbolic graph (nil
// Rtype) against reflect's ground truth, including struct padding edge cases.
func TestSymbolicLayout(t *testing.T) {
	b := func(k reflect.Kind) *Type { return &Type{kind: k} }
	st := func(fields ...*Type) *Type { return &Type{kind: reflect.Struct, Fields: fields} }
	zeroArr := &Type{kind: reflect.Array, ArrayLen: 0, ElemType: b(reflect.Int)}

	cases := []struct {
		typ  *Type
		want reflect.Type
	}{
		{b(reflect.Bool), reflect.TypeOf(false)},
		{b(reflect.Int8), reflect.TypeOf(int8(0))},
		{b(reflect.Int16), reflect.TypeOf(int16(0))},
		{b(reflect.Int32), reflect.TypeOf(int32(0))},
		{b(reflect.Int64), reflect.TypeOf(int64(0))},
		{b(reflect.Int), reflect.TypeOf(0)},
		{b(reflect.Uint), reflect.TypeOf(uint(0))},
		{b(reflect.Uintptr), reflect.TypeOf(uintptr(0))},
		{b(reflect.Float32), reflect.TypeOf(float32(0))},
		{b(reflect.Float64), reflect.TypeOf(float64(0))},
		{b(reflect.Complex64), reflect.TypeOf(complex64(0))},
		{b(reflect.Complex128), reflect.TypeOf(complex128(0))},
		{b(reflect.String), reflect.TypeOf("")},
		{b(reflect.Func), reflect.TypeOf(func() {})},
		{b(reflect.Interface), reflect.TypeOf((*error)(nil)).Elem()},
		{&Type{kind: reflect.Pointer, ElemType: b(reflect.Int)}, reflect.TypeOf((*int)(nil))},
		{&Type{kind: reflect.Slice, ElemType: b(reflect.Int)}, reflect.TypeOf([]int(nil))},
		{&Type{kind: reflect.Map, KeyType: b(reflect.String), ElemType: b(reflect.Int)}, reflect.TypeOf(map[string]int(nil))},
		{&Type{kind: reflect.Chan, ElemType: b(reflect.Int)}, reflect.TypeOf(make(chan int))},
		{&Type{kind: reflect.Array, ArrayLen: 3, ElemType: b(reflect.Int)}, reflect.TypeOf([3]int{})},
		{zeroArr, reflect.TypeOf([0]int{})},
		{st(), reflect.TypeOf(struct{}{})},
		{st(b(reflect.Int8), b(reflect.Int64)), reflect.TypeOf(struct {
			a int8
			b int64
		}{})},
		{st(b(reflect.Bool), b(reflect.Complex128)), reflect.TypeOf(struct {
			a bool
			b complex128
		}{})},
		{st(b(reflect.Int8), b(reflect.Int8), b(reflect.Int32)), reflect.TypeOf(struct {
			a, b int8
			c    int32
		}{})},
		{st(b(reflect.Int64), zeroArr), reflect.TypeOf(struct {
			x int64
			y [0]int
		}{})},
		{st(zeroArr), reflect.TypeOf(struct{ x [0]int }{})},
		{st(zeroArr, b(reflect.Int64)), reflect.TypeOf(struct {
			x [0]int
			y int64
		}{})},
		{&Type{kind: reflect.Slice, ElemType: &Type{kind: reflect.Pointer, ElemType: b(reflect.Int)}}, reflect.TypeOf([]*int(nil))},
	}
	for _, c := range cases {
		if c.typ.Rtype != nil {
			t.Fatalf("%v: precondition failed, Rtype must be nil for symbolic layout", c.want)
		}
		if got, want := c.typ.Size(), c.want.Size(); got != want {
			t.Errorf("%v: Size()=%d, want %d", c.want, got, want)
		}
		if got, want := c.typ.Align(), c.want.Align(); got != want {
			t.Errorf("%v: Align()=%d, want %d", c.want, got, want)
		}
	}
}

func symBasic(k reflect.Kind) *Type { return &Type{kind: k} }
func symStruct(fs ...*Type) *Type   { return &Type{kind: reflect.Struct, Fields: fs} }

// TestSymbolicComparable checks IsComparable on nil-Rtype types against
// reflect.Type.Comparable.
func TestSymbolicComparable(t *testing.T) {
	cases := []struct {
		typ  *Type
		want reflect.Type
	}{
		{symBasic(reflect.Int), reflect.TypeOf(0)},
		{symBasic(reflect.String), reflect.TypeOf("")},
		{SymSlice(symBasic(reflect.Int)), reflect.TypeOf([]int(nil))},
		{SymMap(symBasic(reflect.String), symBasic(reflect.Int)), reflect.TypeOf(map[string]int(nil))},
		{SymArray(3, symBasic(reflect.Int)), reflect.TypeOf([3]int{})},
		{SymArray(3, SymSlice(symBasic(reflect.Int))), reflect.TypeOf([3][]int{})},
		{SymPtr(symBasic(reflect.Int)), reflect.TypeOf((*int)(nil))},
		{SymChan(reflect.BothDir, symBasic(reflect.Int)), reflect.TypeOf(make(chan int))},
		{symStruct(symBasic(reflect.Int), symBasic(reflect.String)), reflect.TypeOf(struct {
			a int
			b string
		}{})},
		{symStruct(symBasic(reflect.Int), SymSlice(symBasic(reflect.Int))), reflect.TypeOf(struct {
			a int
			b []int
		}{})},
		{symBasic(reflect.Interface), reflect.TypeOf((*error)(nil)).Elem()},
	}
	for _, c := range cases {
		if c.typ.Rtype != nil {
			t.Fatalf("%v: precondition failed, Rtype must be nil", c.want)
		}
		if got, want := c.typ.IsComparable(), c.want.Comparable(); got != want {
			t.Errorf("%v: IsComparable()=%v, want %v", c.want, got, want)
		}
	}
}

func TestSymbolicIdentical(t *testing.T) {
	named := func(pkg, name string, k reflect.Kind) *Type { return &Type{PkgName: pkg, Name: name, kind: k} }
	cases := []struct {
		a, b *Type
		want bool
	}{
		{named("p", "T", reflect.Int), named("p", "T", reflect.Int), true},
		{named("p", "T", reflect.Int), named("p", "U", reflect.Int), false},
		{named("p", "T", reflect.Int), named("q", "T", reflect.Int), false},
		{SymSlice(symBasic(reflect.Int)), SymSlice(symBasic(reflect.Int)), true},
		{SymSlice(symBasic(reflect.Int)), SymSlice(symBasic(reflect.String)), false},
		{SymMap(symBasic(reflect.String), symBasic(reflect.Int)), SymMap(symBasic(reflect.String), symBasic(reflect.Int)), true},
		{SymArray(3, symBasic(reflect.Int)), SymArray(4, symBasic(reflect.Int)), false},
		{SymPtr(symBasic(reflect.Int)), SymPtr(symBasic(reflect.Int)), true},
		{symBasic(reflect.Int), symBasic(reflect.Int), true},
		{symBasic(reflect.Int), symBasic(reflect.String), false},
	}
	for i, c := range cases {
		if got := c.a.Identical(c.b); got != c.want {
			t.Errorf("case %d: Identical()=%v, want %v", i, got, c.want)
		}
	}
}

func TestSymbolicLenVariadic(t *testing.T) {
	if got := SymArray(5, symBasic(reflect.Int)).Len(); got != 5 {
		t.Errorf("symbolic Len()=%d, want 5", got)
	}
	if got := (&Type{kind: reflect.Func, Variadic: true}).IsVariadic(); !got {
		t.Errorf("symbolic IsVariadic()=false, want true")
	}
	if got := (&Type{kind: reflect.Func}).IsVariadic(); got {
		t.Errorf("symbolic IsVariadic()=true, want false")
	}
}

func TestSymVisibleFieldsPromotion(t *testing.T) {
	intT := &Type{Name: "int", kind: reflect.Int}
	a := &Type{Name: "A", kind: reflect.Int, Base: intT}
	inner := SymStruct([]*Type{a}, nil, nil)
	inner.Name = "Inner"
	innerField := &Type{Name: "Inner", kind: reflect.Struct, Fields: inner.Fields, Embedded: inner.Embedded, Base: inner}
	b := &Type{Name: "B", kind: reflect.Int, Base: intT}
	outer := SymStruct([]*Type{innerField, b}, []EmbeddedField{{FieldIdx: 0, Type: inner}}, nil)

	want := map[string][]int{"Inner": {0}, "A": {0, 0}, "B": {1}}
	got := map[string][]int{}
	for _, sf := range outer.symVisibleFields() {
		got[sf.name] = sf.index
	}
	if len(got) != len(want) {
		t.Fatalf("visible fields = %v, want keys %v", got, want)
	}
	for name, idx := range want {
		g, ok := got[name]
		if !ok || len(g) != len(idx) {
			t.Fatalf("field %s index = %v, want %v", name, g, idx)
		}
		for i := range idx {
			if g[i] != idx[i] {
				t.Fatalf("field %s index = %v, want %v", name, g, idx)
			}
		}
	}
	// FieldOffset: B sits after one int.
	if off := outer.FieldOffset([]int{1}); off != ptrSizeInt() {
		t.Errorf("Offsetof(B) = %d, want %d", off, ptrSizeInt())
	}
}

func ptrSizeInt() uintptr { return (&Type{kind: reflect.Int}).Size() }

// TestFieldTypeAtPathCloneCanonical guards the x/net/http2 promoted-method
// write-through bug: a field-access clone loses its Fields, so FieldTypeAtPath
// must resolve the embedded field's type via the canonical (Base).
func TestFieldTypeAtPathCloneCanonical(t *testing.T) {
	fh := &Type{Name: "FrameHeader", kind: reflect.Struct, Rtype: reflect.TypeOf(struct{ valid bool }{})}
	canon := &Type{
		Name: "HeadersFrame", kind: reflect.Struct,
		Rtype:  reflect.TypeOf(struct{ a int }{}),
		Fields: []*Type{fh},
	}
	clone := &Type{Name: "HeadersFrame", kind: reflect.Struct, Base: canon} // no Fields, no Rtype

	got := SymPtr(clone).FieldTypeAtPath([]int{0})
	if got == nil {
		t.Fatal("FieldTypeAtPath on *clone returned nil; want FrameHeader via canonical")
	}
	if got.Name != "FrameHeader" {
		t.Fatalf("FieldTypeAtPath = %q, want FrameHeader", got.Name)
	}
}
