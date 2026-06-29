package stubs

import (
	"fmt"
	"sync/atomic"
	"unsafe"
)

// HandlerS14 is the per-method callback for shape S14: func(*T, fmt.State, rune).
// Covers fmt.Formatter.Format (no result).
type HandlerS14 = func(recv unsafe.Pointer, st fmt.State, verb rune)

type methodDescS14 struct{ handler HandlerS14 }

var (
	slotPoolS14 [poolSizeS14]methodDescS14
	nextSlotS14 atomic.Uint32
)

func acquireSlotS14(h HandlerS14) (pc uintptr, release func(), err error) {
	n := nextSlotS14.Add(1) - 1
	if n >= poolSizeS14 {
		return 0, nil, errPoolFmt("S14", poolSizeS14)
	}
	slotPoolS14[n].handler = h
	return stubsS14[n], func() { slotPoolS14[n].handler = nil }, nil
}

// SlotsUsedS14 reports how many S14 stub slots have been consumed.
func SlotsUsedS14() uint32 { return nextSlotS14.Load() }

//go:nosplit
func dispatchS14(slot uint32, recv unsafe.Pointer, st fmt.State, verb rune) {
	if slot >= poolSizeS14 {
		return
	}
	d := &slotPoolS14[slot]
	if d.handler == nil {
		return
	}
	d.handler(recv, st, verb)
}
