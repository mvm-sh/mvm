package vm

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/mtype"
)

type unboxWrapper struct{ V int32 }

func (*unboxWrapper) M0() {}
func (*unboxWrapper) M1() {}

// A method-bearing concrete pointer boxed in an mvm Iface, stored into an empty
// interface, must unwrap to the raw concrete eface (not the Iface struct), so
// native reflect/unsafe reads see a real Go value. Without the fix the Iface
// stays boxed and a native eface read (e.g. protobuf's oneof field coder via
// reflect.NewAt) panics "reflect.Value.IsNil on struct Value".
func TestUnboxIfaceForEmptyInterfaceMethodBearing(t *testing.T) {
	m := &Machine{}
	concrete := (*unboxWrapper)(nil) // typed-nil pointer, as in TestEncodeOneofNilWrapper
	ptrRtype := reflect.TypeOf(concrete)

	typ := &mtype.Type{
		Rtype:   ptrRtype,                // *vm.unboxWrapper: Kind Pointer (not Func), Name ""
		Methods: make([]mtype.Method, 2), // non-empty: skips the methodless-unwrap path
	}
	val := Value{ref: reflect.ValueOf(Iface{Typ: typ, Val: FromReflect(reflect.ValueOf(concrete))})}
	if !val.IsIface() {
		t.Fatal("val should be an Iface")
	}

	emptyIface := reflect.TypeOf((*any)(nil)).Elem()
	got, ok := m.unboxIfaceFor(val, emptyIface)
	if !ok {
		t.Fatal("unboxIfaceFor: want ok for method-bearing concrete into empty interface")
	}
	if got.Type() == ifaceRtype {
		t.Fatalf("unboxIfaceFor returned a boxed Iface struct; want raw concrete %v", ptrRtype)
	}
	if got.Kind() != reflect.Pointer || got.Type() != ptrRtype {
		t.Fatalf("unboxIfaceFor = %v (kind %v), want %v", got.Type(), got.Kind(), ptrRtype)
	}

	// End-to-end through setFuncField: an empty-interface struct field must hold a
	// real eface so a reflect read sees the pointer concrete, not a vm.Iface struct.
	field := reflect.New(reflect.StructOf([]reflect.StructField{
		{Name: "F", Type: emptyIface},
	})).Elem().Field(0)
	m.setFuncField(field, val)
	if got := field.Elem().Type(); got != ptrRtype {
		t.Fatalf("field concrete = %v, want %v (double-boxed Iface without the fix)", got, ptrRtype)
	}
}
