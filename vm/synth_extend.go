package vm

import (
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// SynthCall bundles one synth method dispatch so an out-of-vm bridge handler can
// re-enter the interpreter without reaching into vm internals.
type SynthCall struct {
	m      *Machine
	t      *Type
	name   string
	method Method
	form   recvForm
}

// Invoke runs the bundled method, with recv as the receiver storage and args the
// already reflect-boxed arguments; it returns the raw result values.
func (c SynthCall) Invoke(recv unsafe.Pointer, args []reflect.Value) ([]reflect.Value, error) {
	rv := makeRecvValue(c.t.Rtype, recv, c.form)
	return callMethod(c.m, c.t, c.name, rv, c.method, c.method.Rtype, args)
}

// Extended shapes are the stdlib-specific method shapes (io/fs, log/slog,
// encoding/xml); stdlib registers them so vm core need not import those packages.
var (
	detectExtendedShape func(reflect.Type) (stubs.Shape, bool)
	makeExtendedHandler func(SynthCall, stubs.Shape) any
)

// RegisterExtendedShapes plugs the stdlib-specific synth method shapes into the
// bridge. detect classifies a method signature; makeHandler builds the native
// callback for a classified shape.
func RegisterExtendedShapes(
	detect func(reflect.Type) (stubs.Shape, bool),
	makeHandler func(SynthCall, stubs.Shape) any,
) {
	detectExtendedShape = detect
	makeExtendedHandler = makeHandler
}
