// Package wordabi classifies a method signature into the ABI "words" that select
// a synth dispatch-stub pool, and marshals a call's args/results between those
// words and native reflect.Values.
//
// A synth method-table stub must have a real text-segment PC whose ABI exactly
// matches the method signature. The ABI is set by the register-word (or, on a
// stack ABI, the stack-slot) classification of the params and results, not their
// exact Go types, so many distinct signatures share one stub family. ClassifyWordSig
// yields a signature's word-shape key ("params_results"); the SigWordLayouts/
// MarshalArgs/MarshalResults (register ABI) and ABI0Regions/ABI0MarshalArgs/
// ABI0MarshalResults (stack ABI) pairs move bytes for one call.
//
// The classification and marshaling are arch-specific: register targets flatten
// each aggregate to one word per leaf field (regabi.go), while the wasm target
// packs sub-word fields into 8-byte stack slots (abi0.go). WordABI0 picks one; the
// other branch is eliminated at compile time. Both paths are compiled on every
// arch so the classifier stays unit-testable on any host.
//
// The path requires a 64-bit little-endian target (see WordShapesSupported); on
// any other arch the caller drops every method to the word path and only the typed
// shapes (arch-independent) attach, so dispatch is always correct, just less capable.
package wordabi

import (
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/internal/runtype"
)

const wordSize = unsafe.Sizeof(uintptr(0))

// WordShapesSupported gates the whole word-class path to a 64-bit little-endian
// target: the classifier treats each scalar/pointer as one 8-byte word (wrong for
// multi-register int64/uint64 on 32-bit) and the byte ops pack low-first (wrong on
// big-endian). The 8-byte check is a compile-time constant, the endian check a
// one-time init probe, so this is true on amd64, arm64, riscv64, ppc64le, wasm,
// etc. and false elsewhere. When false, ClassifyWordSig drops every method to the
// word path -- identical to the pre-word-class behavior (the typed shapes stay
// arch-independent and keep working).
var WordShapesSupported = unsafe.Sizeof(uintptr(0)) == 8 && nativeIsLittleEndian()

func nativeIsLittleEndian() bool {
	x := uint16(1)
	return *(*byte)(unsafe.Pointer(&x)) == 1
}

// ClassifyWordSig classifies sig into its word-shape key ("params_results"), or
// the drop reason and ok=false. The decomposition is arch-specific: register
// targets flatten each leaf to its own word (regabiClassifyWordSig), the wasm
// stack ABI packs sub-word fields into 8-byte slots (abi0ClassifyWordSig). The
// WordABI0 const picks one; the other branch is eliminated at compile time.
func ClassifyWordSig(sig reflect.Type) (key, reason string, ok bool) {
	if WordABI0 {
		return abi0ClassifyWordSig(sig)
	}
	return regabiClassifyWordSig(sig)
}

// setResultValue copies a method return into dst (a typed result slot), clearing
// the read-only flag and mirroring reflectToError's nil handling for interface
// targets: a nil concrete reference kind becomes a nil interface, not a boxed
// typed-nil.
func setResultValue(dst, v reflect.Value) {
	if !v.IsValid() {
		return
	}
	v = runtype.Exportable(v)
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
