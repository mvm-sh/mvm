package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS10 is the per-method callback for shape S10: func(*T, int, int).
// Covers sort.Interface.Swap (no result).
type HandlerS10 = func(recv unsafe.Pointer, i, j int)

type methodDescS10 struct{ handler HandlerS10 }

var (
	slotPoolS10 [poolSizeS10]methodDescS10
	nextSlotS10 atomic.Uint32
)

func acquireSlotS10(h HandlerS10) (pc uintptr, release func(), err error) {
	n := nextSlotS10.Add(1) - 1
	if n >= poolSizeS10 {
		return 0, nil, errPoolFmt("S10", poolSizeS10)
	}
	slotPoolS10[n].handler = h
	return stubsS10[n], func() { slotPoolS10[n].handler = nil }, nil
}

// SlotsUsedS10 reports how many S10 stub slots have been consumed.
func SlotsUsedS10() uint32 { return nextSlotS10.Load() }

//go:nosplit
func dispatchS10(slot uint32, recv unsafe.Pointer, i, j int) {
	if slot >= poolSizeS10 {
		return
	}
	d := &slotPoolS10[slot]
	if d.handler == nil {
		return
	}
	d.handler(recv, i, j)
}
