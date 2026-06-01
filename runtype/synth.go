package runtype

import (
	"reflect"
)

// MethodSpec describes one method to install on a synthesized rtype.
// StubPC is the dispatch-stub entry PC wired into the method's Ifn/Tfn; the
// caller resolves it from the method's signature shape before Fill.
type MethodSpec struct {
	Name     string
	Exported bool
	Sig      reflect.Type
	StubPC   uintptr
}

// synthStruct is the multi-method container for a synth struct.
// Typed-struct allocation (vs []byte) gives GC the correct pointer map for
// Equal, GCData, and the Fields slice.
type synthStruct struct {
	st abiStructType
	u  abiUncommon
	m  [maxMethods]abiMethod
}
