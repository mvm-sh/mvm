package runtype

import (
	"reflect"
	"unsafe"

	_ "unsafe" // for go:linkname
)

// addReflectOff registers a pointer into the runtime's reflect-offset table.
// Returns the corresponding NameOff / TypeOff / TextOff.
// reflect tags this symbol with `//go:linkname addReflectOff` at its
// definition, permitting external access without -checklinkname=0 on go1.23+.
//
//go:linkname addReflectOff reflect.addReflectOff
//go:noescape
func addReflectOff(ptr unsafe.Pointer) int32

// The resolve*Off trio mirrors reflect's declarations; the runtime implements
// and push-linknames them (runtime1.go reflect_resolve*Off), so pulling them
// here needs no -checklinkname=0.

//go:linkname resolveNameOff reflect.resolveNameOff
//go:noescape
func resolveNameOff(ptrInModule unsafe.Pointer, off int32) unsafe.Pointer

//go:linkname resolveTypeOff reflect.resolveTypeOff
//go:noescape
func resolveTypeOff(rtype unsafe.Pointer, off int32) unsafe.Pointer

//go:linkname resolveTextOff reflect.resolveTextOff
//go:noescape
func resolveTextOff(rtype unsafe.Pointer, off int32) unsafe.Pointer

// rtypePtr extracts the *abiType from a reflect.Type interface value.
// reflect.Type's interface header is (itab, data); the data word is the *rtype.
//
//go:nosplit
func rtypePtr(t reflect.Type) *abiType {
	if t == nil {
		return nil
	}
	return (*abiType)((*[2]unsafe.Pointer)(unsafe.Pointer(&t))[1])
}

// asReflectType wraps a *abiType as reflect.Type.
// Borrows a stable rtype itab from the sample, then patches the data word.
//
//go:nosplit
func asReflectType(t *abiType) reflect.Type {
	if t == nil {
		return nil
	}
	out := sampleReflectType
	(*[2]unsafe.Pointer)(unsafe.Pointer(&out))[1] = unsafe.Pointer(t)
	return out
}

// sampleReflectType carries the canonical (*rtype, reflect.Type) itab.
var sampleReflectType = reflect.TypeOf(struct{}{})
