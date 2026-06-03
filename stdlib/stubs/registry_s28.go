package stubs

import (
	"io/fs"
	"sync/atomic"
	"unsafe"
)

// HandlerS28 is the per-method callback for shape S28: func(*T, string) (fs.FS, error).
// Covers fs.SubFS.Sub.
type HandlerS28 = func(recv unsafe.Pointer, dir string) (fs.FS, error)

type methodDescS28 struct{ handler HandlerS28 }

var (
	slotPoolS28 [poolSizeS28]methodDescS28
	nextSlotS28 atomic.Uint32
)

func acquireSlotS28(h HandlerS28) (pc uintptr, release func(), err error) {
	n := nextSlotS28.Add(1) - 1
	if n >= poolSizeS28 {
		return 0, nil, errPoolFmt("S28", poolSizeS28)
	}
	slotPoolS28[n].handler = h
	return stubsS28[n], func() { slotPoolS28[n].handler = nil }, nil
}

// SlotsUsedS28 reports how many S28 stub slots have been consumed.
func SlotsUsedS28() uint32 { return nextSlotS28.Load() }

//go:nosplit
func dispatchS28(slot uint32, recv unsafe.Pointer, dir string) (out0 fs.FS, out1 error) {
	if slot >= poolSizeS28 {
		return
	}
	d := &slotPoolS28[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv, dir)
}
