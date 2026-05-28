package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS7 is the per-method callback for shape S7: func(*T) []error.
// Covers multi-error unwrap: (T).Unwrap() []error.
type HandlerS7 = func(recv unsafe.Pointer) []error

type methodDescS7 struct{ handler HandlerS7 }

var (
	slotPoolS7 [poolSizeS7]methodDescS7
	nextSlotS7 atomic.Uint32
)

func acquireSlotS7(h HandlerS7) (pc uintptr, release func(), err error) {
	n := nextSlotS7.Add(1) - 1
	if n >= poolSizeS7 {
		return 0, nil, errPoolFmt("S7", poolSizeS7)
	}
	slotPoolS7[n].handler = h
	return stubsS7[n], func() { slotPoolS7[n].handler = nil }, nil
}

// SlotsUsedS7 reports how many S7 stub slots have been consumed.
func SlotsUsedS7() uint32 { return nextSlotS7.Load() }

// dispatchS7 returns nil on a broken invariant so a stray result never
// pollutes an errors-tree walk.
//
//go:nosplit
func dispatchS7(slot uint32, recv unsafe.Pointer) []error {
	if slot >= poolSizeS7 {
		return nil
	}
	d := &slotPoolS7[slot]
	if d.handler == nil {
		return nil
	}
	return d.handler(recv)
}
