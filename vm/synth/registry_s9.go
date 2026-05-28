package synth

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS9 is the per-method callback for shape S9: func(*T, int, int) bool.
// Covers sort.Interface.Less.
type HandlerS9 = func(recv unsafe.Pointer, i, j int) bool

type methodDescS9 struct{ handler HandlerS9 }

var (
	slotPoolS9 [poolSizeS9]methodDescS9
	nextSlotS9 atomic.Uint32
)

func acquireSlotS9(h HandlerS9) (pc uintptr, release func(), err error) {
	n := nextSlotS9.Add(1) - 1
	if n >= poolSizeS9 {
		return 0, nil, errPoolFmt("S9", poolSizeS9)
	}
	slotPoolS9[n].handler = h
	return stubsS9[n], func() { slotPoolS9[n].handler = nil }, nil
}

// SlotsUsedS9 reports how many S9 stub slots have been consumed.
func SlotsUsedS9() uint32 { return nextSlotS9.Load() }

//go:nosplit
func dispatchS9(slot uint32, recv unsafe.Pointer, i, j int) bool {
	if slot >= poolSizeS9 {
		return false
	}
	d := &slotPoolS9[slot]
	if d.handler == nil {
		return false
	}
	return d.handler(recv, i, j)
}
