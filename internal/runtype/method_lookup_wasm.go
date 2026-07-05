//go:build wasm

package runtype

import "reflect"

// Interface Methods carry no Func, so stock reflect never resolves a textOff
// for them; everything else goes through the abi walk (see method_lookup.go).

// TypeMethodByName is reflect.Type.MethodByName, wasm-GC-safe on wasm.
func TypeMethodByName(t reflect.Type, name string) (reflect.Method, bool) {
	if t.Kind() == reflect.Interface {
		return t.MethodByName(name)
	}
	return typeMethodByNameABI(t, name)
}

// TypeHasMethodByName reports whether t has an exported method named name.
func TypeHasMethodByName(t reflect.Type, name string) bool {
	if t.Kind() == reflect.Interface {
		_, ok := t.MethodByName(name)
		return ok
	}
	return typeHasMethodByNameABI(t, name)
}

// TypeMethodNames lists t's exported method names in method-table order.
func TypeMethodNames(t reflect.Type) []string {
	if t.Kind() == reflect.Interface {
		names := make([]string, t.NumMethod())
		for i := range names {
			names[i] = t.Method(i).Name
		}
		return names
	}
	return typeMethodNamesABI(t)
}

// TypeMethods lists t's exported methods in method-table order.
func TypeMethods(t reflect.Type) []reflect.Method {
	if t.Kind() == reflect.Interface {
		out := make([]reflect.Method, t.NumMethod())
		for i := range out {
			out[i] = t.Method(i)
		}
		return out
	}
	return typeMethodsABI(t)
}

// ValueMethodByName is reflect.Value.MethodByName, wasm-GC-safe on wasm.
// A stock bound method value spills the PC at every Call (methodReceiver);
// bindABIMethod routes the call through the forged unbound Func instead.
// Interface receivers stay stock: methodReceiver reads their PC in place
// from the itab, never spilling it.
func ValueMethodByName(v reflect.Value, name string) reflect.Value {
	if !v.IsValid() || v.Kind() == reflect.Interface {
		return v.MethodByName(name)
	}
	m, ok := typeMethodByNameABI(v.Type(), name)
	if !ok {
		return reflect.Value{}
	}
	return bindABIMethod(m, v)
}

// ValueMethod is reflect.Value.Method, wasm-GC-safe on wasm.
func ValueMethod(v reflect.Value, i int) reflect.Value {
	if !v.IsValid() || v.Kind() == reflect.Interface {
		return v.Method(i)
	}
	m, ok := typeMethodABI(v.Type(), i)
	if !ok {
		return v.Method(i) // out of range: keep stock panic behavior
	}
	return bindABIMethod(m, v)
}
