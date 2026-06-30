package stubs

import (
	"fmt"
	"sync/atomic"
	"unsafe"
)

// HandlerS2 is the per-method callback for shape S2: func(*T) ([]byte, error).
// Covers MarshalJSON, MarshalBinary, MarshalText.
type HandlerS2 = func(recv unsafe.Pointer) ([]byte, error)

type methodDescS2 struct{ handler HandlerS2 }

var (
	slotPoolS2 [poolSizeS2]methodDescS2
	nextSlotS2 atomic.Uint32
)

func acquireSlotS2(h HandlerS2) (pc uintptr, release func(), err error) {
	n := nextSlotS2.Add(1) - 1
	if n >= poolSizeS2 {
		return 0, nil, errPoolFmt("S2", poolSizeS2)
	}
	slotPoolS2[n].handler = h
	return stubsS2[n], func() { slotPoolS2[n].handler = nil }, nil
}

// SlotsUsedS2 reports how many S2 stub slots have been consumed.
func SlotsUsedS2() uint32 { return nextSlotS2.Load() }

//go:nosplit
func dispatchS2(slot uint32, recv unsafe.Pointer) ([]byte, error) {
	if slot >= poolSizeS2 {
		return nil, fmt.Errorf("synth: slot %d invalid", slot)
	}
	d := &slotPoolS2[slot]
	if d.handler == nil {
		return nil, fmt.Errorf("synth: slot %d has no handler", slot)
	}
	return d.handler(recv)
}
