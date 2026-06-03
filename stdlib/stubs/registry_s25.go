package stubs

import (
	"io/fs"
	"sync/atomic"
	"unsafe"
)

// HandlerS25 is the per-method callback for shape S25: func(*T) (fs.FileInfo, error).
// Covers fs.DirEntry.Info and fs.File.Stat.
type HandlerS25 = func(recv unsafe.Pointer) (fs.FileInfo, error)

type methodDescS25 struct{ handler HandlerS25 }

var (
	slotPoolS25 [poolSizeS25]methodDescS25
	nextSlotS25 atomic.Uint32
)

func acquireSlotS25(h HandlerS25) (pc uintptr, release func(), err error) {
	n := nextSlotS25.Add(1) - 1
	if n >= poolSizeS25 {
		return 0, nil, errPoolFmt("S25", poolSizeS25)
	}
	slotPoolS25[n].handler = h
	return stubsS25[n], func() { slotPoolS25[n].handler = nil }, nil
}

// SlotsUsedS25 reports how many S25 stub slots have been consumed.
func SlotsUsedS25() uint32 { return nextSlotS25.Load() }

//go:nosplit
func dispatchS25(slot uint32, recv unsafe.Pointer) (out0 fs.FileInfo, out1 error) {
	if slot >= poolSizeS25 {
		return
	}
	d := &slotPoolS25[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv)
}
