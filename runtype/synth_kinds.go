package runtype

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"unsafe"
)

// ErrKindUnsupported is returned by ReserveMethods for layouts whose Kind is
// not in the synth catalog.
var ErrKindUnsupported = errors.New(
	"runtype: ReserveMethods: layout kind not supported")

func stampHeader(t *abiType, name string) {
	t.TFlag = (t.TFlag &^ tflagExtraStar) | tflagUncommon | tflagNamed
	t.Hash = nextSyntheticHash()
	t.PtrToThis = 0
	t.Str = addReflectOff(unsafe.Pointer(encodeName(name, true).Bytes))
}

func makeUncommon(pkgPath string, methods []MethodSpec, moff uint32) abiUncommon {
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

func makeMethod(m MethodSpec) abiMethod {
	return abiMethod{
		Name: uint32(addReflectOff(unsafe.Pointer(
			encodeName(m.Name, m.Exported).Bytes))),
		Mtyp: uint32(addReflectOff(unsafe.Pointer(rtypePtr(m.Sig)))),
		Ifn:  uint32(addReflectOff(ptrFromPC(m.StubPC))),
		Tfn:  uint32(addReflectOff(ptrFromPC(m.StubPC))),
	}
}

func installMethods(dst []abiMethod, methods []MethodSpec) {
	order := make([]int, len(methods))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return methods[order[i]].Name < methods[order[j]].Name
	})
	for i, idx := range order {
		dst[i] = makeMethod(methods[idx])
	}
}

func checkMethodCount(methods []MethodSpec) error {
	switch {
	case len(methods) == 0:
		return errNoMethods
	case len(methods) > maxMethods:
		return fmt.Errorf("runtype: too many methods (%d > %d)",
			len(methods), maxMethods)
	}
	return nil
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
	errFuncTooManyIO = errors.New("runtype: reserveFunc: too many in/out params")
	errNoMethods     = errors.New("runtype: methods slice is empty")
)

// maxFuncIO caps a synth func type's combined in+out parameter count; the
// inline io array is sized to it. Method-bearing func types are rare and have
// small signatures, so this is generous headroom.
const maxFuncIO = 32

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

// synthFunc places the In/Out pointer array (io) between the uncommon struct
// and the methods, matching the runtime layout reflect expects for func types.
type synthFunc struct {
	t  abiFuncType
	u  abiUncommon
	io [maxFuncIO]*abiType
	m  [maxMethods]abiMethod
}
