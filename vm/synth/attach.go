package synth

import (
	"errors"
	"reflect"
	"unsafe"
)

var errKindStruct = errors.New(
	"synth: AttachStructMethods: layout kind is not Struct")

// AttachStructMethods returns a new rtype whose method set contains m.
// name is the user-facing type name stamped into the synth rtype's Str so
// reflect.Type.Name()/String() match the source program (the source layout's
// Str may point into a moduledata reflect cannot resolve from a heap-built
// rtype, so we always restamp via addReflectOff).
// The original layout rtype is discarded; callers MUST use the returned type
// as the canonical identity.
// itab cache keys on pointer identity, so a mismatch silently disables
// interface dispatch.
func AttachStructMethods(
	layout reflect.Type, name, pkgPath string, m Method,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Struct {
		return nil, errKindStruct
	}

	stubPC, err := acquireSlot(m)
	if err != nil {
		return nil, err
	}

	src := (*abiStructType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synth1)

	// Fields/Equal/GCData copy by pointer; the source rtype keeps them reachable.
	b.st = *src
	stampHeader(&b.st.abiType, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, 1, m.Exported, uint32(moff))
	b.m[0] = makeMethod(m, stubPC)

	return asReflectType(&b.st.abiType), nil
}

// synth1 is the fixed-shape container for a synth struct with one method.
// Typed-struct allocation (vs []byte) gives GC the correct pointer map for
// Equal, GCData, and the Fields slice.
type synth1 struct {
	st abiStructType
	u  abiUncommon
	m  [1]abiMethod
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
