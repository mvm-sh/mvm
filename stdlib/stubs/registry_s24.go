package stubs

import (
	"sync/atomic"
	"time"
	"unsafe"
)

// HandlerS24 is the per-method callback for shape S24: func(*T) time.Time.
// Covers fs.FileInfo.ModTime.
type HandlerS24 = func(recv unsafe.Pointer) time.Time

type methodDescS24 struct{ handler HandlerS24 }

var (
	slotPoolS24 [poolSizeS24]methodDescS24
	nextSlotS24 atomic.Uint32
)

func acquireSlotS24(h HandlerS24) (pc uintptr, release func(), err error) {
	n := nextSlotS24.Add(1) - 1
	if n >= poolSizeS24 {
		return 0, nil, errPoolFmt("S24", poolSizeS24)
	}
	slotPoolS24[n].handler = h
	return stubsS24[n], func() { slotPoolS24[n].handler = nil }, nil
}

// SlotsUsedS24 reports how many S24 stub slots have been consumed.
func SlotsUsedS24() uint32 { return nextSlotS24.Load() }

//go:nosplit
func dispatchS24(slot uint32, recv unsafe.Pointer) (out0 time.Time) {
	if slot >= poolSizeS24 {
		return
	}
	d := &slotPoolS24[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv)
}
