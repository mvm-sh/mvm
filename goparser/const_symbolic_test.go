package goparser

import (
	"go/constant"
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/vm"
)

// The const-typing helpers must work on symbolic types whose Rtype is not yet
// materialized (the S1 goal): kind drives conversion, symbolic Size drives the
// overflow width. SymBasic builds a basic type with Rtype nil.

func TestTypedConstValueSymbolic(t *testing.T) {
	cases := []struct {
		cv   constant.Value
		kind reflect.Kind
		want any
	}{
		{constant.MakeInt64(5), reflect.Int8, int8(5)},
		{constant.MakeInt64(5), reflect.Uint16, uint16(5)},
		{constant.MakeFloat64(2.5), reflect.Float32, float32(2.5)},
		{constant.MakeString("hi"), reflect.String, "hi"},
		{constant.MakeBool(true), reflect.Bool, true},
	}
	for _, c := range cases {
		typ := mtype.SymBasic(c.kind)
		if typ.Rtype != nil {
			t.Fatalf("%v: precondition failed, Rtype must be nil", c.kind)
		}
		if got := typedConstValue(c.cv, typ); got != c.want {
			t.Errorf("typedConstValue(%v, %v) = %#v, want %#v", c.cv, c.kind, got, c.want)
		}
	}
}

// Regression: a const of unresolved-external type (OpaqueRtype, e.g. `^big.Word(0)`)
// must fold its basic value, not Convert to struct{} -- the `make generate` panic.
func TestTypedConstValueOpaque(t *testing.T) {
	typ := &mtype.Type{Name: "Word", Rtype: vm.OpaqueRtype}
	if got := typedConstValue(constant.MakeInt64(-1), typ); got != -1 {
		t.Errorf("typedConstValue(-1, Opaque Word) = %#v, want -1", got)
	}
}

func TestConstConvertSymbolic(t *testing.T) {
	trunc := constConvert(constant.MakeFloat64(3.7), mtype.SymBasic(reflect.Int))
	if n, ok := constant.Int64Val(trunc); !ok || n != 3 {
		t.Errorf("float->int truncation: got %v, want 3", trunc)
	}
	rune65 := constConvert(constant.MakeInt64(65), mtype.SymBasic(reflect.String))
	if rune65.Kind() != constant.String || constant.StringVal(rune65) != "A" {
		t.Errorf("int->string rune: got %v, want \"A\"", rune65)
	}
}

func TestOverflowsTypeSymbolic(t *testing.T) {
	cases := []struct {
		val  int64
		kind reflect.Kind
		want bool
	}{
		{200, reflect.Int8, true},
		{100, reflect.Int8, false},
		{256, reflect.Uint8, true},
		{255, reflect.Uint8, false},
		{70000, reflect.Int16, true},
		{30000, reflect.Int16, false},
	}
	for _, c := range cases {
		typ := mtype.SymBasic(c.kind)
		if got := OverflowsType(constant.MakeInt64(c.val), typ); got != c.want {
			t.Errorf("OverflowsType(%d, %v) = %v, want %v", c.val, c.kind, got, c.want)
		}
	}
}
