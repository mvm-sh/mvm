package vm

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/internal/runtype"
)

type coerceErr struct{}

func (*coerceErr) Error() string { return "x" }

// TestCoerceInterfaceArgsReadOnly checks a read-only arg (from an unexported
// field) is made exportable, so a native reflect.Value.Call won't panic packing
// it (the io.Pipe writer deadlock: CloseWithError(err) on a flagRO error).
func TestCoerceInterfaceArgsReadOnly(t *testing.T) {
	type holder struct{ e *coerceErr } // unexported -> reads are read-only
	ro := reflect.ValueOf(holder{e: &coerceErr{}}).Field(0)
	if ro.CanInterface() {
		t.Fatal("setup: field value should be read-only")
	}

	in := []reflect.Value{ro}
	coerceInterfaceArgs(in, reflect.TypeOf(func(error) error { return nil }))

	if !in[0].CanInterface() {
		t.Fatal("read-only arg was not made exportable")
	}
	if _, ok := in[0].Interface().(*coerceErr); !ok {
		t.Fatalf("arg value/type changed: got %v", in[0].Type())
	}
}

// TestExportableReadOnlyFunc checks a read-only func value (from an unexported
// field) is made callable, as the native-defer and go-native paths now require.
func TestExportableReadOnlyFunc(t *testing.T) {
	called := false
	type holder struct{ fn func() }
	ro := reflect.ValueOf(holder{fn: func() { called = true }}).Field(0)
	if ro.CanInterface() {
		t.Fatal("setup: func field should be read-only")
	}

	fn := runtype.Exportable(ro)
	if !fn.CanInterface() {
		t.Fatal("read-only func value not made exportable")
	}
	fn.Call(nil) // would panic if still read-only
	if !called {
		t.Fatal("exported func value did not run")
	}
}
