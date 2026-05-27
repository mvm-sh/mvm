package synth

import (
	"errors"
	"reflect"
	"unsafe"
)

var errCloneStructKind = errors.New(
	"synth: CloneStruct: layout kind is not Struct")

// CloneStruct returns a fresh rtype that mirrors layout but carries its own
// identity (new hash, separate PtrToThis).
// Method set is empty; use when callers want a private elem rtype to wire
// *T methods against without stomping the original layout's PtrToThis.
// The original layout keeps Fields/Equal/GCData reachable for GC.
func CloneStruct(layout reflect.Type, pkgPath string) (reflect.Type, error) {
	if layout.Kind() != reflect.Struct {
		return nil, errCloneStructKind
	}
	src := (*abiStructType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synth0)
	b.st = *src
	b.st.TFlag |= tflagUncommon
	b.st.Hash = nextSyntheticHash()
	b.st.PtrToThis = 0

	b.u = abiUncommon{
		PkgPath: uint32(addReflectOff(unsafe.Pointer(
			encodeName(pkgPath, false).Bytes))),
		Mcount: 0,
		Xcount: 0,
		Moff:   uint32(unsafe.Sizeof(b.u)),
	}
	return asReflectType(&b.st.abiType), nil
}

// synth0 is the zero-method counterpart of synth1.
// Layout: abiStructType + abiUncommon, no method array.
type synth0 struct {
	st abiStructType
	u  abiUncommon
}
