package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS18 is the per-method callback for shape S18: func(*T, int) bool.
// Covers fmt.State.Flag.
type HandlerS18 = func(recv unsafe.Pointer, c int) bool

type methodDescS18 struct{ handler HandlerS18 }

var (
	slotPoolS18 [poolSizeS18]methodDescS18
	nextSlotS18 atomic.Uint32
)

func acquireSlotS18(h HandlerS18) (pc uintptr, release func(), err error) {
	n := nextSlotS18.Add(1) - 1
	if n >= poolSizeS18 {
		return 0, nil, errPoolFmt("S18", poolSizeS18)
	}
	slotPoolS18[n].handler = h
	return stubsS18[n], func() { slotPoolS18[n].handler = nil }, nil
}

// SlotsUsedS18 reports how many S18 stub slots have been consumed.
func SlotsUsedS18() uint32 { return nextSlotS18.Load() }

//go:nosplit
func dispatchS18(slot uint32, recv unsafe.Pointer, c int) bool {
	if slot >= poolSizeS18 {
		return false
	}
	d := &slotPoolS18[slot]
	if d.handler == nil {
		return false
	}
	return d.handler(recv, c)
}
