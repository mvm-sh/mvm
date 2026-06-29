package stubs

import (
	"fmt"
	"sync/atomic"
	"unsafe"
)

// HandlerS20 is the per-method callback for shape S20: func(*T, string) error.
// Covers flag.Value.Set.
type HandlerS20 = func(recv unsafe.Pointer, value string) error

type methodDescS20 struct{ handler HandlerS20 }

var (
	slotPoolS20 [poolSizeS20]methodDescS20
	nextSlotS20 atomic.Uint32
)

func acquireSlotS20(h HandlerS20) (pc uintptr, release func(), err error) {
	n := nextSlotS20.Add(1) - 1
	if n >= poolSizeS20 {
		return 0, nil, errPoolFmt("S20", poolSizeS20)
	}
	slotPoolS20[n].handler = h
	return stubsS20[n], func() { slotPoolS20[n].handler = nil }, nil
}

// SlotsUsedS20 reports how many S20 stub slots have been consumed.
func SlotsUsedS20() uint32 { return nextSlotS20.Load() }

//go:nosplit
func dispatchS20(slot uint32, recv unsafe.Pointer, value string) error {
	if slot >= poolSizeS20 {
		return fmt.Errorf("synth: slot %d invalid", slot)
	}
	d := &slotPoolS20[slot]
	if d.handler == nil {
		return fmt.Errorf("synth: slot %d has no handler", slot)
	}
	return d.handler(recv, value)
}
