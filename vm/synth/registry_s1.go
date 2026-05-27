package synth

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS1 is the per-method callback for shape S1: func(*T) string.
// recv is the receiver pointer per Go's iface-dispatch convention.
// For non-direct kinds it points at the boxed value; for direct-iface kinds
// it IS the value reinterpreted as a pointer.
type HandlerS1 = func(recv unsafe.Pointer) string

type methodDescS1 struct{ handler HandlerS1 }

var (
	slotPoolS1 [poolSizeS1]methodDescS1
	nextSlotS1 atomic.Uint32
)

func acquireSlotS1(h HandlerS1) (pc uintptr, err error) {
	n := nextSlotS1.Add(1) - 1
	if n >= poolSizeS1 {
		return 0, errPoolFmt("S1", poolSizeS1)
	}
	slotPoolS1[n].handler = h
	return stubsS1[n], nil
}

// SlotsUsedS1 reports how many S1 stub slots have been consumed.
// Exported for tests that verify idempotency at the interp layer.
func SlotsUsedS1() uint32 { return nextSlotS1.Load() }

//go:nosplit
func dispatchS1(slot uint32, recv unsafe.Pointer) string {
	if slot >= poolSizeS1 {
		return dispatchErr(slot, "invalid")
	}
	d := &slotPoolS1[slot]
	if d.handler == nil {
		return dispatchErr(slot, "has no handler")
	}
	return d.handler(recv)
}
