package synth

import (
	"errors"
	"reflect"
	"unsafe"
)

// Clone returns a fresh rtype mirroring layout but with its own identity
// (new hash, separate PtrToThis), and no methods attached.
// Used by callers that want a private elem rtype to wire *T methods against
// without stomping the original layout's PtrToThis.
// The original layout keeps Fields/Equal/GCData reachable for GC.
func Clone(layout reflect.Type, pkgPath string) (reflect.Type, error) {
	switch k := layout.Kind(); {
	case k == reflect.Struct:
		return cloneStruct(layout, pkgPath)
	case isPrimitiveKind(k):
		return clonePrim(layout, pkgPath)
	case k == reflect.Slice:
		return cloneSlice(layout, pkgPath)
	case k == reflect.Array:
		return cloneArray(layout, pkgPath)
	case k == reflect.Map:
		return cloneMap(layout, pkgPath)
	}
	return nil, errCloneKind
}

var errCloneKind = errors.New(
	"synth: Clone: layout kind not supported")

func cloneStruct(layout reflect.Type, pkgPath string) (reflect.Type, error) {
	src := (*abiStructType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synth0Struct)
	b.t = *src
	b.t.TFlag |= tflagUncommon
	b.t.Hash = nextSyntheticHash()
	b.t.PtrToThis = 0
	b.u = makeUncommon(pkgPath, nil, uint32(unsafe.Sizeof(b.u)))
	registerLayout(&b.t.abiType, &src.abiType)
	return asReflectType(&b.t.abiType), nil
}

func clonePrim(layout reflect.Type, pkgPath string) (reflect.Type, error) {
	src := rtypePtr(layout)
	b := new(synth0Prim)
	b.t = *src
	b.t.TFlag |= tflagUncommon
	b.t.Hash = nextSyntheticHash()
	b.t.PtrToThis = 0
	b.u = makeUncommon(pkgPath, nil, uint32(unsafe.Sizeof(b.u)))
	registerLayout(&b.t, src)
	return asReflectType(&b.t), nil
}

func cloneSlice(layout reflect.Type, pkgPath string) (reflect.Type, error) {
	src := (*abiSliceType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synth0Slice)
	b.t = *src
	b.t.TFlag |= tflagUncommon
	b.t.Hash = nextSyntheticHash()
	b.t.PtrToThis = 0
	b.u = makeUncommon(pkgPath, nil, uint32(unsafe.Sizeof(b.u)))
	registerLayout(&b.t.abiType, &src.abiType)
	return asReflectType(&b.t.abiType), nil
}

func cloneArray(layout reflect.Type, pkgPath string) (reflect.Type, error) {
	src := (*abiArrayType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synth0Array)
	b.t = *src
	b.t.TFlag |= tflagUncommon
	b.t.Hash = nextSyntheticHash()
	b.t.PtrToThis = 0
	b.u = makeUncommon(pkgPath, nil, uint32(unsafe.Sizeof(b.u)))
	registerLayout(&b.t.abiType, &src.abiType)
	return asReflectType(&b.t.abiType), nil
}

func cloneMap(layout reflect.Type, pkgPath string) (reflect.Type, error) {
	src := (*abiMapType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synth0Map)
	b.t = *src
	b.t.TFlag |= tflagUncommon
	b.t.Hash = nextSyntheticHash()
	b.t.PtrToThis = 0
	b.u = makeUncommon(pkgPath, nil, uint32(unsafe.Sizeof(b.u)))
	registerLayout(&b.t.abiType, &src.abiType)
	return asReflectType(&b.t.abiType), nil
}

// CloneStruct is a back-compat shim for the struct-only Phase-2a caller.
// New code should call Clone.
func CloneStruct(layout reflect.Type, pkgPath string) (reflect.Type, error) {
	if layout.Kind() != reflect.Struct {
		return nil, errCloneStructKind
	}
	return cloneStruct(layout, pkgPath)
}

var errCloneStructKind = errors.New(
	"synth: CloneStruct: layout kind is not Struct")

// Zero-method containers, one per kind.
// Layout = kind-specific type prefix + uncommon (no method array).

type synth0Struct struct {
	t abiStructType
	u abiUncommon
}

type synth0Prim struct {
	t abiType
	u abiUncommon
}

type synth0Slice struct {
	t abiSliceType
	u abiUncommon
}

type synth0Array struct {
	t abiArrayType
	u abiUncommon
}

type synth0Map struct {
	t abiMapType
	u abiUncommon
}
