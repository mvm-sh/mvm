package synth

import (
	"encoding/xml"
	"sync/atomic"
	"unsafe"
)

// HandlerS16 is the per-method callback for shape S16:
// func(*T, *xml.Decoder, xml.StartElement) error.
// Covers xml.Unmarshaler.UnmarshalXML.
type HandlerS16 = func(recv unsafe.Pointer, d *xml.Decoder, start xml.StartElement) error

type methodDescS16 struct{ handler HandlerS16 }

var (
	slotPoolS16 [poolSizeS16]methodDescS16
	nextSlotS16 atomic.Uint32
)

func acquireSlotS16(h HandlerS16) (pc uintptr, release func(), err error) {
	n := nextSlotS16.Add(1) - 1
	if n >= poolSizeS16 {
		return 0, nil, errPoolFmt("S16", poolSizeS16)
	}
	slotPoolS16[n].handler = h
	return stubsS16[n], func() { slotPoolS16[n].handler = nil }, nil
}

// SlotsUsedS16 reports how many S16 stub slots have been consumed.
func SlotsUsedS16() uint32 { return nextSlotS16.Load() }

//go:nosplit
func dispatchS16(slot uint32, recv unsafe.Pointer, d *xml.Decoder, start xml.StartElement) error {
	if slot >= poolSizeS16 {
		return nil
	}
	ds := &slotPoolS16[slot]
	if ds.handler == nil {
		return nil
	}
	return ds.handler(recv, d, start)
}
