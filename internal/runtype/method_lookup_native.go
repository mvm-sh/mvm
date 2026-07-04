//go:build !wasm

package runtype

import "reflect"

// Native code PCs never alias the GC arena; stock reflect is fine here.

// TypeMethodByName is reflect.Type.MethodByName, wasm-GC-safe on wasm.
func TypeMethodByName(t reflect.Type, name string) (reflect.Method, bool) {
	return t.MethodByName(name)
}

// TypeHasMethodByName reports whether t has an exported method named name.
func TypeHasMethodByName(t reflect.Type, name string) bool {
	_, ok := t.MethodByName(name)
	return ok
}

// TypeMethodNames lists t's exported method names in method-table order.
func TypeMethodNames(t reflect.Type) []string {
	names := make([]string, t.NumMethod())
	for i := range names {
		names[i] = t.Method(i).Name
	}
	return names
}

// TypeMethods lists t's exported methods in method-table order.
func TypeMethods(t reflect.Type) []reflect.Method {
	out := make([]reflect.Method, t.NumMethod())
	for i := range out {
		out[i] = t.Method(i)
	}
	return out
}

// ValueMethodByName is reflect.Value.MethodByName, wasm-GC-safe on wasm.
func ValueMethodByName(v reflect.Value, name string) reflect.Value {
	return v.MethodByName(name)
}
