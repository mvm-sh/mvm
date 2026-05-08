// Package errorsx is a mvm-aware replacement for the parts of stdlib
// `errors` that need to walk error chains containing interpreted error
// types. Native stderrors.As panics on a target whose rtype lacks the
// Error method, which is the case for any interpreted error struct
// (its rtype comes from reflect.StructOf, which has no method set).
// errorsx replaces errors.As at import time so the chain walk uses
// mvm-aware type information and routes through bridge wrappers
// transparently.
package errorsx

import (
	"reflect"

	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/vm"
)

func init() {
	stdlib.RegisterPackagePatcher("errors", patchErrors)
	// Argument 1 of mvmAs is the target pointer. The default native-call
	// boundary would bridge a mvm Iface as e.g. *BridgeError; instead we
	// need the raw pointer so mvmAs can dereference and assign through it.
	vm.RegisterArgProxy(mvmAs, 1, stdlib.PassthroughIface)
}

func patchErrors(_ *vm.Machine, values map[string]vm.Value) {
	values["As"] = vm.FromReflect(reflect.ValueOf(mvmAs))
}

// mvmAs replaces stderrors.As. It walks err's chain (Unwrap and
// Unwrap-[]error) and at each layer extracts the underlying interpreted
// value via vm.UnbridgeValue when the layer is a bridge wrapper. The
// first layer whose value is assignable to *target's element type sets
// the target and returns true.
func mvmAs(err error, target any) bool {
	if err == nil {
		return false
	}
	if target == nil {
		panic("errors: target cannot be nil")
	}
	rv := reflect.ValueOf(target)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		panic("errors: target must be a non-nil pointer")
	}
	return asWalk(err, rv, rv.Type().Elem())
}

func asWalk(err error, targetVal reflect.Value, targetType reflect.Type) bool {
	for err != nil {
		v := reflect.ValueOf(err)
		if uv := vm.UnbridgeValue(v); uv.IsValid() {
			v = uv
		}
		if v.IsValid() && v.Type().AssignableTo(targetType) {
			targetVal.Elem().Set(v)
			return true
		}
		if x, ok := err.(interface{ As(any) bool }); ok && x.As(targetVal.Interface()) {
			return true
		}
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		case interface{ Unwrap() []error }:
			for _, e := range x.Unwrap() {
				if e == nil {
					continue
				}
				if asWalk(e, targetVal, targetType) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}
