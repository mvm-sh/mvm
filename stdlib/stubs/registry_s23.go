package stubs

import (
	"io/fs"
	"sync/atomic"
	"unsafe"
)

// HandlerS23 is the per-method callback for shape S23: func(*T) fs.FileMode.
// Covers fs.FileInfo.Mode and fs.DirEntry.Type.
type HandlerS23 = func(recv unsafe.Pointer) fs.FileMode

type methodDescS23 struct{ handler HandlerS23 }

var (
	slotPoolS23 [poolSizeS23]methodDescS23
	nextSlotS23 atomic.Uint32
)

func acquireSlotS23(h HandlerS23) (pc uintptr, release func(), err error) {
	n := nextSlotS23.Add(1) - 1
	if n >= poolSizeS23 {
		return 0, nil, errPoolFmt("S23", poolSizeS23)
	}
	slotPoolS23[n].handler = h
	return stubsS23[n], func() { slotPoolS23[n].handler = nil }, nil
}

// SlotsUsedS23 reports how many S23 stub slots have been consumed.
func SlotsUsedS23() uint32 { return nextSlotS23.Load() }

//go:nosplit
func dispatchS23(slot uint32, recv unsafe.Pointer) (out0 fs.FileMode) {
	if slot >= poolSizeS23 {
		return
	}
	d := &slotPoolS23[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv)
}
