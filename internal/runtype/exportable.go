package runtype

import (
	"reflect"
	"unsafe"
)

// Exportable returns rv with its read-only flag cleared so that .Interface()
// and .Call() do not panic on values obtained from unexported struct fields.
func Exportable(rv reflect.Value) reflect.Value {
	if !rv.IsValid() || rv.CanInterface() {
		return rv
	}
	if rv.CanAddr() {
		return reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem()
	}
	// Non-addressable read-only value (typical: a method value taken off
	// an unexported field, or the result of an unexported func-typed field
	// access). Clear flagStickyRO and flagEmbedRO directly. The reflect.Value
	// layout has been stable across recent Go releases; if the bit positions
	// ever change, this will fail loudly via panic on the next .Interface()
	// rather than silently misbehave.
	// rvHeader mirrors reflect.Value's internal layout (typ, ptr, flag).
	type rvHeader struct {
		typ  unsafe.Pointer
		ptr  unsafe.Pointer
		flag uintptr
	}
	const flagRO = (1 << 5) | (1 << 6) // flagStickyRO | flagEmbedRO
	out := rv
	(*rvHeader)(unsafe.Pointer(&out)).flag &^= flagRO
	return out
}
