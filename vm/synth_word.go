package vm

import (
	"reflect"
	"strings"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// Word-class synth dispatch.
//
// A synth method-table stub must have a real text-segment PC whose ABI exactly
// matches the method signature. The ABI is set by the register-word
// classification of the params and results, not their exact Go types, so many
// distinct signatures share one stub family. classifyType maps a type to its
// ABI words: p = a pointer-containing register word, i = an integer register
// word. The generated pools (stdlib/stubs/pool_w*.go) carry one stub family +
// one generic dispatcher per word-shape; makeWordCore supplies the per-method
// marshaling the dispatcher calls back into.
//
// classifyType drops floats and arrays (returns !ok), so detectWordShape falls
// through to "drop" -- identical to the typed-shape behavior, never a
// misclassification. Structs are flattened when every leaf is exactly one
// register word (classifyStruct); a sub-word or float field drops the type.
//
// The path requires a 64-bit little-endian target (see wordShapesSupported); on
// any other arch detectWordShape drops everything and only the typed shapes
// (arch-independent) attach, so dispatch is always correct, just less capable.

// forceWordShape, set only by benchmarks, makes attach prefer the word-class path
// over a matching typed shape, so the two dispatch mechanisms can be compared.
var forceWordShape bool

// SetForceWordShape toggles the benchmark-only word-path preference.
func SetForceWordShape(v bool) { forceWordShape = v }

// classifyType returns t's ABI register words as a string over {p, i}, or
// ok=false for a type that is not register-safe (float, complex, array, or a
// struct with a sub-word/float leaf). Every classifiable type has 8-byte-strided
// words: a top-level sub-word scalar is a single i word; string is (p,i); slice
// is (p,i,i); interface is (p,p); a struct is its leaves flattened.
func classifyType(t reflect.Type) (classes string, ok bool) {
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		return "i", true
	case reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Map, reflect.Func:
		return "p", true
	case reflect.String:
		return "pi", true
	case reflect.Slice:
		return "pii", true
	case reflect.Interface:
		return "pp", true
	case reflect.Struct:
		return classifyStruct(t)
	}
	return "", false
}

// classifyStruct flattens a struct to its register words, accepting it only when
// every field starts on a word boundary and occupies a whole number of register
// words (so each leaf scalar is exactly one word). Under that condition the
// register-word sequence equals the memory layout, which is what lets
// writeWords/readWords reconstruct the value from words at k*wordSize. A sub-word
// leaf (e.g. struct{a, b uint32}), a float field, an array, or trailing padding
// fails the invariant and drops the type (= today's behavior, no regression).
//
// Example: time.Time{wall uint64; ext int64; loc *Location} -> "iip".
func classifyStruct(t reflect.Type) (string, bool) {
	var b strings.Builder
	expect := uintptr(0)
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Offset != expect {
			return "", false // padding or sub-word packing
		}
		c, ok := classifyType(f.Type)
		if !ok || uintptr(len(c))*wordSize != f.Type.Size() {
			return "", false // unclassifiable, or a sub-word leaf (size < its words)
		}
		b.WriteString(c)
		expect += uintptr(len(c)) * wordSize
	}
	if expect != t.Size() {
		return "", false // trailing padding
	}
	return b.String(), true
}

// maxWordIO caps the register words on each side (params, results) of a
// word-shape. Conservatively below the amd64 integer-arg budget (9, the smaller
// of amd64/arm64): the receiver consumes one param register, so params + recv
// stays clear of the limit. A signature over the cap is dropped, not
// mis-marshaled.
const maxWordIO = 6

// wordShapesSupported gates the whole word-class path to a 64-bit little-endian
// target: the classifier treats each scalar/pointer as one 8-byte register word
// (wrong for multi-register int64/uint64 on 32-bit) and writeIntWord/readIntWord
// pack bytes low-first (wrong on big-endian). The 8-byte check is a compile-time
// constant, the endian check a one-time init probe, so this is true on amd64,
// arm64, riscv64, ppc64le, etc. and false elsewhere. When false, detectWordShape
// drops every method to the word path -- identical to the pre-word-class
// behavior (the typed shapes stay arch-independent and keep working).
var wordShapesSupported = unsafe.Sizeof(uintptr(0)) == 8 && nativeIsLittleEndian()

