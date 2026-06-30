package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS38 is the per-method callback for shape S38: func(*T).
// Covers niladic marker methods with no results.
type HandlerS38 = func(recv unsafe.Pointer)

type methodDescS38 struct{ handler HandlerS38 }

var (
	slotPoolS38 [poolSizeS38]methodDescS38
	nextSlotS38 atomic.Uint32
)

func acquireSlotS38(h HandlerS38) (pc uintptr, release func(), err error) {
	n := nextSlotS38.Add(1) - 1
	if n >= poolSizeS38 {
		return 0, nil, errPoolFmt("S38", poolSizeS38)
	}
	slotPoolS38[n].handler = h
	return stubsS38[n], func() { slotPoolS38[n].handler = nil }, nil
}

// SlotsUsedS38 reports how many S38 stub slots have been consumed.
func SlotsUsedS38() uint32 { return nextSlotS38.Load() }

//go:nosplit
func dispatchS38(slot uint32, recv unsafe.Pointer) {
	if slot >= poolSizeS38 {
		return
	}
	d := &slotPoolS38[slot]
	if d.handler == nil {
		return
	}
	d.handler(recv)
}
