package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS22 is the per-method callback for shape S22: func(*T) int64.
// Covers fs.FileInfo.Size.
type HandlerS22 = func(recv unsafe.Pointer) int64

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
func dispatchS22(slot uint32, recv unsafe.Pointer) (out0 int64) {
	if slot >= poolSizeS22 {
		return
	}
	d := &slotPoolS22[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv)
}
