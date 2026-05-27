package synth

import (
	"errors"
	"reflect"
	"unsafe"
)

// AttachMethods dispatches to the kind-specific attach func.
// Supported kinds: struct, named primitive (int/uint/float/bool/string/
// complex), slice, array, map.
// Unsupported kinds return ErrKindUnsupported.
func AttachMethods(
	layout reflect.Type, name, pkgPath string, m Method,
) (reflect.Type, error) {
	switch k := layout.Kind(); {
	case k == reflect.Struct:
		return AttachStructMethods(layout, name, pkgPath, m)
	case isPrimitiveKind(k):
		return AttachPrimitiveMethods(layout, name, pkgPath, m)
	case k == reflect.Slice:
		return AttachSliceMethods(layout, name, pkgPath, m)
	case k == reflect.Array:
		return AttachArrayMethods(layout, name, pkgPath, m)
	case k == reflect.Map:
		return AttachMapMethods(layout, name, pkgPath, m)
	}
	return nil, ErrKindUnsupported
}

// ErrKindUnsupported is returned by AttachMethods for layouts whose Kind is
// not in the Phase 2b catalog.
var ErrKindUnsupported = errors.New(
	"synth: AttachMethods: layout kind not supported")

// AttachPrimitiveMethods synthesizes a fresh rtype mirroring a named primitive
// layout (named int/uint/float/bool/string/complex) with method m attached.
// The source rtype identity is shared with the native primitive; the returned
// rtype has its own identity (new hash, separate PtrToThis) so reflect
// queries on user types do not bleed into native primitive state.
func AttachPrimitiveMethods(
	layout reflect.Type, name, pkgPath string, m Method,
) (reflect.Type, error) {
	if !isPrimitiveKind(layout.Kind()) {
		return nil, errKindPrim
	}
	stubPC, err := acquireSlot(m)
	if err != nil {
		return nil, err
	}
	src := rtypePtr(layout)
	b := new(synthPrim1)
	b.t = *src
	stampHeader(&b.t, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, 1, m.Exported, uint32(moff))
	b.m[0] = makeMethod(m, stubPC)
	return asReflectType(&b.t), nil
}

// AttachSliceMethods synthesizes a fresh slice rtype carrying method m.
func AttachSliceMethods(
	layout reflect.Type, name, pkgPath string, m Method,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Slice {
		return nil, errKindSlice
	}
	stubPC, err := acquireSlot(m)
	if err != nil {
		return nil, err
	}
	src := (*abiSliceType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthSlice1)
	b.t = *src
	stampHeader(&b.t.abiType, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, 1, m.Exported, uint32(moff))
	b.m[0] = makeMethod(m, stubPC)
	return asReflectType(&b.t.abiType), nil
}

// AttachArrayMethods synthesizes a fresh array rtype carrying method m.
func AttachArrayMethods(
	layout reflect.Type, name, pkgPath string, m Method,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Array {
		return nil, errKindArray
	}
	stubPC, err := acquireSlot(m)
	if err != nil {
		return nil, err
	}
	src := (*abiArrayType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthArray1)
	b.t = *src
	stampHeader(&b.t.abiType, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, 1, m.Exported, uint32(moff))
	b.m[0] = makeMethod(m, stubPC)
	return asReflectType(&b.t.abiType), nil
}

// AttachMapMethods synthesizes a fresh map rtype carrying method m.
func AttachMapMethods(
	layout reflect.Type, name, pkgPath string, m Method,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Map {
		return nil, errKindMap
	}
	stubPC, err := acquireSlot(m)
	if err != nil {
		return nil, err
	}
	src := (*abiMapType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthMap1)
	b.t = *src
	stampHeader(&b.t.abiType, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, 1, m.Exported, uint32(moff))
	b.m[0] = makeMethod(m, stubPC)
	return asReflectType(&b.t.abiType), nil
}

// stampHeader stamps the abiType header common to every synth rtype: fresh
// hash, uncommon+named flags, cleared PtrToThis, and a new Str NameOff.
func stampHeader(t *abiType, name string) {
	t.TFlag |= tflagUncommon | tflagNamed
	t.Hash = nextSyntheticHash()
	t.PtrToThis = 0
	t.Str = addReflectOff(unsafe.Pointer(encodeName(name, true).Bytes))
}

func makeUncommon(pkgPath string, mcount uint16, exported bool, moff uint32) abiUncommon {
	return abiUncommon{
		PkgPath: uint32(addReflectOff(unsafe.Pointer(
			encodeName(pkgPath, false).Bytes))),
		Mcount: mcount,
		Xcount: uint16(boolInt(exported)),
		Moff:   moff,
	}
}

func makeMethod(m Method, stubPC uintptr) abiMethod {
	return abiMethod{
		Name: uint32(addReflectOff(unsafe.Pointer(
			encodeName(m.Name, m.Exported).Bytes))),
		Mtyp: uint32(addReflectOff(unsafe.Pointer(rtypePtr(m.Sig)))),
		Ifn:  uint32(addReflectOff(ptrFromPC(stubPC))),
		Tfn:  uint32(addReflectOff(ptrFromPC(stubPC))),
	}
}

func isPrimitiveKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return true
	}
	return false
}

var (
	errKindPrim  = errors.New("synth: AttachPrimitiveMethods: layout is not a primitive kind")
	errKindSlice = errors.New("synth: AttachSliceMethods: layout kind is not Slice")
	errKindArray = errors.New("synth: AttachArrayMethods: layout kind is not Array")
	errKindMap   = errors.New("synth: AttachMapMethods: layout kind is not Map")
)

// Per-kind 1-method containers, matching the synth1 pattern for struct.
// Layout = kind-specific type prefix + uncommon + [1]method.

type synthPrim1 struct {
	t abiType
	u abiUncommon
	m [1]abiMethod
}

type synthSlice1 struct {
	t abiSliceType
	u abiUncommon
	m [1]abiMethod
}

type synthArray1 struct {
	t abiArrayType
	u abiUncommon
	m [1]abiMethod
}

type synthMap1 struct {
	t abiMapType
	u abiUncommon
	m [1]abiMethod
}
