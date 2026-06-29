package stubs

import (
	"context"
	"log/slog"
	"sync/atomic"
	"unsafe"
)

// HandlerS33 is the per-method callback for shape S33: func(*T, context.Context, slog.Record) error.
// Covers slog.Handler.Handle.
type HandlerS33 = func(recv unsafe.Pointer, ctx context.Context, record slog.Record) error

type methodDescS33 struct{ handler HandlerS33 }

var (
	slotPoolS33 [poolSizeS33]methodDescS33
	nextSlotS33 atomic.Uint32
)

func acquireSlotS33(h HandlerS33) (pc uintptr, release func(), err error) {
	n := nextSlotS33.Add(1) - 1
	if n >= poolSizeS33 {
		return 0, nil, errPoolFmt("S33", poolSizeS33)
	}
	slotPoolS33[n].handler = h
	return stubsS33[n], func() { slotPoolS33[n].handler = nil }, nil
}

// SlotsUsedS33 reports how many S33 stub slots have been consumed.
func SlotsUsedS33() uint32 { return nextSlotS33.Load() }

//go:nosplit
//nolint:revive // slot and recv must come first: the stub ABI prepends them to the method params.
func dispatchS33(slot uint32, recv unsafe.Pointer, ctx context.Context, record slog.Record) error {
	if slot >= poolSizeS33 {
		return nil
	}
	d := &slotPoolS33[slot]
	if d.handler == nil {
		return nil
	}
	return d.handler(recv, ctx, record)
}
