package stubs

import (
	"log/slog"
	"sync/atomic"
	"unsafe"
)

// HandlerS35 is the per-method callback for shape S35: func(*T, string) slog.Handler.
// Covers slog.Handler.WithGroup.
type HandlerS35 = func(recv unsafe.Pointer, name string) slog.Handler

type methodDescS35 struct{ handler HandlerS35 }

var (
	slotPoolS35 [poolSizeS35]methodDescS35
	nextSlotS35 atomic.Uint32
)

func acquireSlotS35(h HandlerS35) (pc uintptr, release func(), err error) {
	n := nextSlotS35.Add(1) - 1
	if n >= poolSizeS35 {
		return 0, nil, errPoolFmt("S35", poolSizeS35)
	}
	slotPoolS35[n].handler = h
	return stubsS35[n], func() { slotPoolS35[n].handler = nil }, nil
}

// SlotsUsedS35 reports how many S35 stub slots have been consumed.
func SlotsUsedS35() uint32 { return nextSlotS35.Load() }

//go:nosplit
func dispatchS35(slot uint32, recv unsafe.Pointer, name string) slog.Handler {
	if slot >= poolSizeS35 {
		return nil
	}
	d := &slotPoolS35[slot]
	if d.handler == nil {
		return nil
	}
	return d.handler(recv, name)
}
