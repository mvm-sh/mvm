package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS29 is the per-method callback for shape S29: func(*T, string) ([]string, error).
// Covers fs.GlobFS.Glob.
type HandlerS29 = func(recv unsafe.Pointer, pattern string) ([]string, error)

type methodDescS29 struct{ handler HandlerS29 }

var (
	slotPoolS29 [poolSizeS29]methodDescS29
	nextSlotS29 atomic.Uint32
)

func acquireSlotS29(h HandlerS29) (pc uintptr, release func(), err error) {
	n := nextSlotS29.Add(1) - 1
	if n >= poolSizeS29 {
		return 0, nil, errPoolFmt("S29", poolSizeS29)
	}
	slotPoolS29[n].handler = h
	return stubsS29[n], func() { slotPoolS29[n].handler = nil }, nil
}

// SlotsUsedS29 reports how many S29 stub slots have been consumed.
func SlotsUsedS29() uint32 { return nextSlotS29.Load() }

//go:nosplit
func dispatchS29(slot uint32, recv unsafe.Pointer, pattern string) (out0 []string, out1 error) {
	if slot >= poolSizeS29 {
		return
	}
	d := &slotPoolS29[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv, pattern)
}
