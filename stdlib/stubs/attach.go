package stubs

import (
	"reflect"

	"github.com/mvm-sh/mvm/runtype"
)

type attachFunc = func(reflect.Type, string, string, []runtype.MethodSpec) (reflect.Type, error)

// AttachMethods resolves each method's shape to a stub slot, then dispatches by
// layout kind to runtype.
func AttachMethods(layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	return attach(runtype.AttachMethods, layout, name, pkgPath, methods)
}

// AttachStructMethods is the struct-layout entry point.
func AttachStructMethods(layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	return attach(runtype.AttachStructMethods, layout, name, pkgPath, methods)
}

// AttachPrimitiveMethods is the named-primitive entry point.
func AttachPrimitiveMethods(layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	return attach(runtype.AttachPrimitiveMethods, layout, name, pkgPath, methods)
}

// AttachSliceMethods is the slice-layout entry point.
func AttachSliceMethods(layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	return attach(runtype.AttachSliceMethods, layout, name, pkgPath, methods)
}

// AttachArrayMethods is the array-layout entry point.
func AttachArrayMethods(layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	return attach(runtype.AttachArrayMethods, layout, name, pkgPath, methods)
}

// AttachMapMethods is the map-layout entry point.
func AttachMapMethods(layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	return attach(runtype.AttachMapMethods, layout, name, pkgPath, methods)
}

// AttachFuncMethods is the func-layout entry point.
func AttachFuncMethods(layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	return attach(runtype.AttachFuncMethods, layout, name, pkgPath, methods)
}

// AttachPtrMethods is the pointer-receiver entry point; it wires *T back so
// reflect.PointerTo(elem) returns the method-bearing *T.
func AttachPtrMethods(elem reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	return attach(runtype.AttachPtrMethods, elem, name, pkgPath, methods)
}

// FillMethods installs methods into a reserved rtype in place (cascade-retiring
// reserve/fill path), resolving each method's stub slot first like attach does.
func FillMethods(res *runtype.Reservation, methods []Method) error {
	stubs, err := acquireSlots(methods)
	if err != nil {
		return err
	}
	specs := make([]runtype.MethodSpec, len(methods))
	for i, m := range methods {
		specs[i] = runtype.MethodSpec{
			Name:     m.Name,
			Exported: m.Exported,
			Sig:      m.Sig,
			StubPC:   stubs[i],
		}
	}
	return res.Fill(specs)
}

func attach(fn attachFunc, layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	stubs, err := acquireSlots(methods)
	if err != nil {
		return nil, err
	}
	specs := make([]runtype.MethodSpec, len(methods))
	for i, m := range methods {
		specs[i] = runtype.MethodSpec{
			Name:     m.Name,
			Exported: m.Exported,
			Sig:      m.Sig,
			StubPC:   stubs[i],
		}
	}
	return fn(layout, name, pkgPath, specs)
}

// acquireSlots claims one stub-pool slot per method, returning the slot PCs.
// On mid-batch failure it releases the handlers already claimed (freeing their
// closure captures); the slot indices stay consumed, as counters are monotonic.
func acquireSlots(methods []Method) ([]uintptr, error) {
	stubs := make([]uintptr, len(methods))
	releases := make([]func(), 0, len(methods))
	for i, m := range methods {
		pc, release, err := acquireSlot(m)
		if err != nil {
			for _, r := range releases {
				r()
			}
			return nil, err
		}
		stubs[i] = pc
		releases = append(releases, release)
	}
	return stubs, nil
}
