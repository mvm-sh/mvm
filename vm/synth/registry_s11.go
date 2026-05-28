package synth

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS11 is the per-method callback for shape S11: func(*T, any).
// Covers heap.Interface.Push (no result).
type HandlerS11 = func(recv unsafe.Pointer, x any)

type methodDescS11 struct{ handler HandlerS11 }

var (
	slotPoolS11 [poolSizeS11]methodDescS11
	nextSlotS11 atomic.Uint32
)

func acquireSlotS11(h HandlerS11) (pc uintptr, release func(), err error) {
	n := nextSlotS11.Add(1) - 1
	if n >= poolSizeS11 {
		return 0, nil, errPoolFmt("S11", poolSizeS11)
	}
	slotPoolS11[n].handler = h
	return stubsS11[n], func() { slotPoolS11[n].handler = nil }, nil
}

// SlotsUsedS11 reports how many S11 stub slots have been consumed.
func SlotsUsedS11() uint32 { return nextSlotS11.Load() }

//go:nosplit
func dispatchS11(slot uint32, recv unsafe.Pointer, x any) {
	if slot >= poolSizeS11 {
		return
	}
	d := &slotPoolS11[slot]
	if d.handler == nil {
		return
	}
	d.handler(recv, x)
}
