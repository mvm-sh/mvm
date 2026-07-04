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
// Stock Value.MethodByName resolves the index via Type.MethodByName, which
// spills the method PC; the abi index into Value.Method avoids that.
func ValueMethodByName(v reflect.Value, name string) reflect.Value {
	if !v.IsValid() || v.Kind() == reflect.Interface {
		return v.MethodByName(name)
	}
	i := exportedMethodIndexABI(v.Type(), name)
	if i < 0 {
		return reflect.Value{}
	}
	return v.Method(i)
}
