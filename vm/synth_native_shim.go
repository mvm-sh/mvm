package vm

import (
	"fmt"
	"reflect"
)

// On the shared-PC (wasm) build a synth method's text PC is the -1 unreachable
// sentinel, so native code cannot dispatch it (MODE 2). When a synth value
// implementing error/Stringer crosses into a native interface sink -- e.g.
// testing's native fmt.Sprintf -- wrap it in a native shim whose method has a
// real PC and re-enters the interpreter via callMethod.

type synthErrShim struct {
	m  *Machine
	rv reflect.Value
}

func (s synthErrShim) Error() string { return s.m.callSynthString(s.rv, "Error") }

type synthStrShim struct {
	m  *Machine
	rv reflect.Value
}

func (s synthStrShim) String() string { return s.m.callSynthString(s.rv, "String") }

var (
	synthErrShimType = reflect.TypeFor[synthErrShim]()
	synthStrShimType = reflect.TypeFor[synthStrShim]()
	stringerIface    = reflect.TypeFor[fmt.Stringer]()
)

func (m *Machine) callSynthString(rv reflect.Value, name string) string {
	t := m.typeByRtype(rv.Type())
	if t == nil {
		return ""
	}
	method, ok := m.MethodByName(t, name)
	if !ok {
		return ""
	}
	out, err := callMethod(m, t, name, rv, method, method.Rtype, nil)
	if err != nil || len(out) == 0 {
		return ""
	}
	return out[0].String()
}

// wrapSynthIfaceForNative returns a native forwarding shim for a synth
// error/Stringer reaching a native interface target, or ok=false to leave the
// value unchanged. error wins over Stringer to match fmt's dispatch order.
func (m *Machine) wrapSynthIfaceForNative(val, targetType reflect.Type, rv reflect.Value) (reflect.Value, bool) {
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		rv = rv.Elem()
		val = rv.Type()
	}
	if !isSynthOrSynthPtr(val) {
		return reflect.Value{}, false
	}
	if val.Implements(errorIface) && synthErrShimType.AssignableTo(targetType) {
		return reflect.ValueOf(synthErrShim{m, rv}), true
	}
	if val.Implements(stringerIface) && synthStrShimType.AssignableTo(targetType) {
		return reflect.ValueOf(synthStrShim{m, rv}), true
	}
	return reflect.Value{}, false
}
