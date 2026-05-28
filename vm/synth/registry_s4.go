package synth

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS4 is the per-method callback for shape S4: func(*T, error) bool.
// Covers errors.Is dispatch: (T).Is(target error) bool.
type HandlerS4 = func(recv unsafe.Pointer, target error) bool

type methodDescS4 struct{ handler HandlerS4 }

var (
	slotPoolS4 [poolSizeS4]methodDescS4
	nextSlotS4 atomic.Uint32
)

func acquireSlotS4(h HandlerS4) (pc uintptr, release func(), err error) {
	n := nextSlotS4.Add(1) - 1
	if n >= poolSizeS4 {
		return 0, nil, errPoolFmt("S4", poolSizeS4)
	}
	slotPoolS4[n].handler = h
	return stubsS4[n], func() { slotPoolS4[n].handler = nil }, nil
}

// SlotsUsedS4 reports how many S4 stub slots have been consumed.
func SlotsUsedS4() uint32 { return nextSlotS4.Load() }

//go:nosplit
func dispatchS4(slot uint32, recv unsafe.Pointer, target error) bool {
	if slot >= poolSizeS4 {
		return false
	}
	d := &slotPoolS4[slot]
	if d.handler == nil {
		return false
	}
	return d.handler(recv, target)
}
