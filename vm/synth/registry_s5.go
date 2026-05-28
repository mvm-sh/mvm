package synth

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS5 is the per-method callback for shape S5: func(*T, any) bool.
// Covers errors.As dispatch: (T).As(target any) bool, where target is a
// non-nil pointer the method writes through.
type HandlerS5 = func(recv unsafe.Pointer, target any) bool

type methodDescS5 struct{ handler HandlerS5 }

var (
	slotPoolS5 [poolSizeS5]methodDescS5
	nextSlotS5 atomic.Uint32
)

func acquireSlotS5(h HandlerS5) (pc uintptr, release func(), err error) {
	n := nextSlotS5.Add(1) - 1
	if n >= poolSizeS5 {
		return 0, nil, errPoolFmt("S5", poolSizeS5)
	}
	slotPoolS5[n].handler = h
	return stubsS5[n], func() { slotPoolS5[n].handler = nil }, nil
}

// SlotsUsedS5 reports how many S5 stub slots have been consumed.
func SlotsUsedS5() uint32 { return nextSlotS5.Load() }

//go:nosplit
func dispatchS5(slot uint32, recv unsafe.Pointer, target any) bool {
	if slot >= poolSizeS5 {
		return false
	}
	d := &slotPoolS5[slot]
	if d.handler == nil {
		return false
	}
	return d.handler(recv, target)
}
