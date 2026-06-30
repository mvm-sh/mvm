package stubs

import (
	"io/fs"
	"sync/atomic"
	"unsafe"
)

// HandlerS27 is the per-method callback for shape S27: func(*T, string) (fs.FileInfo, error).
// Covers fs.StatFS.Stat.
type HandlerS27 = func(recv unsafe.Pointer, name string) (fs.FileInfo, error)

type methodDescS27 struct{ handler HandlerS27 }

var (
	slotPoolS27 [poolSizeS27]methodDescS27
	nextSlotS27 atomic.Uint32
)

func acquireSlotS27(h HandlerS27) (pc uintptr, release func(), err error) {
	n := nextSlotS27.Add(1) - 1
	if n >= poolSizeS27 {
		return 0, nil, errPoolFmt("S27", poolSizeS27)
	}
	slotPoolS27[n].handler = h
	return stubsS27[n], func() { slotPoolS27[n].handler = nil }, nil
}

// SlotsUsedS27 reports how many S27 stub slots have been consumed.
func SlotsUsedS27() uint32 { return nextSlotS27.Load() }

//go:nosplit
func dispatchS27(slot uint32, recv unsafe.Pointer, name string) (out0 fs.FileInfo, out1 error) {
	if slot >= poolSizeS27 {
		return
	}
	d := &slotPoolS27[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv, name)
}
