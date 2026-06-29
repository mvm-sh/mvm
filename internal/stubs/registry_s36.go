package stubs

import (
	"log/slog"
	"sync/atomic"
	"unsafe"
)

// HandlerS36 is the per-method callback for shape S36: func(*T) slog.Value.
// Covers slog.LogValuer.LogValue.
type HandlerS36 = func(recv unsafe.Pointer) slog.Value

type methodDescS36 struct{ handler HandlerS36 }

var (
	slotPoolS36 [poolSizeS36]methodDescS36
	nextSlotS36 atomic.Uint32
)

func acquireSlotS36(h HandlerS36) (pc uintptr, release func(), err error) {
	n := nextSlotS36.Add(1) - 1
	if n >= poolSizeS36 {
		return 0, nil, errPoolFmt("S36", poolSizeS36)
	}
	slotPoolS36[n].handler = h
	return stubsS36[n], func() { slotPoolS36[n].handler = nil }, nil
}

// SlotsUsedS36 reports how many S36 stub slots have been consumed.
func SlotsUsedS36() uint32 { return nextSlotS36.Load() }

//go:nosplit
func dispatchS36(slot uint32, recv unsafe.Pointer) slog.Value {
	if slot >= poolSizeS36 {
		return slog.Value{}
	}
	d := &slotPoolS36[slot]
	if d.handler == nil {
		return slog.Value{}
	}
	return d.handler(recv)
}
