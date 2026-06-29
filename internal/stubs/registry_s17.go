package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS17 is the per-method callback for shape S17: func(*T) (int, bool).
// Covers fmt.State.Width and fmt.State.Precision.
type HandlerS17 = func(recv unsafe.Pointer) (int, bool)

type methodDescS17 struct{ handler HandlerS17 }

var (
	slotPoolS17 [poolSizeS17]methodDescS17
	nextSlotS17 atomic.Uint32
)

func acquireSlotS17(h HandlerS17) (pc uintptr, release func(), err error) {
	n := nextSlotS17.Add(1) - 1
	if n >= poolSizeS17 {
		return 0, nil, errPoolFmt("S17", poolSizeS17)
	}
	slotPoolS17[n].handler = h
	return stubsS17[n], func() { slotPoolS17[n].handler = nil }, nil
}

// SlotsUsedS17 reports how many S17 stub slots have been consumed.
func SlotsUsedS17() uint32 { return nextSlotS17.Load() }

//go:nosplit
func dispatchS17(slot uint32, recv unsafe.Pointer) (int, bool) {
	if slot >= poolSizeS17 {
		return 0, false
	}
	d := &slotPoolS17[slot]
	if d.handler == nil {
		return 0, false
	}
	return d.handler(recv)
}
