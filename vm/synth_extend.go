package vm

import (
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// SynthCall bundles one synth method dispatch so an out-of-vm shape handler can
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

// All synth method shapes (generic and package-specific) live in stdlib so vm
// core holds no synth-shape knowledge; stdlib registers them via init().
var (
	detectShapeHook func(reflect.Type) (stubs.Shape, bool)
	makeHandlerHook func(SynthCall, stubs.Shape) any
)

// RegisterSynthShapes plugs the synth method shapes into the bridge. detect
// classifies a method signature into a stubs.Shape; makeHandler builds the native
// callback for a classified shape.
func RegisterSynthShapes(
	detect func(reflect.Type) (stubs.Shape, bool),
	makeHandler func(SynthCall, stubs.Shape) any,
) {
	detectShapeHook = detect
	makeHandlerHook = makeHandler
}

// detectShape classifies a method signature into the matching stubs.Shape, via
// the stdlib-registered classifier; (0, false) when stdlib is not linked.
func detectShape(sig reflect.Type) (stubs.Shape, bool) {
	if detectShapeHook == nil {
		return 0, false
	}
	return detectShapeHook(sig)
}
