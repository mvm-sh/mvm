package stubs

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

	// ShapeS17 is func() (int, bool).
	// Covers fmt.State.Width and fmt.State.Precision.
	ShapeS17 Shape = 16

	// ShapeS18 is func(int) bool.
	// Covers fmt.State.Flag.
	ShapeS18 Shape = 17

	// ShapeS19 is func(fmt.ScanState, rune) error.
	// Covers fmt.Scanner.Scan.
	ShapeS19 Shape = 18

	// ShapeS20 is func(string) error.
	// Covers flag.Value.Set.
	ShapeS20 Shape = 19

	// ShapeS21 is func() bool.
	// Covers flag.boolFlag.IsBoolFlag.
	ShapeS21 Shape = 20

	// ShapeS22 is func() int64. Covers fs.FileInfo.Size.
	ShapeS22 Shape = 21
	// ShapeS23 is func() fs.FileMode. Covers fs.FileInfo.Mode and fs.DirEntry.Type.
	ShapeS23 Shape = 22
	// ShapeS24 is func() time.Time. Covers fs.FileInfo.ModTime.
	ShapeS24 Shape = 23
	// ShapeS25 is func() (fs.FileInfo, error). Covers fs.DirEntry.Info and fs.File.Stat.
	ShapeS25 Shape = 24
	// ShapeS26 is func(string) (fs.File, error). Covers fs.FS.Open.
	ShapeS26 Shape = 25
	// ShapeS27 is func(string) (fs.FileInfo, error). Covers fs.StatFS.Stat.
	ShapeS27 Shape = 26
	// ShapeS28 is func(string) (fs.FS, error). Covers fs.SubFS.Sub.
	ShapeS28 Shape = 27
	// ShapeS29 is func(string) ([]string, error). Covers fs.GlobFS.Glob.
	ShapeS29 Shape = 28
	// ShapeS30 is func(string) ([]fs.DirEntry, error). Covers fs.ReadDirFS.ReadDir.
	ShapeS30 Shape = 29
	// ShapeS31 is func(string) ([]byte, error). Covers fs.ReadFileFS.ReadFile.
	ShapeS31 Shape = 30
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

var errInvalidHandlerType = errors.New("stubs: handler type does not match shape")

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
	case ShapeS17:
		h, ok := m.Handler.(HandlerS17)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS17(h)
	case ShapeS18:
		h, ok := m.Handler.(HandlerS18)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS18(h)
	case ShapeS19:
		h, ok := m.Handler.(HandlerS19)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS19(h)
	case ShapeS20:
		h, ok := m.Handler.(HandlerS20)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS20(h)
	case ShapeS21:
		h, ok := m.Handler.(HandlerS21)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS21(h)
	case ShapeS22:
		h, ok := m.Handler.(HandlerS22)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS22(h)
	case ShapeS23:
		h, ok := m.Handler.(HandlerS23)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS23(h)
	case ShapeS24:
		h, ok := m.Handler.(HandlerS24)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS24(h)
	case ShapeS25:
		h, ok := m.Handler.(HandlerS25)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS25(h)
	case ShapeS26:
		h, ok := m.Handler.(HandlerS26)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS26(h)
	case ShapeS27:
		h, ok := m.Handler.(HandlerS27)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS27(h)
	case ShapeS28:
		h, ok := m.Handler.(HandlerS28)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS28(h)
	case ShapeS29:
		h, ok := m.Handler.(HandlerS29)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS29(h)
	case ShapeS30:
		h, ok := m.Handler.(HandlerS30)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS30(h)
	case ShapeS31:
		h, ok := m.Handler.(HandlerS31)
		if !ok {
			return 0, nil, errInvalidHandlerType
		}
		return acquireSlotS31(h)
	}
	return 0, nil, fmt.Errorf("stubs: unknown shape %d", m.Shape)
}

// errPoolFmt produces the user-facing message for a given shape's pool
// exhaustion.
// Centralized so each shape registry references a single template.
func errPoolFmt(shape string, poolCap uint32) error {
	return fmt.Errorf("stubs: shape %s stub pool exhausted (cap=%d)", shape, poolCap)
}

// dispatchErr returns the formatted error string a stub returns when its
// slot was acquired but a runtime invariant was broken (unknown slot index,
// missing handler).
// Centralized so each shape dispatcher reports identically.
func dispatchErr(slot uint32, what string) string {
	return fmt.Sprintf("stubs: slot %d %s", slot, what)
}
