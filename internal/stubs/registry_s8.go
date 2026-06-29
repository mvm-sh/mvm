package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS8 is the per-method callback for shape S8: func(*T) int.
// Covers sort.Interface.Len.
type HandlerS8 = func(recv unsafe.Pointer) int

type methodDescS8 struct{ handler HandlerS8 }

var (
	slotPoolS8 [poolSizeS8]methodDescS8
	nextSlotS8 atomic.Uint32
)

func acquireSlotS8(h HandlerS8) (pc uintptr, release func(), err error) {
	n := nextSlotS8.Add(1) - 1
	if n >= poolSizeS8 {
		return 0, nil, errPoolFmt("S8", poolSizeS8)
	}
	slotPoolS8[n].handler = h
	return stubsS8[n], func() { slotPoolS8[n].handler = nil }, nil
}

// SlotsUsedS8 reports how many S8 stub slots have been consumed.
func SlotsUsedS8() uint32 { return nextSlotS8.Load() }

//go:nosplit
func dispatchS8(slot uint32, recv unsafe.Pointer) int {
	if slot >= poolSizeS8 {
		return 0
	}
	d := &slotPoolS8[slot]
	if d.handler == nil {
		return 0
	}
	return d.handler(recv)
}
