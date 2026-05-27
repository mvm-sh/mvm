package synth

import (
	"errors"
	"reflect"
	"unsafe"
)

var errNilElemType = errors.New("synth: AttachPtrMethods: nil elem type")

// AttachPtrMethods synthesizes a *T rtype carrying the methods and wires it
// back so reflect.PointerTo(elem) returns this *T rather than a fresh
// methodless one.
// elem becomes the Elem of the new *T; its PtrToThis is overwritten.
// len(methods) must be in [1, maxMethods].
func AttachPtrMethods(
	elem reflect.Type, name, pkgPath string, methods []Method,
) (reflect.Type, error) {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil, errNilElemType
	}
	if err := checkMethodCount(methods); err != nil {
		return nil, err
	}
	stubs, err := acquireSlots(methods)
	if err != nil {
		return nil, err
	}

	b := new(synthPtr)
	b.elem = elemRT

	// Pointer types are direct-iface, one word, and share Equal/GCData with
	// every other pointer type.
	// Borrowing from *int's rtype gives us the canonical single-pointer
	// GCData bitmap and the pointer-equality func for free.
	intPtrRT := rtypePtr(reflect.TypeOf((*int)(nil)))

	b.t = abiType{
		Size:       unsafe.Sizeof(uintptr(0)),
		PtrBytes:   unsafe.Sizeof(uintptr(0)),
		Hash:       nextSyntheticHash(),
		TFlag:      tflagUncommon | tflagNamed | tflagDirectIface,
		Align:      uint8(unsafe.Alignof(uintptr(0))),
		FieldAlign: uint8(unsafe.Alignof(uintptr(0))),
		Kind:       kindPointer,
		Equal:      intPtrRT.Equal,
		GCData:     intPtrRT.GCData,
		Str: addReflectOff(unsafe.Pointer(
			encodeName(name, true).Bytes)),
		PtrToThis: 0,
	}

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, methods, uint32(moff))
	installMethods(b.m[:len(methods)], methods, stubs)

	// Close the cycle: reflect.PointerTo(elem) returns our synth *T because
	// reflect consults elem.PtrToThis before allocating a fresh ptr type.
	elemRT.PtrToThis = addReflectOff(unsafe.Pointer(&b.t))

	intPtrLayout := rtypePtr(reflect.TypeOf((*int)(nil)))
	registerLayout(&b.t, intPtrLayout)
	return asReflectType(&b.t), nil
}

// synthPtr is the multi-method container for a synth *T.
// Layout: abiType(48) + Elem(8) + uncommon(16) + [maxMethods]method.
// Uncommon at offset 56, matching runtime's PtrType + UncommonType.
type synthPtr struct {
	t    abiType
	elem *abiType
	u    abiUncommon
	m    [maxMethods]abiMethod
}
