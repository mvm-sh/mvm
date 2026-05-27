package synth

import "unsafe"

// funcPC returns the entry PC of a Go function value.
// A func value is a pointer to a function descriptor whose first word is the
// entry PC.
// We avoid linknaming internal/abi.FuncPCABI0 to keep the package independent
// of -checklinkname=0.
func funcPC(fn any) uintptr {
	type iface struct{ tab, data unsafe.Pointer }
	return *(*uintptr)((*iface)(unsafe.Pointer(&fn)).data)
}

// ptrFromPC reinterprets a function-entry PC as unsafe.Pointer for
// addReflectOff.
// addReflectOff records the value verbatim and needs no GC visibility, so
// this conversion is sound.
//
//nolint:govet // unsafeptr: addReflectOff just records the value, no deref
func ptrFromPC(pc uintptr) unsafe.Pointer {
	return unsafe.Pointer(pc)
}
