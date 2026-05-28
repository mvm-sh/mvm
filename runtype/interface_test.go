package runtype

import (
	"reflect"
	"testing"
)

type hasTimeout struct{}

func (hasTimeout) Timeout() bool { return true }

type noTimeout struct{}

func (noTimeout) Error() string { return "x" }

func boolFuncSig() reflect.Type {
	return reflect.FuncOf(nil, []reflect.Type{reflect.TypeOf(true)}, false)
}

func TestInterfaceOfImplements(t *testing.T) {
	iface := InterfaceOf("interface { Timeout() bool }", "", []Imethod{
		{Name: "Timeout", Exported: true, Sig: boolFuncSig()},
	})
	if iface.Kind() != reflect.Interface {
		t.Fatalf("kind = %v, want Interface", iface.Kind())
	}
	if got := iface.NumMethod(); got != 1 {
		t.Fatalf("NumMethod = %d, want 1", got)
	}

	// Pointer and value receivers, native types.
	if !reflect.TypeOf(hasTimeout{}).Implements(iface) {
		t.Errorf("hasTimeout does not implement Timeout-iface")
	}
	if reflect.TypeOf(noTimeout{}).Implements(iface) {
		t.Errorf("noTimeout unexpectedly implements Timeout-iface")
	}
	if !reflect.TypeOf(hasTimeout{}).AssignableTo(iface) {
		t.Errorf("hasTimeout not assignable to Timeout-iface")
	}
}

func TestInterfaceOfSetRoundTrip(t *testing.T) {
	iface := InterfaceOf("interface { Timeout() bool }", "", []Imethod{
		{Name: "Timeout", Exported: true, Sig: boolFuncSig()},
	})
	// reflect.New(iface) gives a *iface; Set a concrete implementer through it
	// (this exercises itab construction for a synth interface), then read back.
	box := reflect.New(iface)
	box.Elem().Set(reflect.ValueOf(hasTimeout{}))
	got := box.Elem().Interface()
	if _, ok := got.(interface{ Timeout() bool }); !ok {
		t.Fatalf("round-tripped value %T does not satisfy Timeout", got)
	}
}

func TestInterfaceOfMultiMethodSorted(t *testing.T) {
	// Methods given out of order must still satisfy a multi-method check.
	strSig := reflect.FuncOf(nil, []reflect.Type{reflect.TypeOf("")}, false)
	iface := InterfaceOf("interface { Error() string; Timeout() bool }", "", []Imethod{
		{Name: "Timeout", Exported: true, Sig: boolFuncSig()},
		{Name: "Error", Exported: true, Sig: strSig},
	})
	if !reflect.TypeOf(bothMethods{}).Implements(iface) {
		t.Errorf("bothMethods does not implement {Error;Timeout}")
	}
	if reflect.TypeOf(hasTimeout{}).Implements(iface) {
		t.Errorf("hasTimeout unexpectedly implements {Error;Timeout}")
	}
}

type bothMethods struct{}

func (bothMethods) Error() string { return "b" }
func (bothMethods) Timeout() bool { return true }

func TestInterfaceOfEmpty(t *testing.T) {
	if got := InterfaceOf("", "", nil); got != reflect.TypeOf((*any)(nil)).Elem() {
		t.Errorf("empty InterfaceOf = %v, want any", got)
	}
}
