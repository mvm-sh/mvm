package vm

import (
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// Word-class synth dispatch (arch-independent core).
//
// A synth method-table stub must have a real text-segment PC whose ABI exactly
// matches the method signature. The ABI is set by the register-word (or, on a
// stack ABI, the stack-slot) classification of the params and results, not their
// exact Go types, so many distinct signatures share one stub family. The
// generated pools (stdlib/stubs/pool_w*.go) carry one stub family + one generic
// dispatcher per word-shape; makeWordCore supplies the per-method marshaling the
// dispatcher calls back into.
//
// The classification and marshaling are arch-specific: register targets flatten
// each aggregate to one word per leaf field (synth_word_regabi.go), while the
// wasm target packs sub-word fields into 8-byte stack slots (synth_word_abi0.go,
// wired in by synth_word_wasm.go). Both classifyWordSig and makeWordCore are
// provided per-arch; everything in this file is shared.
//
// The path requires a 64-bit little-endian target (see wordShapesSupported); on
// any other arch detectWordShape drops everything and only the typed shapes
// (arch-independent) attach, so dispatch is always correct, just less capable.

// forceWordShape, set only by benchmarks, makes attach prefer the word-class path
// over a matching typed shape, so the two dispatch mechanisms can be compared.
var forceWordShape bool

// SetForceWordShape toggles the benchmark-only word-path preference.
func SetForceWordShape(v bool) { forceWordShape = v }

const wordSize = unsafe.Sizeof(uintptr(0))

// wordShapesSupported gates the whole word-class path to a 64-bit little-endian
// target: the classifier treats each scalar/pointer as one 8-byte word (wrong for
// multi-register int64/uint64 on 32-bit) and the byte ops pack low-first (wrong on
// big-endian). The 8-byte check is a compile-time constant, the endian check a
// one-time init probe, so this is true on amd64, arm64, riscv64, ppc64le, wasm,
// etc. and false elsewhere. When false, detectWordShape drops every method to the
// word path -- identical to the pre-word-class behavior (the typed shapes stay
// arch-independent and keep working).
var wordShapesSupported = unsafe.Sizeof(uintptr(0)) == 8 && nativeIsLittleEndian()

func nativeIsLittleEndian() bool {
	x := uint16(1)
	return *(*byte)(unsafe.Pointer(&x)) == 1
}

// classifyWordSig classifies sig into its word-shape key ("params_results"), or
// the drop reason and ok=false. The decomposition is arch-specific: register
// targets flatten each leaf to its own word (regabiClassifyWordSig), the wasm
// stack ABI packs sub-word fields into 8-byte slots (abi0ClassifyWordSig). The
// wordABI0 const picks one; the other branch is eliminated at compile time.
func classifyWordSig(sig reflect.Type) (key, reason string, ok bool) {
	if wordABI0 {
		return abi0ClassifyWordSig(sig)
	}
	return regabiClassifyWordSig(sig)
}

// makeWordCore builds the per-method marshaler the generated dispatcher calls
// back into, selecting the register-ABI or ABI0 implementation per arch.
func (m *Machine) makeWordCore(t *mtype.Type, method mtype.Method, name string, form recvForm, swallowErr bool) stubs.CoreFunc {
	if wordABI0 {
		return m.makeWordCoreABI0(t, method, name, form, swallowErr)
	}
	return m.makeWordCoreRegabi(t, method, name, form, swallowErr)
}

// detectWordShape classifies sig into its word-shape key, or ok=false if the
// target is not 64-bit little-endian, any param/result is unclassifiable, the
// words exceed the arch's budget, or no generated pool exists for the key (so an
// attach never errors on it). Drops are recorded for the MVM_WORDDROPS report.
func detectWordShape(sig reflect.Type) (key string, ok bool) {
	key, reason, ok := classifyWordSig(sig)
	switch {
	case !ok && reason != "":
		recordWordDrop(&wordDropUnsup, reason, sig)
		return "", false
	case !ok:
		return "", false
	case !stubs.HasWordShape(key):
		recordWordDrop(&wordDropPools, key, sig)
		return "", false
	}
	return key, true
}

// wordShapeKey returns sig's word-shape key when a generated pool exists,
// silently: reserve gates and typed-fallback probes must not pollute the
// MVM_WORDDROPS counts.
func wordShapeKey(sig reflect.Type) (string, bool) {
	key, _, ok := classifyWordSig(sig)
	if !ok || !stubs.HasWordShape(key) {
		return "", false
	}
	return key, true
}

// wordShapeAvailable reports whether sig has a word-shape with a generated pool.
func wordShapeAvailable(sig reflect.Type) bool {
	_, ok := wordShapeKey(sig)
	return ok
}

// setResultValue copies a method return into dst (a typed result slot), clearing
// the read-only flag and mirroring reflectToError's nil handling for interface
// targets: a nil concrete reference kind becomes a nil interface, not a boxed
// typed-nil.
func setResultValue(dst, v reflect.Value) {
	if !v.IsValid() {
		return
	}
	v = Exportable(v)
	dt := dst.Type()
	if dt.Kind() == reflect.Interface {
		switch v.Kind() {
		case reflect.Interface:
			if v.IsNil() {
				return
			}
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
			if v.IsNil() {
				return
			}
		}
		if v.Type().AssignableTo(dt) {
			dst.Set(v)
		}
		return
	}
	switch {
	case v.Type() == dt, v.Type().AssignableTo(dt):
		dst.Set(v)
	case v.Type().ConvertibleTo(dt):
		dst.Set(v.Convert(dt))
	}
}
