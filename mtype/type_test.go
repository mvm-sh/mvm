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
