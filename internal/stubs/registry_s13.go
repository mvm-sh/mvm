package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS13 is the per-method callback for shape S13: func(*T, []byte) (int, error).
// Covers io.Reader.Read and io.Writer.Write.
type HandlerS13 = func(recv unsafe.Pointer, p []byte) (int, error)

type methodDescS13 struct{ handler HandlerS13 }

var (
	slotPoolS13 [poolSizeS13]methodDescS13
	nextSlotS13 atomic.Uint32
)

func acquireSlotS13(h HandlerS13) (pc uintptr, release func(), err error) {
	n := nextSlotS13.Add(1) - 1
	if n >= poolSizeS13 {
		return 0, nil, errPoolFmt("S13", poolSizeS13)
	}
	slotPoolS13[n].handler = h
	return stubsS13[n], func() { slotPoolS13[n].handler = nil }, nil
}

// SlotsUsedS13 reports how many S13 stub slots have been consumed.
func SlotsUsedS13() uint32 { return nextSlotS13.Load() }

//go:nosplit
func dispatchS13(slot uint32, recv unsafe.Pointer, p []byte) (int, error) {
	if slot >= poolSizeS13 {
		return 0, nil
	}
	d := &slotPoolS13[slot]
	if d.handler == nil {
		return 0, nil
	}
	return d.handler(recv, p)
}
