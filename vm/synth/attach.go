package synth

import (
	"fmt"
	"reflect"
	"unsafe"
)

// AttachStructMethods returns a new rtype whose method set contains m.
// The original layout rtype is discarded; callers MUST use the returned type
// as the canonical identity.
// itab cache keys on pointer identity, so a mismatch silently disables
// interface dispatch.
//
// Phase 1: struct kind only, one method per call.
func AttachStructMethods(
	layout reflect.Type, pkgPath string, m Method,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Struct {
		return nil, fmt.Errorf("synth: AttachStructMethods: layout kind=%s, want Struct",
			layout.Kind())
	}

	stubPC, err := acquireSlotS1(m.Handler)
	if err != nil {
		return nil, err
	}

	src := (*abiStructType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synth1)

	// Fields/Equal/GCData copy by pointer; the source rtype keeps them reachable.
	b.st = *src

	b.st.TFlag |= tflagUncommon
	b.st.Hash = nextSyntheticHash()
	b.st.PtrToThis = 0

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = abiUncommon{
		PkgPath: uint32(addReflectOff(unsafe.Pointer(encodeName(pkgPath, false).Bytes))),
		Mcount:  1,
		Xcount:  uint16(boolInt(m.Exported)),
		Moff:    uint32(moff),
	}

	// Ifn and Tfn share a stub: matches the iface-dispatch convention.
	// Phase 4 may split them for natural-ABI value receivers if
	// Method.Func.Call needs it.
	b.m[0] = abiMethod{
		Name: uint32(addReflectOff(unsafe.Pointer(encodeName(m.Name, m.Exported).Bytes))),
		Mtyp: uint32(addReflectOff(unsafe.Pointer(rtypePtr(m.Sig)))),
		Ifn:  uint32(addReflectOff(ptrFromPC(stubPC))),
		Tfn:  uint32(addReflectOff(ptrFromPC(stubPC))),
	}

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
