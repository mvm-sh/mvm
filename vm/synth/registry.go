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

	// ShapeS4 is func(error) bool.
	// Covers the errors-tree predicate errors.Is dispatches: (T).Is(error) bool.
	ShapeS4 Shape = 3

	// ShapeS5 is func(any) bool.
	// Covers errors.As dispatch: (T).As(any) bool.
	ShapeS5 Shape = 4

	// ShapeS6 is func() error.
	// Covers single-error unwrap: (T).Unwrap() error.
	ShapeS6 Shape = 5

	// ShapeS7 is func() []error.
	// Covers multi-error unwrap: (T).Unwrap() []error.
	ShapeS7 Shape = 6

	// ShapeS8 is func() int.
	// Covers sort.Interface.Len.
	ShapeS8 Shape = 7

	// ShapeS9 is func(int, int) bool.
	// Covers sort.Interface.Less.
	ShapeS9 Shape = 8

	// ShapeS10 is func(int, int).
	// Covers sort.Interface.Swap.
	ShapeS10 Shape = 9

	// ShapeS11 is func(any).
	// Covers heap.Interface.Push.
	ShapeS11 Shape = 10

	// ShapeS12 is func() any.
	// Covers heap.Interface.Pop.
	ShapeS12 Shape = 11

	// ShapeS13 is func([]byte) (int, error).
	// Covers io.Reader.Read and io.Writer.Write.
	ShapeS13 Shape = 12

	// ShapeS14 is func(fmt.State, rune).
	// Covers fmt.Formatter.Format.
	ShapeS14 Shape = 13

	// ShapeS15 is func(*xml.Encoder, xml.StartElement) error.
	// Covers xml.Marshaler.MarshalXML.
	ShapeS15 Shape = 14

	// ShapeS16 is func(*xml.Decoder, xml.StartElement) error.
	// Covers xml.Unmarshaler.UnmarshalXML.
	ShapeS16 Shape = 15
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

var errInvalidHandlerType = errors.New("synth: handler type does not match shape")

// acquireSlot claims a free slot in the pool for m.Shape and returns the
// stub PC for Ifn/Tfn plus a release closure.
// release nils the slot's handler entry, freeing captured closure state
// (*Machine/*Type references) for GC.
// The slot INDEX itself remains consumed: the per-shape counter is
// monotonic with no safe decrement under concurrent acquires.
// Release is best-effort memory hygiene, not pool reclamation.
func acquireSlot(m Method) (pc uintptr, release func(), err error) {
	switch m.Shape {
	case ShapeS1:
		h, ok := m.Handler.(HandlerS1)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS1(h)
	case ShapeS2:
		h, ok := m.Handler.(HandlerS2)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS2(h)
	case ShapeS3:
		h, ok := m.Handler.(HandlerS3)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS3(h)
	case ShapeS4:
		h, ok := m.Handler.(HandlerS4)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS4(h)
	case ShapeS5:
		h, ok := m.Handler.(HandlerS5)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS5(h)
	case ShapeS6:
		h, ok := m.Handler.(HandlerS6)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS6(h)
	case ShapeS7:
		h, ok := m.Handler.(HandlerS7)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS7(h)
	case ShapeS8:
		h, ok := m.Handler.(HandlerS8)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS8(h)
	case ShapeS9:
		h, ok := m.Handler.(HandlerS9)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS9(h)
	case ShapeS10:
		h, ok := m.Handler.(HandlerS10)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS10(h)
	case ShapeS11:
		h, ok := m.Handler.(HandlerS11)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS11(h)
	case ShapeS12:
		h, ok := m.Handler.(HandlerS12)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS12(h)
	case ShapeS13:
		h, ok := m.Handler.(HandlerS13)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS13(h)
	case ShapeS14:
		h, ok := m.Handler.(HandlerS14)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS14(h)
	case ShapeS15:
		h, ok := m.Handler.(HandlerS15)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS15(h)
	case ShapeS16:
		h, ok := m.Handler.(HandlerS16)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS16(h)
	}
	return 0, nil, fmt.Errorf("synth: unknown shape %d", m.Shape)
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
