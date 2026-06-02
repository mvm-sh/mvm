package stubs

import (
	"sync/atomic"
	"time"
	"unsafe"
)

// HandlerS22 is the per-method callback for shape S22: func(*T) time.Time.
// time.Time is a word-sized-leaf struct, but until the word-class path flattens
// such structs it gets a typed shape so the compiler emits its struct ABI.
type HandlerS22 = func(recv unsafe.Pointer) time.Time

type methodDescS22 struct{ handler HandlerS22 }

var (
	slotPoolS22 [poolSizeS22]methodDescS22
	nextSlotS22 atomic.Uint32
)

func acquireSlotS22(h HandlerS22) (pc uintptr, release func(), err error) {
	n := nextSlotS22.Add(1) - 1
	if n >= poolSizeS22 {
		return 0, nil, errPoolFmt("S22", poolSizeS22)
	}
	slotPoolS22[n].handler = h
	return stubsS22[n], func() { slotPoolS22[n].handler = nil }, nil
}

// SlotsUsedS22 reports how many S22 stub slots have been consumed.
func SlotsUsedS22() uint32 { return nextSlotS22.Load() }

//go:nosplit
func dispatchS22(slot uint32, recv unsafe.Pointer) time.Time {
	if slot >= poolSizeS22 {
		return time.Time{}
	}
	d := &slotPoolS22[slot]
	if d.handler == nil {
		return time.Time{}
	}
	return d.handler(recv)
}
