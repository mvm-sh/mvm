package stdlib

import (
	"io"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// Reconcile the interpreted build's io.EOF (a distinct errors.New pointer) with
// the host io.EOF across the native boundary, so `err == io.EOF` holds either
// way. Lives here, not in vm: it is io-specific stub behavior. errorIface is
// declared in synth_method_shapes.go (same package).
func init() {
	vm.RegisterSentinelHooks(canonNativeEOF, mapInterpEOF)
}

var nativeIoEOF any = io.EOF

// canonNativeEOF rewrites a returned native io.EOF to the interpreted copy so an
// interpreted `err == io.EOF` succeeds (native->interp direction).
func canonNativeEOF(m *vm.Machine, out []reflect.Value) {
	eof := m.InterpEOFValue()
	if !eof.IsValid() {
		return
	}
	eofRV := eof.Reflect()
	for i, v := range out {
		if v.Kind() == reflect.Interface && !v.IsNil() && v.Interface() == nativeIoEOF {
			out[i] = eofRV
		}
	}
}

// mapInterpEOF maps an interpreted io.EOF return back to the host io.EOF for a
// native sink (io.Copy, io.ReadAll). It must run before bridgeIface, which on
// wasm would wrap the synth EOF in a synthErrShim that hides its identity.
func mapInterpEOF(m *vm.Machine, v vm.Value) (reflect.Value, bool) {
	eof := m.InterpEOFValue()
	if !eof.IsValid() || !v.IsValid() {
		return reflect.Value{}, false
	}
	if v.IsIface() {
		v = v.IfaceVal().Val
	}
	rv := v.Reflect()
	if !rv.IsValid() || !rv.Type().Implements(errorIface) || !rv.CanInterface() {
		return reflect.Value{}, false
	}
	e, _ := rv.Interface().(error)
	concrete, _ := eof.Interface().(error)
	if e != nil && e == concrete { //nolint:errorlint // sentinel identity, not a wrap
		return reflect.ValueOf(io.EOF), true
	}
	return reflect.Value{}, false
}
