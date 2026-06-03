package stubs

import (
	"sync/atomic"
	"unsafe"
)

// HandlerS31 is the per-method callback for shape S31: func(*T, string) ([]byte, error).
// Covers fs.ReadFileFS.ReadFile.
type HandlerS31 = func(recv unsafe.Pointer, name string) ([]byte, error)

type methodDescS31 struct{ handler HandlerS31 }

var (
	slotPoolS31 [poolSizeS31]methodDescS31
	nextSlotS31 atomic.Uint32
)

func acquireSlotS31(h HandlerS31) (pc uintptr, release func(), err error) {
	n := nextSlotS31.Add(1) - 1
	if n >= poolSizeS31 {
		return 0, nil, errPoolFmt("S31", poolSizeS31)
	}
	slotPoolS31[n].handler = h
	return stubsS31[n], func() { slotPoolS31[n].handler = nil }, nil
}

// SlotsUsedS31 reports how many S31 stub slots have been consumed.
func SlotsUsedS31() uint32 { return nextSlotS31.Load() }

//go:nosplit
func dispatchS31(slot uint32, recv unsafe.Pointer, name string) (out0 []byte, out1 error) {
	if slot >= poolSizeS31 {
		return
	}
	d := &slotPoolS31[slot]
	if d.handler == nil {
		return
	}
	return d.handler(recv, name)
}
