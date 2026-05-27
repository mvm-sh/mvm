package synth

import (
	"fmt"
	"reflect"
	"sync/atomic"
	"unsafe"
)

// Method describes one method to install on a synthesized type.
type Method struct {
	Name     string
	Exported bool
	Sig      reflect.Type // method signature without receiver, e.g. func() string
	Handler  HandlerS1    // Phase 1: all methods are shape S1
}

// HandlerS1 is the per-method callback for shape S1 (func(*T) string).
// recv is the receiver pointer per Go's iface-dispatch convention.
// For non-direct kinds it points at the boxed value; for direct-iface kinds
// it IS the value reinterpreted as a pointer.
type HandlerS1 = func(recv unsafe.Pointer) string

type methodDescS1 struct {
	handler HandlerS1
}

// slotPoolS1 holds the per-slot descriptors for shape S1, indexed by the
// stub's baked-in slot number.
// Phase 2c replaces with a code-generated, configurable pool.
var slotPoolS1 [poolSizeS1]methodDescS1

var nextSlotS1 atomic.Uint32

var errPoolExhausted = fmt.Errorf(
	"synth: shape S1 stub pool exhausted (cap=%d)", poolSizeS1)

// acquireSlotS1 claims a free slot and returns the stub PC for Ifn/Tfn.
func acquireSlotS1(h HandlerS1) (pc uintptr, err error) {
	n := nextSlotS1.Add(1) - 1
	if n >= poolSizeS1 {
		return 0, errPoolExhausted
	}
	slotPoolS1[n].handler = h
	return stubsS1[n], nil
}

// dispatchS1 is the shared dispatcher every stub tail-calls into.
// The slot index was baked into the stub at code-gen time.
//
//go:nosplit
func dispatchS1(slot uint32, recv unsafe.Pointer) string {
	if slot >= poolSizeS1 {
		return fmt.Sprintf("synth: invalid slot %d", slot)
	}
	d := &slotPoolS1[slot]
	if d.handler == nil {
		return fmt.Sprintf("synth: slot %d has no handler", slot)
	}
	return d.handler(recv)
}