func nativeIsLittleEndian() bool {
	x := uint16(1)
	return *(*byte)(unsafe.Pointer(&x)) == 1
}

// classifyWordSig classifies sig into its word-shape key ("params_results"),
// or the drop reason and ok=false. It never records telemetry and never
// consults the pool registry.
func classifyWordSig(sig reflect.Type) (key, reason string, ok bool) {
	if !wordShapesSupported || sig == nil || sig.Kind() != reflect.Func {
		return "", "", false
	}
	var params, results []byte
	for i := range sig.NumIn() {
		c, ok := classifyType(sig.In(i))
		if !ok {
			return "", "unclassifiable param/result", false
		}
		params = append(params, c...)
	}
	for j := range sig.NumOut() {
		c, ok := classifyType(sig.Out(j))
		if !ok {
			return "", "unclassifiable param/result", false
		}
		results = append(results, c...)
	}
	if len(params) > maxWordIO || len(results) > maxWordIO {
		return "", "over word budget", false
	}
	return string(params) + "_" + string(results), "", true
}

// detectWordShape classifies sig into its word-shape key, or ok=false if the
// target is not 64-bit little-endian, any param/result is unclassifiable, the
// words exceed maxWordIO, or no generated pool exists for the key (so an attach
// never errors on it). Drops are recorded for the MVM_WORDDROPS report.
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

// sigWordClasses precomputes the per-param and per-result class strings once at
// attach, so per-call marshaling never re-classifies (classifyStruct allocates).
func sigWordClasses(sig reflect.Type) (in, out []string) {
	in = make([]string, sig.NumIn())
	for i := range in {
		in[i], _ = classifyType(sig.In(i))
	}
	out = make([]string, sig.NumOut())
	for j := range out {
		out[j], _ = classifyType(sig.Out(j))
	}
	return in, out
}

// makeWordCore builds the stubs.CoreFunc for one word-shaped method: it
// reconstructs the args from the scattered words, re-enters the interpreter,
// and writes the result words back. A failed dispatch panics (raiseMethodErr)
// unless swallowErr (see shapeSwallowsDispatchErr): then results stay zero.
func (m *Machine) makeWordCore(t *Type, method Method, name string, form recvForm, swallowErr bool) stubs.CoreFunc {
	methodSig := method.Rtype
	inClasses, outClasses := sigWordClasses(methodSig)
	return func(recv unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, rpw []unsafe.Pointer, rsw []uint64) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := marshalArgs(methodSig, inClasses, pw, sw)
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			if !swallowErr {
				raiseMethodErr(err)
			}
			out = nil // zero result words
		}
		marshalResults(methodSig, outClasses, out, rpw, rsw)
	}
}

// marshalArgs reconstructs each native arg Value from the scattered register
// words, consumed left-to-right in signature order (matching the generated
// dispatcher's scatter). Each arg is built in a fresh reflect.New allocation
// (typed, so the GC scans its pointer words) before its words are written in.
func marshalArgs(sig reflect.Type, classes []string, pw []unsafe.Pointer, sw []uint64) []reflect.Value {
	n := sig.NumIn()
	if n == 0 {
		return nil
	}
	argv := make([]reflect.Value, n)
	pi, si := 0, 0
	for i := range n {
		t := sig.In(i)
		av := reflect.New(t)
		pi, si = writeWords(t, classes[i], av.UnsafePointer(), pw, sw, pi, si)
		argv[i] = av.Elem()
	}
	return argv
}

