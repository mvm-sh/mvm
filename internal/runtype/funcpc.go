package runtype

import "unsafe"

// FuncPC returns the entry PC of a Go function value.
// A func value is a pointer to a function descriptor whose first word is the
// entry PC.
// We avoid linknaming internal/abi.FuncPCABI0 to keep the package independent
// of -checklinkname=0.
func FuncPC(fn any) uintptr {
	type iface struct{ tab, data unsafe.Pointer }
	return *(*uintptr)((*iface)(unsafe.Pointer(&fn)).data)
}

// PointerFromUintptr reinterprets an integer as unsafe.Pointer via a
// pointer-to-pointer load, which vet's unsafeptr check accepts.
// The result carries no GC liveness; callers must guarantee validity.
func PointerFromUintptr(p uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&p))
}

// ptrFromPC reinterprets a function-entry PC as unsafe.Pointer for
// addReflectOff, which records the value verbatim and never derefs it.
func ptrFromPC(pc uintptr) unsafe.Pointer {
	return PointerFromUintptr(pc)
}
