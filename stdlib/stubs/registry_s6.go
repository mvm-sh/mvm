package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS6 is the per-method callback for shape S6: func(*T) error.
// Covers single-error unwrap: (T).Unwrap() error.
type HandlerS6 = func(recv unsafe.Pointer) error

type methodDescS6 struct{ handler HandlerS6 }

var (
	slotPoolS6 [poolSizeS6]methodDescS6
	nextSlotS6 atomic.Uint32
)

func acquireSlotS6(h HandlerS6) (pc uintptr, release func(), err error) {
	n := nextSlotS6.Add(1) - 1
	if n >= poolSizeS6 {
		return 0, nil, errPoolFmt("S6", poolSizeS6)
	}
	slotPoolS6[n].handler = h
	return stubsS6[n], func() { slotPoolS6[n].handler = nil }, nil
}

// SlotsUsedS6 reports how many S6 stub slots have been consumed.
func SlotsUsedS6() uint32 { return nextSlotS6.Load() }

// dispatchS6 returns nil on a broken invariant so a stray result never
// pollutes an errors-tree walk (a non-nil Unwrap would extend the chain).
//
//go:nosplit
func dispatchS6(slot uint32, recv unsafe.Pointer) error {
	if slot >= poolSizeS6 {
		return nil
	}
	d := &slotPoolS6[slot]
	if d.handler == nil {
		return nil
	}
	return d.handler(recv)
}
