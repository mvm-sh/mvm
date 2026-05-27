package synth

import (
	"fmt"
	"reflect"
	"testing"
	"unsafe"
)

func TestAttachPtrMethodsStringer(t *testing.T) {
	type layout struct {
		V int
	}
	layoutT, err := AttachStructMethods(
		reflect.StructOf([]reflect.StructField{
			{Name: "V", Type: reflect.TypeOf(int(0))},
		}),
		"test",
		Method{
			Name:     "_marker",
			Exported: false,
			Sig:      reflect.TypeOf((func() string)(nil)),
			Handler:  func(unsafe.Pointer) string { return "" },
		},
	)
	if err != nil {
		t.Fatalf("AttachStructMethods (T): %v", err)
	}

	called := false
	handler := func(recv unsafe.Pointer) string {
		called = true
		l := (*layout)(recv)
		return fmt.Sprintf("ptr V=%d", l.V)
	}
	ptrT, err := AttachPtrMethods(layoutT, "*T", "test", Method{
		Name:     "String",
		Exported: true,
		Sig:      reflect.TypeOf((func() string)(nil)),
		Handler:  handler,
	})
	if err != nil {
		t.Fatalf("AttachPtrMethods: %v", err)
	}

	if got, want := ptrT.Kind(), reflect.Pointer; got != want {
		t.Errorf("ptrT.Kind() = %v, want %v", got, want)
	}
	if ptrT.Elem() != layoutT {
		t.Errorf("ptrT.Elem() != layoutT")
	}
	if got, want := ptrT.NumMethod(), 1; got != want {
		t.Errorf("ptrT.NumMethod() = %d, want %d", got, want)
	}

	// PtrToThis wiring: reflect.PointerTo(layoutT) returns our synth *T.
	if got := reflect.PointerTo(layoutT); got != ptrT {
		t.Errorf("PointerTo(layoutT) != ptrT: got %v, want %v", got, ptrT)
	}

	stringerT := reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
	if !ptrT.Implements(stringerT) {
		t.Fatal("ptrT.Implements(fmt.Stringer) = false")
	}

	v := reflect.New(layoutT).Elem()
	v.Field(0).SetInt(7)
	s, ok := v.Addr().Interface().(fmt.Stringer)
	if !ok {
		t.Fatal("v.Addr().Interface().(fmt.Stringer) failed")
	}
	if got, want := s.String(), "ptr V=7"; got != want {
		t.Errorf("s.String() = %q, want %q", got, want)
	}
	if !called {
		t.Error("handler not invoked")
	}
}
