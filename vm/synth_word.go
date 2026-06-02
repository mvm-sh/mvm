package vm

import (
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// Word-class synth dispatch (stage 1, non-struct).
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
// Stage 1 deliberately drops structs, arrays, and floats (classifyType returns
// !ok), so detectWordShape falls through to "drop" -- identical to the
// pre-fallback behavior, never a misclassification. Stage 2 widens classifyType
// to word-sized-leaf structs and floats.

// classifyType returns t's ABI register words as a string over {p, i}, or
// ok=false for a type stage 1 cannot prove register-safe (struct, array, float,
// complex). Every classifiable type has 8-byte-strided words: a sub-word scalar
// is a single i word; string is (p,i); slice is (p,i,i); interface is (p,p).
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
	}
	return "", false
}

// maxWordIO caps the register words on each side (params, results) of a
// word-shape. Conservatively below the amd64 integer-arg budget (9, the smaller
// of amd64/arm64): the receiver consumes one param register, so params + recv
// stays clear of the limit. A signature over the cap is dropped, not
// mis-marshaled.
const maxWordIO = 6

// detectWordShape classifies sig into its word-shape key ("params_results"), or
// ok=false if any param/result is unclassifiable, the words exceed maxWordIO, or
// no generated pool exists for the key (so an attach never errors on it).
func detectWordShape(sig reflect.Type) (key string, ok bool) {
	if sig == nil || sig.Kind() != reflect.Func {
		return "", false
	}
	var params, results []byte
	for i := range sig.NumIn() {
		c, ok := classifyType(sig.In(i))
		if !ok {
			return "", false
		}
		params = append(params, c...)
	}
	for j := range sig.NumOut() {
		c, ok := classifyType(sig.Out(j))
		if !ok {
			return "", false
		}
		results = append(results, c...)
	}
	if len(params) > maxWordIO || len(results) > maxWordIO {
		return "", false
	}
	key = string(params) + "_" + string(results)
	if !stubs.HasWordShape(key) {
		return "", false
	}
	return key, true
}

// makeWordCore builds the stubs.CoreFunc for one word-shaped method: it
// reconstructs the args from the scattered words, re-enters the interpreter, and
// writes the result words back. A failed dispatch panics (raiseMethodErr), so a
// panicking interpreted method propagates through the native caller as in Go.
func (m *Machine) makeWordCore(t *Type, method Method, name string, ptrRecv bool) stubs.CoreFunc {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, rpw []unsafe.Pointer, rsw []uint64) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := marshalArgs(methodSig, pw, sw)
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			raiseMethodErr(err)
		}
		marshalResults(methodSig, out, rpw, rsw)
	}
}

// marshalArgs reconstructs each native arg Value from the scattered register
// words, consumed left-to-right in signature order (matching the generated
// dispatcher's scatter). Each arg is built in a fresh reflect.New allocation
// (typed, so the GC scans its pointer words) before its words are written in.
func marshalArgs(sig reflect.Type, pw []unsafe.Pointer, sw []uint64) []reflect.Value {
	n := sig.NumIn()
	if n == 0 {
		return nil
	}
	argv := make([]reflect.Value, n)
	pi, si := 0, 0
	for i := range n {
		t := sig.In(i)
		av := reflect.New(t)
		pi, si = writeWords(t, av.UnsafePointer(), pw, sw, pi, si)
		argv[i] = av.Elem()
	}
	return argv
}

// marshalResults writes each result Value's words back into rpw/rsw, walking the
// same flat class sequence the generated dispatcher gathers from.
func marshalResults(sig reflect.Type, out []reflect.Value, rpw []unsafe.Pointer, rsw []uint64) {
	pi, si := 0, 0
	for j := range sig.NumOut() {
		t := sig.Out(j)
		tmp := reflect.New(t)
		if j < len(out) {
			setResultValue(tmp.Elem(), out[j])
		}
		pi, si = readWords(t, tmp.UnsafePointer(), rpw, rsw, pi, si)
	}
}

// writeWords writes t's words from pw/sw into the value at dst (a *t allocation),
// returning the advanced pointer/integer cursors. A pointer word is written
// through a pointer-typed slot so it stays GC-visible; an integer word writes
// only its meaningful low bytes (min(8, size-off)) so a sub-word scalar does not
// overrun its allocation.
func writeWords(t reflect.Type, dst unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, pi, si int) (int, int) {
	classes, _ := classifyType(t)
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
func readWords(t reflect.Type, src unsafe.Pointer, rpw []unsafe.Pointer, rsw []uint64, pi, si int) (int, int) {
	classes, _ := classifyType(t)
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
