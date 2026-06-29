package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS37 is the per-method callback for shape S37: func(*T) (rune, int, error).
// Covers io.RuneReader.ReadRune.
type HandlerS37 = func(recv unsafe.Pointer) (rune, int, error)

type methodDescS37 struct{ handler HandlerS37 }

var (
	slotPoolS37 [poolSizeS37]methodDescS37
	nextSlotS37 atomic.Uint32
)

func acquireSlotS37(h HandlerS37) (pc uintptr, release func(), err error) {
	n := nextSlotS37.Add(1) - 1
	if n >= poolSizeS37 {
		return 0, nil, errPoolFmt("S37", poolSizeS37)
	}
	slotPoolS37[n].handler = h
	return stubsS37[n], func() { slotPoolS37[n].handler = nil }, nil
}

// SlotsUsedS37 reports how many S37 stub slots have been consumed.
func SlotsUsedS37() uint32 { return nextSlotS37.Load() }

//go:nosplit
func dispatchS37(slot uint32, recv unsafe.Pointer) (rune, int, error) {
	if slot >= poolSizeS37 {
		return 0, 0, nil
	}
	d := &slotPoolS37[slot]
	if d.handler == nil {
		return 0, 0, nil
	}
	return d.handler(recv)
}
