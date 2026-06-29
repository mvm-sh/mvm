package stubs

import (
	"context"
	"log/slog"
	"sync/atomic"
	"unsafe"
)

// HandlerS32 is the per-method callback for shape S32: func(*T, context.Context, slog.Level) bool.
// Covers slog.Handler.Enabled.
type HandlerS32 = func(recv unsafe.Pointer, ctx context.Context, level slog.Level) bool

type methodDescS32 struct{ handler HandlerS32 }

var (
	slotPoolS32 [poolSizeS32]methodDescS32
	nextSlotS32 atomic.Uint32
)

func acquireSlotS32(h HandlerS32) (pc uintptr, release func(), err error) {
	n := nextSlotS32.Add(1) - 1
	if n >= poolSizeS32 {
		return 0, nil, errPoolFmt("S32", poolSizeS32)
	}
	slotPoolS32[n].handler = h
	return stubsS32[n], func() { slotPoolS32[n].handler = nil }, nil
}

// SlotsUsedS32 reports how many S32 stub slots have been consumed.
func SlotsUsedS32() uint32 { return nextSlotS32.Load() }

//go:nosplit
//nolint:revive // slot and recv must come first: the stub ABI prepends them to the method params.
func dispatchS32(slot uint32, recv unsafe.Pointer, ctx context.Context, level slog.Level) bool {
	if slot >= poolSizeS32 {
		return false
	}
	d := &slotPoolS32[slot]
	if d.handler == nil {
		return false
	}
	return d.handler(recv, ctx, level)
}
