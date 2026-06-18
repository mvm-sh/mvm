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

func makeUncommon(pkgPath string, moff uint32) abiUncommon {
	return abiUncommon{
		PkgPath: uint32(addReflectOff(unsafe.Pointer(
			encodeName(pkgPath, false).Bytes))),
		Moff: moff,
	}
}

func makeMethod(m MethodSpec) abiMethod {
	return abiMethod{
		Name: uint32(addReflectOff(unsafe.Pointer(
			encodeNamePkg(m.Name, m.Exported, m.PkgPath).Bytes))),
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

// SupportedKind reports whether a layout of kind k can be given a reserved identity.
func SupportedKind(k reflect.Kind) bool {
	switch k {
	case reflect.Struct, reflect.Slice, reflect.Array, reflect.Map, reflect.Chan,
		reflect.Pointer, reflect.Func:
		return true
	}
	return isPrimitiveKind(k)
}

var (
	errFuncTooManyIO = errors.New("runtype: reserveFunc: too many in/out params")
	errNoMethods     = errors.New("runtype: methods slice is empty")
)

// maxFuncIO caps a synth func type's combined in+out parameter count.
// Method-bearing func types are rare and have small signatures, so this is generous headroom.
const maxFuncIO = 32

// maxMethods caps the methods installable per synth attach call.
// Runtime cost: maxMethods*16 bytes per synth rtype.
const maxMethods = 256

// MaxMethods is the per-synth-rtype method-table capacity.
const MaxMethods = maxMethods

// Per-kind multi-method containers.
// Layout = kind-specific type prefix + uncommon + [maxMethods]method.

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

type synthChan struct {
	t abiChanType
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
