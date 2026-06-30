package stubs

import (
	"encoding/xml"
	"sync/atomic"
	"unsafe"
)

// HandlerS15 is the per-method callback for shape S15:
// func(*T, *xml.Encoder, xml.StartElement) error.
// Covers xml.Marshaler.MarshalXML.
type HandlerS15 = func(recv unsafe.Pointer, e *xml.Encoder, start xml.StartElement) error

type methodDescS15 struct{ handler HandlerS15 }

var (
	slotPoolS15 [poolSizeS15]methodDescS15
	nextSlotS15 atomic.Uint32
)

func acquireSlotS15(h HandlerS15) (pc uintptr, release func(), err error) {
	n := nextSlotS15.Add(1) - 1
	if n >= poolSizeS15 {
		return 0, nil, errPoolFmt("S15", poolSizeS15)
	}
	slotPoolS15[n].handler = h
	return stubsS15[n], func() { slotPoolS15[n].handler = nil }, nil
}

// SlotsUsedS15 reports how many S15 stub slots have been consumed.
func SlotsUsedS15() uint32 { return nextSlotS15.Load() }

//go:nosplit
func dispatchS15(slot uint32, recv unsafe.Pointer, e *xml.Encoder, start xml.StartElement) error {
	if slot >= poolSizeS15 {
		return nil
	}
	d := &slotPoolS15[slot]
	if d.handler == nil {
		return nil
	}
	return d.handler(recv, e, start)
}
