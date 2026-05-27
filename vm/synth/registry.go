package synth

import (
	"errors"
	"fmt"
	"reflect"
)

// Shape identifies a method-signature shape.
// Each shape has its own stub pool, handler type, and dispatcher.
// New shapes can be added without touching existing shapes.
type Shape uint8

const (
	// ShapeS1 is func() string.
	// Covers fmt.Stringer.String, error.Error, fmt.GoStringer.GoString,
	// flag.Value.String.
	ShapeS1 Shape = 0

	// ShapeS2 is func() ([]byte, error).
	// Covers json.Marshaler.MarshalJSON, encoding.BinaryMarshaler,
	// encoding.TextMarshaler, xml.Marshaler.MarshalXML (almost; subset).
	ShapeS2 Shape = 1

	// ShapeS3 is func([]byte) error.
	// Covers json.Unmarshaler.UnmarshalJSON, encoding.BinaryUnmarshaler,
	// encoding.TextUnmarshaler.
	ShapeS3 Shape = 2
)

// Method describes one method to install on a synthesized type.
// Shape selects which stub pool the slot comes from; Handler must be the
// matching HandlerS* function type for the Shape (default ShapeS1 expects
// HandlerS1).
type Method struct {
	Name     string
	Exported bool
	Sig      reflect.Type
	Shape    Shape
	Handler  any
}

// errPoolExhausted reports that the pool for the given shape is full.
// Each shape carries its own message via fmt.
var errInvalidHandlerType = errors.New("synth: handler type does not match shape")

// acquireSlot claims a free slot in the pool for m.Shape and returns the
// stub PC for Ifn/Tfn together with the (16-bit) slot index.
func acquireSlot(m Method) (pc uintptr, err error) {
	switch m.Shape {
	case ShapeS1:
		h, ok := m.Handler.(HandlerS1)
		if !ok {
			return 0, errInvalidHandlerType
		}
		return acquireSlotS1(h)
	case ShapeS2:
		h, ok := m.Handler.(HandlerS2)
		if !ok {
			return 0, errInvalidHandlerType
		}
		return acquireSlotS2(h)
	case ShapeS3:
		h, ok := m.Handler.(HandlerS3)
		if !ok {
			return 0, errInvalidHandlerType
		}
		return acquireSlotS3(h)
	}
	return 0, fmt.Errorf("synth: unknown shape %d", m.Shape)
}

// errPoolFmt produces the user-facing message for a given shape's pool
// exhaustion.
// Centralized so each shape registry references a single template.
func errPoolFmt(shape string, poolCap uint32) error {
	return fmt.Errorf("synth: shape %s stub pool exhausted (cap=%d)", shape, poolCap)
}

// dispatchErr returns the formatted error string a stub returns when its
// slot was acquired but a runtime invariant was broken (unknown slot index,
// missing handler).
// Centralized so each shape dispatcher reports identically.
func dispatchErr(slot uint32, what string) string {
	return fmt.Sprintf("synth: slot %d %s", slot, what)
}
