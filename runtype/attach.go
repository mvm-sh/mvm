package runtype

import (
	"errors"
	"reflect"
	"unsafe"
)

// MethodSpec describes one method to install on a synthesized rtype.
// StubPC is the dispatch-stub entry PC wired into the method's Ifn/Tfn; the
// caller resolves it from the method's signature shape before calling Attach*.
type MethodSpec struct {
	Name     string
	Exported bool
	Sig      reflect.Type
	StubPC   uintptr
}

var errKindStruct = errors.New(
	"runtype: AttachStructMethods: layout kind is not Struct")

// AttachStructMethods returns a new rtype whose method set contains methods.
// name is the user-facing type name stamped into the synth rtype's Str so
// reflect.Type.Name()/String() match the source program.
// The original layout rtype is discarded; callers MUST use the returned type
// as the canonical identity.
// itab cache keys on pointer identity, so a mismatch silently disables
// interface dispatch.
// len(methods) must be in [1, maxMethods].
func AttachStructMethods(
	layout reflect.Type, name, pkgPath string, methods []MethodSpec,
) (reflect.Type, error) {
	if layout.Kind() != reflect.Struct {
		return nil, errKindStruct
	}
	if err := checkMethodCount(methods); err != nil {
		return nil, err
	}

	src := (*abiStructType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthStruct)

	// Fields/Equal/GCData copy by pointer; the source rtype keeps them reachable.
	b.st = *src
	stampHeader(&b.st.abiType, name)

	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, methods, uint32(moff))
	installMethods(b.m[:len(methods)], methods)

	registerLayout(&b.st.abiType, &src.abiType)
	return asReflectType(&b.st.abiType), nil
}

// synthStruct is the multi-method container for a synth struct.
// Typed-struct allocation (vs []byte) gives GC the correct pointer map for
// Equal, GCData, and the Fields slice.
type synthStruct struct {
	st abiStructType
	u  abiUncommon
	m  [maxMethods]abiMethod
}
