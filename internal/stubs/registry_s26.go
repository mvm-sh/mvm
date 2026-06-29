package stubs

import (
	"io/fs"
	"sync/atomic"
	"unsafe"
)

// HandlerS26 is the per-method callback for shape S26: func(*T, string) (fs.File, error).
// Covers fs.FS.Open.
type HandlerS26 = func(recv unsafe.Pointer, name string) (fs.File, error)

type methodDescS26 struct{ handler HandlerS26 }

var (
	slotPoolS26 [poolSizeS26]methodDescS26
	nextSlotS26 atomic.Uint32
)

func acquireSlotS26(h HandlerS26) (pc uintptr, release func(), err error) {
	n := nextSlotS26.Add(1) - 1
	if n >= poolSizeS26 {
		return 0, nil, errPoolFmt("S26", poolSizeS26)
	}
	slotPoolS26[n].handler = h
	return stubsS26[n], func() { slotPoolS26[n].handler = nil }, nil
}

// SlotsUsedS26 reports how many S26 stub slots have been consumed.
func SlotsUsedS26() uint32 { return nextSlotS26.Load() }

//go:nosplit
func dispatchS26(slot uint32, recv unsafe.Pointer, name string) (out0 fs.File, out1 error) {
	if slot >= poolSizeS26 {
		return
	}
	d := &slotPoolS26[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv, name)
}