// marshalResults writes each result Value's words back into rpw/rsw, walking the
// same flat class sequence the generated dispatcher gathers from. A single-word
// scalar or pointer result of the exact type is read straight off the value (no
// allocation); anything needing a typed buffer (conversion, composite, interface)
// falls back to a reflect.New scratch.
func marshalResults(sig reflect.Type, classes []string, out []reflect.Value, rpw []unsafe.Pointer, rsw []uint64) {
	pi, si := 0, 0
	for j := range sig.NumOut() {
		t := sig.Out(j)
		if j < len(out) && out[j].IsValid() && out[j].Type() == t {
			switch {
			case isScalarWordKind(t.Kind()):
				rsw[si] = scalarWord(out[j])
				si++
				continue
			case isPtrWordKind(t.Kind()):
				rpw[pi] = unsafe.Pointer(out[j].Pointer())
				pi++
				continue
			}
		}
		tmp := reflect.New(t)
		if j < len(out) {
			setResultValue(tmp.Elem(), out[j])
		}
		pi, si = readWords(t, classes[j], tmp.UnsafePointer(), rpw, rsw, pi, si)
	}
}

// isScalarWordKind reports a kind that classifies to a single integer word.
func isScalarWordKind(k reflect.Kind) bool {
	switch k {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		return true
	}
	return false
}

// isPtrWordKind reports a kind that classifies to a single pointer word and whose
// word equals reflect.Value.Pointer (excludes Func, whose Pointer is the code
// entry, not the closure word).
func isPtrWordKind(k reflect.Kind) bool {
	switch k {
	case reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Map:
		return true
	}
	return false
}

// scalarWord returns v's bits as the integer word (the native side reads only the
// meaningful low bytes, so the high bits are don't-care).
func scalarWord(v reflect.Value) uint64 {
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return 1
		}
		return 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(v.Int())
	default:
		return v.Uint()
	}
}

// writeWords writes t's words from pw/sw into the value at dst (a *t allocation),
// returning the advanced pointer/integer cursors. A pointer word is written
// through a pointer-typed slot so it stays GC-visible; an integer word writes
// only its meaningful low bytes (min(8, size-off)) so a sub-word scalar does not
// overrun its allocation.
func writeWords(t reflect.Type, classes string, dst unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, pi, si int) (int, int) {
	size := t.Size()
	for k := range len(classes) {
		off := uintptr(k) * wordSize
		if classes[k] == 'p' {
			*(*unsafe.Pointer)(unsafe.Add(dst, off)) = pw[pi]
			pi++
			continue
		}
		writeIntWord(unsafe.Add(dst, off), sw[si], wordBytes(size, off))
		si++
	}
	return pi, si
}

// readWords reads t's words from the value at src (a *t allocation) into rpw/rsw,
// symmetric to writeWords.
func readWords(t reflect.Type, classes string, src unsafe.Pointer, rpw []unsafe.Pointer, rsw []uint64, pi, si int) (int, int) {
	size := t.Size()
	for k := range len(classes) {
		off := uintptr(k) * wordSize
		if classes[k] == 'p' {
			rpw[pi] = *(*unsafe.Pointer)(unsafe.Add(src, off))
			pi++
			continue
		}
		rsw[si] = readIntWord(unsafe.Add(src, off), wordBytes(size, off))
		si++
	}
	return pi, si
}

const wordSize = unsafe.Sizeof(uintptr(0))

// wordBytes is the meaningful byte count of the integer word at off in a value
// of the given size (a full word, or the remaining bytes of a sub-word scalar).
func wordBytes(size, off uintptr) uintptr {
	if n := size - off; n < wordSize {
		return n
	}
	return wordSize
}

// writeIntWord copies the low n bytes of w into dst (little-endian; amd64/arm64).
func writeIntWord(dst unsafe.Pointer, w uint64, n uintptr) {
	for i := range n {
		*(*byte)(unsafe.Add(dst, i)) = byte(w >> (8 * i))
	}
}

// readIntWord reads n bytes at src into a zero-extended uint64 (little-endian).
func readIntWord(src unsafe.Pointer, n uintptr) uint64 {
	var w uint64
	for i := range n {
		w |= uint64(*(*byte)(unsafe.Add(src, i))) << (8 * i)
	}
	return w
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
