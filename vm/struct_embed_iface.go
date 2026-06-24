package vm

import (
	"reflect"
	"sync"
	"unsafe"

	"github.com/mvm-sh/mvm/runtype"
	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// bridgeStructEmbedIface builds, on demand, a reserved synth rtype over the
// struct's layout whose methods forward to the embedded interface field's
// value, then re-types the arg to it so the native callee can invoke the promoted methods.
//
// The work is gated to the rare miss path and memoized per (layout, target).

type structEmbedKey struct {
	layout, target reflect.Type
	field          int // carrier field index; the synth methods forward to it
}

// structEmbedIfaceCache memoizes the synth rtype per key.
var structEmbedIfaceCache sync.Map // structEmbedKey -> reflect.Type

func (m *Machine) bridgeStructEmbedIface(rv reflect.Value, target reflect.Type) reflect.Value {
	if !rv.IsValid() || rv.Kind() != reflect.Struct ||
		target.Kind() != reflect.Interface || target.NumMethod() == 0 {
		return reflect.Value{}
	}
	layout := rv.Type()
	if layout.Implements(target) {
		return reflect.Value{} // genuine native promotion; the normal path is correct
	}
	// The carrier field is found by the dynamic value, so this runs outside the cache.
	fieldIdx := m.embedFieldSatisfying(rv, target)
	if fieldIdx < 0 {
		return reflect.Value{}
	}
	key := structEmbedKey{layout, target, fieldIdx}
	var synthRT reflect.Type
	if e, ok := structEmbedIfaceCache.Load(key); ok {
		synthRT, _ = e.(reflect.Type)
	} else {
		synthRT = m.buildStructEmbedSynth(layout, target, fieldIdx)
		structEmbedIfaceCache.Store(key, synthRT)
	}
	if synthRT == nil {
		return reflect.Value{}
	}
	return retypeStruct(rv, synthRT)
}

func (m *Machine) buildStructEmbedSynth(layout, target reflect.Type, fieldIdx int) reflect.Type {
	res, err := runtype.ReserveMethods(layout, layout.String(), "")
	if err != nil {
		return nil
	}
	synthT := &Type{Rtype: res.Type()} // Kind() derives Struct from Rtype
	specs := make([]synthMethodSpec, 0, target.NumMethod())
	for meth := range target.Methods() {
		sig := meth.Type // interface method type: no receiver
		spec := synthMethodSpec{
			name:   meth.Name,
			method: Method{Index: -1, Path: []int{fieldIdx}, Rtype: sig},
			form:   recvFormFor(res.Type(), false, false),
		}
		if !spec.resolveDispatch(eraseSynthIfaceParams(sig), sig) {
			return nil
		}
		specs = append(specs, spec)
	}
	if err := stubs.FillMethods(res, toSynthMethods(m, synthT, specs)); err != nil {
		return nil
	}
	return res.Type()
}

func (m *Machine) embedFieldSatisfying(rv reflect.Value, target reflect.Type) int {
	for i := 0; i < rv.NumField(); i++ {
		if rv.Type().Field(i).Type.Kind() != reflect.Interface {
			continue
		}
		concrete := dynamicConcrete(Exportable(rv.Field(i)))
		if concrete.IsValid() && concrete.Type().Implements(target) {
			return i
		}
	}
	return -1
}

func dynamicConcrete(fv reflect.Value) reflect.Value {
	if !fv.IsValid() {
		return reflect.Value{}
	}
	if fv.Kind() == reflect.Interface {
		if fv.IsNil() {
			return reflect.Value{}
		}
		fv = unwrapIface(fv)
	}
	if fv.IsValid() && fv.Type() == ifaceRtype {
		return Exportable(fv).Interface().(Iface).Val.Reflect()
	}
	return fv
}

func retypeStruct(rv reflect.Value, synthRT reflect.Type) reflect.Value {
	rv = Exportable(rv)
	if rv.CanAddr() {
		return reflect.NewAt(synthRT, unsafe.Pointer(rv.UnsafeAddr())).Elem()
	}
	tmp := reflect.New(rv.Type()).Elem()
	tmp.Set(rv)
	return reflect.NewAt(synthRT, tmp.Addr().UnsafePointer()).Elem()
}
