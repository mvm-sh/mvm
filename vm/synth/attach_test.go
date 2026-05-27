package synth

import (
	"fmt"
	"reflect"
	"testing"
	"unsafe"
)

// TestAttachStructMethodsStringer end-to-ends shape S1 through real iface
// dispatch on a synth struct.
func TestAttachStructMethodsStringer(t *testing.T) {
	type layout struct {
		V int
	}

	called := false
	handler := func(recv unsafe.Pointer) string {
		called = true
		v := (*layout)(recv)
		return fmt.Sprintf("V=%d", v.V)
	}

	synthT, err := AttachStructMethods(
		reflect.StructOf([]reflect.StructField{
			{Name: "V", Type: reflect.TypeOf(int(0))},
		}),
		"layout",
		"test",
		Method{
			Name:     "String",
			Exported: true,
			Sig:      reflect.TypeOf((func() string)(nil)),
			Handler:  handler,
		},
	)
	if err != nil {
		t.Fatalf("AttachStructMethods: %v", err)
	}

	if got, want := synthT.NumMethod(), 1; got != want {
		t.Errorf("NumMethod = %d, want %d", got, want)
	}
	if m := synthT.Method(0); m.Name != "String" {
		t.Errorf("Method(0).Name = %q, want %q", m.Name, "String")
	}

	stringerT := reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
	if !synthT.Implements(stringerT) {
		t.Fatal("synthT.Implements(fmt.Stringer) = false, want true")
	}

	v := reflect.New(synthT).Elem()
	v.Field(0).SetInt(42)

	s, ok := v.Interface().(fmt.Stringer)
	if !ok {
		t.Fatal("type assertion to fmt.Stringer failed")
	}
	if got, want := s.String(), "V=42"; got != want {
		t.Errorf("s.String() = %q, want %q", got, want)
	}
	if !called {
		t.Error("handler was not invoked")
	}
}

// TestAttachStructMethodsName pins the Phase 2c name-stamping: the synth
// struct's Name()/String() must reflect the caller-supplied name, not the
// source layout's name (which is "" for reflect.StructOf-built layouts).
func TestAttachStructMethodsName(t *testing.T) {
	rt, err := AttachStructMethods(
		reflect.StructOf([]reflect.StructField{
			{Name: "V", Type: reflect.TypeOf(int(0))},
		}),
		"MyStruct",
		"test",
		Method{
			Name:     "String",
			Exported: true,
			Sig:      reflect.TypeOf((func() string)(nil)),
			Handler:  func(unsafe.Pointer) string { return "" },
		},
	)
	if err != nil {
		t.Fatalf("AttachStructMethods: %v", err)
	}
	if got, want := rt.Name(), "MyStruct"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := rt.String(), "MyStruct"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestAttachStructMethodsRejectsNonStruct(t *testing.T) {
	_, err := AttachStructMethods(
		reflect.TypeOf(int(0)),
		"badKind",
		"test",
		Method{
			Name:    "String",
			Sig:     reflect.TypeOf((func() string)(nil)),
			Handler: func(unsafe.Pointer) string { return "" },
		},
	)
	if err == nil {
		t.Fatal("expected error for non-struct layout, got nil")
	}
}

func TestSlotPoolDistinctSlots(t *testing.T) {
	mk := func(tag string) (reflect.Type, *bool) {
		called := new(bool)
		rt, err := AttachStructMethods(
			reflect.StructOf([]reflect.StructField{
				{Name: "V", Type: reflect.TypeOf(int(0))},
			}),
			"slotPool_"+tag,
			"test",
			Method{
				Name:     "String",
				Exported: true,
				Sig:      reflect.TypeOf((func() string)(nil)),
				Handler: func(unsafe.Pointer) string {
					*called = true
					return tag
				},
			},
		)
		if err != nil {
			t.Fatalf("AttachStructMethods(%s): %v", tag, err)
		}
		return rt, called
	}

	rt1, called1 := mk("first")
	rt2, called2 := mk("second")

	if rt1 == rt2 {
		t.Fatal("two distinct synth types compared equal")
	}

	v1 := reflect.New(rt1).Elem().Interface().(fmt.Stringer)
	v2 := reflect.New(rt2).Elem().Interface().(fmt.Stringer)

	if got := v1.String(); got != "first" {
		t.Errorf("v1.String() = %q, want first", got)
	}
	if got := v2.String(); got != "second" {
		t.Errorf("v2.String() = %q, want second", got)
	}
	if !*called1 || !*called2 {
		t.Error("handlers not both invoked")
	}
}
