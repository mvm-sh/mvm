package vm

import (
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/internal/stubs"
	"github.com/mvm-sh/mvm/internal/wordabi"
	"github.com/mvm-sh/mvm/mtype"
)

// Word-class synth dispatch (vm seam).
//
// A synth method-table stub must have a real text-segment PC whose ABI matches
// the method signature; many signatures share one stub family by word-shape. The
// classification and per-call marshaling live in wordabi (arch-specific, pure);
// this file holds the Machine-coupled glue: the per-method core the generated
// dispatcher calls back into (makeWordCore re-enters the interpreter), and the
// detect/probe seams that gate a key against the generated stub pools and record
// MVM_WORDDROPS telemetry.

// forceWordShape, set only by benchmarks, makes attach prefer the word-class path
// over a matching typed shape, so the two dispatch mechanisms can be compared.
var forceWordShape bool

// SetForceWordShape toggles the benchmark-only word-path preference.
func SetForceWordShape(v bool) { forceWordShape = v }

// makeWordCore builds the per-method marshaler the generated dispatcher calls
// back into, selecting the register-ABI or ABI0 implementation per arch.
func (m *Machine) makeWordCore(t *mtype.Type, method mtype.Method, name string, form recvForm, swallowErr bool) stubs.CoreFunc {
	if wordabi.WordABI0 {
		return m.makeWordCoreABI0(t, method, name, form, swallowErr)
	}
	return m.makeWordCoreRegabi(t, method, name, form, swallowErr)
}

// makeWordCoreRegabi builds the stubs.CoreFunc for one word-shaped method: it
// reconstructs the args from the scattered register words, re-enters the
// interpreter, and writes the result words back. A failed dispatch panics
// (raiseMethodErr) unless swallowErr: then results stay zero.
func (m *Machine) makeWordCoreRegabi(t *mtype.Type, method mtype.Method, name string, form recvForm, swallowErr bool) stubs.CoreFunc {
	methodSig := method.Rtype
	inLayouts, outLayouts := wordabi.SigWordLayouts(methodSig)
	return func(recv unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, fw []float64, rpw []unsafe.Pointer, rsw []uint64, rfw []float64) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := wordabi.MarshalArgs(methodSig, inLayouts, pw, sw, fw)
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			if !swallowErr {
				raiseMethodErr(err)
			}
			out = nil // zero result words
		}
		wordabi.MarshalResults(methodSig, outLayouts, out, rpw, rsw, rfw)
	}
}

// makeWordCoreABI0 builds the stubs.CoreFunc for one word-shaped method on a stack
// ABI: it reconstructs the args from the slot words, re-enters the interpreter,
// and writes the result words back. Symmetric to makeWordCoreRegabi.
func (m *Machine) makeWordCoreABI0(t *mtype.Type, method mtype.Method, name string, form recvForm, swallowErr bool) stubs.CoreFunc {
	methodSig := method.Rtype
	inRegion, outRegion := wordabi.ABI0Regions(methodSig)
	return func(recv unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, fw []float64, rpw []unsafe.Pointer, rsw []uint64, rfw []float64) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := wordabi.ABI0MarshalArgs(inRegion, pw, sw, fw)
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			if !swallowErr {
				raiseMethodErr(err)
			}
			out = nil // zero result words
		}
		wordabi.ABI0MarshalResults(outRegion, out, rpw, rsw, rfw)
	}
}

// detectWordShape classifies sig into its word-shape key, or ok=false if the
// target is not 64-bit little-endian, any param/result is unclassifiable, the
// words exceed the arch's budget, or no generated pool exists for the key (so an
// attach never errors on it). Drops are recorded for the MVM_WORDDROPS report.
func detectWordShape(sig reflect.Type) (key string, ok bool) {
	key, reason, ok := wordabi.ClassifyWordSig(sig)
	switch {
	case !ok && reason != "":
		wordabi.RecordUnsupDrop(reason, sig)
		return "", false
	case !ok:
		return "", false
	case !stubs.HasWordShape(key):
		wordabi.RecordPoolDrop(key, sig)
		return "", false
	}
	return key, true
}

// WordShapeDropReport summarizes the word-shapes detectWordShape dropped this
// process (MVM_WORDDROPS; see ADR-022), or "" when unset or nothing dropped.
func WordShapeDropReport() string { return wordabi.DropReport() }

// wordShapeKey returns sig's word-shape key when a generated pool exists,
// silently: reserve gates and typed-fallback probes must not pollute the
// MVM_WORDDROPS counts.
func wordShapeKey(sig reflect.Type) (string, bool) {
	key, _, ok := wordabi.ClassifyWordSig(sig)
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
