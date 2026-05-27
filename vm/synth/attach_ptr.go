package synth

import (
	"errors"
	"reflect"
	"unsafe"
)

var errNilElemType = errors.New("synth: AttachPtrMethods: nil elem type")

// AttachPtrMethods synthesizes a *T rtype carrying method m and wires it
// back so reflect.PointerTo(elem) returns this *T rather than a fresh
// methodless one.
// elem becomes the Elem of the new *T; its PtrToThis is overwritten.
//
// Phase 2a: one method per call.
// Phase 2d extends to multi-method via synthN containers.
func AttachPtrMethods(
	elem reflect.Type, name, pkgPath string, m Method,
) (reflect.Type, error) {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil, errNilElemType
	}

	stubPC, err := acquireSlotS1(m.Handler)
	if err != nil {
		return nil, err
	}

	b := new(synthPtr1)
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
	b.u = abiUncommon{
		PkgPath: uint32(addReflectOff(unsafe.Pointer(
			encodeName(pkgPath, false).Bytes))),
		Mcount: 1,
		Xcount: uint16(boolInt(m.Exported)),
		Moff:   uint32(moff),
	}

	b.m[0] = abiMethod{
		Name: uint32(addReflectOff(unsafe.Pointer(
			encodeName(m.Name, m.Exported).Bytes))),
		Mtyp: uint32(addReflectOff(unsafe.Pointer(rtypePtr(m.Sig)))),
		Ifn:  uint32(addReflectOff(ptrFromPC(stubPC))),
		Tfn:  uint32(addReflectOff(ptrFromPC(stubPC))),
	}

	// Close the cycle: reflect.PointerTo(elem) returns our synth *T because
	// reflect consults elem.PtrToThis before allocating a fresh ptr type.
	elemRT.PtrToThis = addReflectOff(unsafe.Pointer(&b.t))

	return asReflectType(&b.t), nil
}

// synthPtr1 is the fixed-shape container for a synth *T with one method.
// Layout: abiType(48) + Elem(8) + uncommon(16) + method(16).
// Uncommon at offset 56, matching runtime's PtrType + UncommonType.
type synthPtr1 struct {
	t    abiType
	elem *abiType
	u    abiUncommon
	m    [1]abiMethod
}
