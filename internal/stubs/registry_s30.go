package stubs

import (
	"io/fs"
	"sync/atomic"
	"unsafe"
)

// HandlerS30 is the per-method callback for shape S30: func(*T, string) ([]fs.DirEntry, error).
// Covers fs.ReadDirFS.ReadDir.
type HandlerS30 = func(recv unsafe.Pointer, name string) ([]fs.DirEntry, error)

type methodDescS30 struct{ handler HandlerS30 }

var (
	slotPoolS30 [poolSizeS30]methodDescS30
	nextSlotS30 atomic.Uint32
)

func acquireSlotS30(h HandlerS30) (pc uintptr, release func(), err error) {
	n := nextSlotS30.Add(1) - 1
	if n >= poolSizeS30 {
		return 0, nil, errPoolFmt("S30", poolSizeS30)
	}
	slotPoolS30[n].handler = h
	return stubsS30[n], func() { slotPoolS30[n].handler = nil }, nil
}

// SlotsUsedS30 reports how many S30 stub slots have been consumed.
func SlotsUsedS30() uint32 { return nextSlotS30.Load() }

//go:nosplit
func dispatchS30(slot uint32, recv unsafe.Pointer, name string) (out0 []fs.DirEntry, out1 error) {
	if slot >= poolSizeS30 {
		return
	}
	d := &slotPoolS30[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv, name)
}
