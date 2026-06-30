package runtype

import (
	"errors"
)

var errNilElemType = errors.New("runtype: ReservePtrMethods: nil elem type")

// synthPtr is the multi-method container for a synth *T.
// Layout: abiType(48) + Elem(8) + uncommon(16) + [maxMethods]method.
// Uncommon at offset 56, matching runtime's PtrType + UncommonType.
type synthPtr struct {
	t    abiType
	elem *abiType
	u    abiUncommon
	m    [maxMethods]abiMethod
}
