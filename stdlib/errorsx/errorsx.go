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

var errorRtype = reflect.TypeOf((*error)(nil)).Elem()

func init() {
	stdlib.RegisterPackagePatcher("errors", patchErrors)
	vm.RegisterArgProxy(mvmAs, 1, asTargetProxy)
}

func patchErrors(_ *vm.Machine, values map[string]vm.Value) {
	values["As"] = vm.FromReflect(reflect.ValueOf(mvmAs))
}

// asTarget carries an As target across the native-call boundary with its
// interpreted element type intact.
type asTarget struct {
	ptr      reflect.Value // the target pointer to assign through
	elemType *vm.Type      // interpreted type of *target (nil if unknown)
}

func asTargetProxy(_ *vm.Machine, ifc vm.Iface) reflect.Value {
	t := asTarget{ptr: ifc.Val.Reflect()}
	if ifc.Typ != nil {
		t.elemType = ifc.Typ.ElemType
	}
	return reflect.ValueOf(t)
}

// mvmAs replaces stderrors.As.
func mvmAs(err error, target any) bool {
	t, ok := target.(asTarget)
	if !ok {
		// Target not routed through asTargetProxy (e.g. a native caller).
		t = asTarget{ptr: reflect.ValueOf(target)}
	}
	if err == nil {
		return false
	}
	ptr := t.ptr
	if !ptr.IsValid() {
		panic("errors: target cannot be nil")
	}
	if ptr.Kind() != reflect.Pointer || ptr.IsNil() {
		panic("errors: target must be a non-nil pointer")
	}
	targetType := ptr.Type().Elem()
	// errors.As requires *target to be an interface or to implement error.
	// Only a basic-kind target (int, string, ...) can never implement error;
	// reject those. Composite targets (struct/pointer/slice) may carry an
	// interpreted method set invisible to native reflect, so accept them
	// unless their basic-kind elem clearly can't (the elemType methods check
	// covers a named basic type such as `type code int` with an Error method).
	if isBasicKind(targetType.Kind()) && !errorImplementer(t.elemType, targetType) {
		panic("errors: *target must be interface or implement error")
	}
	return asWalk(err, ptr, targetType, t.elemType)
}

func isBasicKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128, reflect.String:
		return true
	}
	return false
}

func errorImplementer(elemType *vm.Type, rtype reflect.Type) bool {
	if rtype.Implements(errorRtype) {
		return true
	}
	if elemType != nil {
		for _, m := range elemType.Methods {
			if m.IsResolved() {
				return true
			}
		}
	}
	return false
}

func asWalk(err error, targetVal reflect.Value, targetType reflect.Type, elemType *vm.Type) bool {
	for err != nil {
		v := reflect.ValueOf(err)
		if uv := vm.UnbridgeValue(v); uv.IsValid() {
			v = uv
		}
		if v.IsValid() && asAssignable(v, targetType, elemType) {
			targetVal.Elem().Set(v)
			return true
		}
		if x, ok := err.(interface{ As(any) bool }); ok && x.As(targetVal.Interface()) {
			return true
		}
		switch x := err.(type) { //nolint:errorlint // this IS the As/Unwrap walker
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		case interface{ Unwrap() []error }:
			for _, e := range x.Unwrap() {
				if e == nil {
					continue
				}
				if asWalk(e, targetVal, targetType, elemType) {
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

func asAssignable(v reflect.Value, targetType reflect.Type, elemType *vm.Type) bool {
	if targetType.Kind() != reflect.Interface || targetType.NumMethod() > 0 {
		return v.Type().AssignableTo(targetType)
	}
	if elemType == nil || len(elemType.IfaceMethods) == 0 {
		return true // interface{} / any: every value matches
	}
	return elemType.MissingMethod(v.Type()) == ""
}
