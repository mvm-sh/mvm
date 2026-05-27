package synth

import (
	"fmt"
	"sync/atomic"
	"unsafe"
)

// HandlerS3 is the per-method callback for shape S3: func(*T, []byte) error.
// Covers UnmarshalJSON, UnmarshalBinary, UnmarshalText.
type HandlerS3 = func(recv unsafe.Pointer, data []byte) error

type methodDescS3 struct{ handler HandlerS3 }

var (
	slotPoolS3 [poolSizeS3]methodDescS3
	nextSlotS3 atomic.Uint32
)

func acquireSlotS3(h HandlerS3) (pc uintptr, err error) {
	n := nextSlotS3.Add(1) - 1
	if n >= poolSizeS3 {
		return 0, errPoolFmt("S3", poolSizeS3)
	}
	slotPoolS3[n].handler = h
	return stubsS3[n], nil
}

// SlotsUsedS3 reports how many S3 stub slots have been consumed.
func SlotsUsedS3() uint32 { return nextSlotS3.Load() }

//go:nosplit
func dispatchS3(slot uint32, recv unsafe.Pointer, data []byte) error {
	if slot >= poolSizeS3 {
		return fmt.Errorf("synth: slot %d invalid", slot)
	}
	d := &slotPoolS3[slot]
	if d.handler == nil {
		return fmt.Errorf("synth: slot %d has no handler", slot)
	}
	return d.handler(recv, data)
}
