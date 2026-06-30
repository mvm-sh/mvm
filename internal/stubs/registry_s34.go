package stubs

import (
	"log/slog"
	"sync/atomic"
	"unsafe"
)

// HandlerS34 is the per-method callback for shape S34: func(*T, []slog.Attr) slog.Handler.
// Covers slog.Handler.WithAttrs.
type HandlerS34 = func(recv unsafe.Pointer, attrs []slog.Attr) slog.Handler

type methodDescS34 struct{ handler HandlerS34 }

var (
	slotPoolS34 [poolSizeS34]methodDescS34
	nextSlotS34 atomic.Uint32
)

func acquireSlotS34(h HandlerS34) (pc uintptr, release func(), err error) {
	n := nextSlotS34.Add(1) - 1
	if n >= poolSizeS34 {
		return 0, nil, errPoolFmt("S34", poolSizeS34)
	}
	slotPoolS34[n].handler = h
	return stubsS34[n], func() { slotPoolS34[n].handler = nil }, nil
}

// SlotsUsedS34 reports how many S34 stub slots have been consumed.
func SlotsUsedS34() uint32 { return nextSlotS34.Load() }

//go:nosplit
func dispatchS34(slot uint32, recv unsafe.Pointer, attrs []slog.Attr) slog.Handler {
	if slot >= poolSizeS34 {
		return nil
	}
	d := &slotPoolS34[slot]
	if d.handler == nil {
		return nil
	}
	return d.handler(recv, attrs)
}
