package stdlib

import (
	"io"
	"io/fs"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// Reconcile interpreted sentinels (a distinct errors.New pointer each) with the
// host ones across the native boundary, so `err == io.EOF` and a native
// syscall.Errno.Is(fs.ErrNotExist) succeed. Lives here, not in vm: it is
// stub-specific. errorIface is declared in synth_method_shapes.go (same package).
func init() {
	vm.RegisterSentinelHooks(mapInterpSentinel, hostSentinelValue)
}

// hostSentinelValue returns name's host sentinel as an error-typed value.
// The vm's healSentinels sweep overwrites the interpreted global with it after
// the var-inits, so both copies are one pointer everywhere.
func hostSentinelValue(name string) (reflect.Value, bool) {
	native, ok := nativeSentinels[name]
	if !ok {
		return reflect.Value{}, false
	}
	rv := reflect.New(errorIface).Elem()
	rv.Set(reflect.ValueOf(native))
	return rv, true
}

// nativeSentinels pairs each interpreted sentinel's registered name (see
// interp.configureSentinels) with the host value it must map to.
var nativeSentinels = map[string]any{
	"io.EOF":           io.EOF,
	"fs.ErrNotExist":   fs.ErrNotExist,
	"fs.ErrExist":      fs.ErrExist,
	"fs.ErrPermission": fs.ErrPermission,
	"fs.ErrClosed":     fs.ErrClosed,
	"fs.ErrInvalid":    fs.ErrInvalid,
	"fs.SkipDir":       fs.SkipDir,
	"fs.SkipAll":       fs.SkipAll,
}

// mapInterpSentinel maps an interpreted sentinel to its host value for a native
// sink (io.Copy) or a native-call arg (syscall.Errno.Is).
// It must run before bridgeIface, which on wasm would wrap the synth value in a
// synthErrShim that hides its identity.
// After the healSentinels sweep the slots hold the host values, so this only
// converts pre-heal escapes (a copy captured during package init).
func mapInterpSentinel(m *vm.Machine, v vm.Value) (reflect.Value, bool) {
	if m == nil || !v.IsValid() {
		return reflect.Value{}, false
	}
	if v.IsIface() {
		v = v.IfaceVal().Val
	}
	rv := v.Reflect()
	// A returned interpreted error often arrives boxed in an eface, which doesn't implement error; unwrap to the concrete.
	if rv.IsValid() && rv.Kind() == reflect.Interface && !rv.IsNil() {
		rv = rv.Elem()
	}
	if !rv.IsValid() || !rv.Type().Implements(errorIface) || !rv.CanInterface() {
		return reflect.Value{}, false
	}
	e, _ := rv.Interface().(error)
	if e == nil {
		return reflect.Value{}, false
	}
	for name, native := range nativeSentinels {
		s := m.InterpSentinelValue(name)
		if !s.IsValid() {
			continue
		}
		concrete, _ := s.Interface().(error)
		if concrete != nil && e == concrete { //nolint:errorlint // sentinel identity, not a wrap
			return reflect.ValueOf(native), true
		}
	}
	return reflect.Value{}, false
}
