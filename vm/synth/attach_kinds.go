package synth

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"unsafe"
)

// AttachMethods dispatches to the kind-specific attach func.
// Supported kinds: struct, named primitive (int/uint/float/bool/string/
// complex), slice, array, map.
// Unsupported kinds return ErrKindUnsupported.
// methods may carry mixed shapes; each is registered against its shape's
// slot pool. len(methods) must be in [1, maxMethods].
func AttachMethods(
	layout reflect.Type, name, pkgPath string, methods []Method,
) (reflect.Type, error) {
	switch k := layout.Kind(); {
	case k == reflect.Struct:
		return AttachStructMethods(layout, name, pkgPath, methods)
	case isPrimitiveKind(k):
		return AttachPrimitiveMethods(layout, name, pkgPath, methods)
	case k == reflect.Slice:
		return AttachSliceMethods(layout, name, pkgPath, methods)
	case k == reflect.Array:
		return AttachArrayMethods(layout, name, pkgPath, methods)
	case k == reflect.Map:
		return AttachMapMethods(layout, name, pkgPath, methods)
	}
	return nil, ErrKindUnsupported
}

// ErrKindUnsupported is returned by AttachMethods for layouts whose Kind is
// not in the Phase 2b catalog.
var ErrKindUnsupported = errors.New(
	"synth: AttachMethods: layout kind not supported")

// AttachPrimitiveMethods synthesizes a fresh rtype mirroring a named primitive
// layout (named int/uint/float/bool/string/complex) with the given methods
// attached.
// The source rtype identity is shared with the native primitive; the returned
// rtype has its own identity (new hash, separate PtrToThis) so reflect
// queries on user types do not bleed into native primitive state.
func AttachPrimitiveMethods(
	layout reflect.Type, name, pkgPath string, methods []Method,
) (reflect.Type, error) {
	if !isPrimitiveKind(layout.Kind()) {
		return nil, errKindPrim
	}
	if err := checkMethodCount(methods); err != nil {
		return nil, err
	}
	stubs, err := acquireSlots(methods)
	if err != nil {
		return nil, err
	}
	src := rtypePtr(layout)
	b := new(synthPrim)
	b.t = *src
	stampHeader(&b.t, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, methods, uint32(moff))
	installMethods(b.m[:len(methods)], methods, stubs)
	registerLayout(&b.t, src)
	return asReflectType(&b.t), nil
}

// AttachSliceMethods synthesizes a fresh slice rtype carrying the methods.
func AttachSliceMethods(
	layout reflect.Type, name, pkgPath string, methods []Method,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Slice {
		return nil, errKindSlice
	}
	if err := checkMethodCount(methods); err != nil {
		return nil, err
	}
	stubs, err := acquireSlots(methods)
	if err != nil {
		return nil, err
	}
	src := (*abiSliceType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthSlice)
	b.t = *src
	stampHeader(&b.t.abiType, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, methods, uint32(moff))
	installMethods(b.m[:len(methods)], methods, stubs)
	registerLayout(&b.t.abiType, &src.abiType)
	return asReflectType(&b.t.abiType), nil
}

// AttachArrayMethods synthesizes a fresh array rtype carrying the methods.
func AttachArrayMethods(
	layout reflect.Type, name, pkgPath string, methods []Method,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Array {
		return nil, errKindArray
	}
	if err := checkMethodCount(methods); err != nil {
		return nil, err
	}
	stubs, err := acquireSlots(methods)
	if err != nil {
		return nil, err
	}
	src := (*abiArrayType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthArray)
	b.t = *src
	stampHeader(&b.t.abiType, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, methods, uint32(moff))
	installMethods(b.m[:len(methods)], methods, stubs)
	registerLayout(&b.t.abiType, &src.abiType)
	return asReflectType(&b.t.abiType), nil
}

// AttachMapMethods synthesizes a fresh map rtype carrying the methods.
func AttachMapMethods(
	layout reflect.Type, name, pkgPath string, methods []Method,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Map {
		return nil, errKindMap
	}
	if err := checkMethodCount(methods); err != nil {
		return nil, err
	}
	stubs, err := acquireSlots(methods)
	if err != nil {
		return nil, err
	}
	src := (*abiMapType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthMap)
	b.t = *src
	stampHeader(&b.t.abiType, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, methods, uint32(moff))
	installMethods(b.m[:len(methods)], methods, stubs)
	registerLayout(&b.t.abiType, &src.abiType)
	return asReflectType(&b.t.abiType), nil
}

func stampHeader(t *abiType, name string) {
	t.TFlag = (t.TFlag &^ tflagExtraStar) | tflagUncommon | tflagNamed
	t.Hash = nextSyntheticHash()
	t.PtrToThis = 0
	t.Str = addReflectOff(unsafe.Pointer(encodeName(name, true).Bytes))
}

func makeUncommon(pkgPath string, methods []Method, moff uint32) abiUncommon {
	xcount := 0
	for _, m := range methods {
		if m.Exported {
			xcount++
		}
	}
	return abiUncommon{
		PkgPath: uint32(addReflectOff(unsafe.Pointer(
			encodeName(pkgPath, false).Bytes))),
		Mcount: uint16(len(methods)),
		Xcount: uint16(xcount),
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

func installMethods(dst []abiMethod, methods []Method, stubs []uintptr) {
	order := make([]int, len(methods))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return methods[order[i]].Name < methods[order[j]].Name
	})
	for i, idx := range order {
		dst[i] = makeMethod(methods[idx], stubs[idx])
	}
}

func checkMethodCount(methods []Method) error {
	switch {
	case len(methods) == 0:
		return errNoMethods
	case len(methods) > maxMethods:
		return fmt.Errorf("synth: too many methods (%d > %d)",
			len(methods), maxMethods)
	}
	return nil
}

func acquireSlots(methods []Method) ([]uintptr, error) {
	stubs := make([]uintptr, len(methods))
	releases := make([]func(), 0, len(methods))
	for i, m := range methods {
		pc, release, err := acquireSlot(m)
		if err != nil {
			for _, r := range releases {
				r()
			}
			return nil, err
		}
		stubs[i] = pc
		releases = append(releases, release)
	}
	return stubs, nil
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
	errNoMethods = errors.New("synth: methods slice is empty")
)

// maxMethods caps the number of methods installable per synth attach call.
// Sized to comfortably hold the union of Stringer/Error/GoString +
// Marshal{JSON,Text,Binary} + Unmarshal{JSON,Text,Binary} + Format-like
// methods, plus headroom.
// Runtime cost: maxMethods*16 bytes per synth rtype (unused slots stay
// zero; Mcount in uncommon bounds runtime iteration to the real count).
const maxMethods = 16

// Per-kind multi-method containers.
// Layout = kind-specific type prefix + uncommon + [maxMethods]method.
// The runtime reads exactly Mcount methods starting at Moff, so unused
// slots are harmless padding.

type synthPrim struct {
	t abiType
	u abiUncommon
	m [maxMethods]abiMethod
}

type synthSlice struct {
	t abiSliceType
	u abiUncommon
	m [maxMethods]abiMethod
}

type synthArray struct {
	t abiArrayType
	u abiUncommon
	m [maxMethods]abiMethod
}

type synthMap struct {
	t abiMapType
	u abiUncommon
	m [maxMethods]abiMethod
}
