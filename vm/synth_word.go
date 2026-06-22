package vm

import (
	"reflect"
	"runtime"
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
// word, f = an FP-register word. The generated pools (stdlib/stubs/pool_w*.go)
// carry one stub family + one generic dispatcher per word-shape; makeWordCore
// supplies the per-method marshaling the dispatcher calls back into.
//
// A float64 leaf is an 'f' word, carried in an FP register (the stub/dispatcher
// types it float64 so Go's register allocator places it there). float32, complex,
// and arrays are still dropped (returns !ok), so detectWordShape falls through to
// "drop" -- never a misclassification. A struct flattens to one word per leaf
// field: each scalar/pointer field takes a whole register even when sub-word in
// memory (Go's register ABI), so fixed.Point26_6 (struct{X,Y int32}) is "ii".
// wordLayoutOf records each leaf's true byte offset and width, so packed fields
// and inter-field padding marshal back to the right place (not a uniform stride).
//
// The path requires a 64-bit little-endian target (see wordShapesSupported); on
// any other arch detectWordShape drops everything and only the typed shapes
// (arch-independent) attach, so dispatch is always correct, just less capable.

// forceWordShape, set only by benchmarks, makes attach prefer the word-class path
// over a matching typed shape, so the two dispatch mechanisms can be compared.
var forceWordShape bool

// SetForceWordShape toggles the benchmark-only word-path preference.
func SetForceWordShape(v bool) { forceWordShape = v }

// classifyType returns t's ABI register words as a string over {p, i, f}, or
// ok=false for a non-register-safe type (float32, complex, array, or a struct
// containing one). It is wordLayoutOf's classes string; see appendWordLeaves for
// the per-leaf rules. A scalar is one i word; a pointer-ish is p; float64 is f;
// string is (p,i); slice is (p,i,i); interface is (p,p); a struct is its leaves
// flattened (one word per leaf field, sub-word and padded fields included).
func classifyType(t reflect.Type) (classes string, ok bool) {
	lay, ok := wordLayoutOf(t)
	if !ok {
		return "", false
	}
	return lay.classes, true
}

// wordLayout is a type's flattened register-word layout: classes[k] is the k-th
// word's class (p/i/f) and offs[k]/sizes[k] are that word's leaf byte offset and
// meaningful byte width in the value's memory. The per-leaf offsets (not a
// uniform 8-byte stride) place sub-word-packed and padded struct fields correctly.
type wordLayout struct {
	classes string
	offs    []uintptr
	sizes   []uintptr
}

// wordLayoutOf computes t's register-word layout, or ok=false if t (or a field)
// is not register-safe. The classes string equals classifyType(t); offs/sizes
// drive writeWords/readWords. Computed once per method at attach, never per call.
func wordLayoutOf(t reflect.Type) (wordLayout, bool) {
	var lay wordLayout
	var b strings.Builder
	if !appendWordLeaves(t, 0, &b, &lay) {
		return wordLayout{}, false
	}
	lay.classes = b.String()
	return lay, true
}

// appendWordLeaves walks t at memory offset base, appending one entry per
// register word. A scalar/pointer leaf takes a whole register regardless of its
// byte size (Go's register ABI), so a struct flattens to one word per leaf field
// at that field's true offset and width -- inter-field padding and sub-word
// packing are fine, since each word reads/writes only its own bytes. float32,
// complex, and arrays have no leaf rule and drop the whole type.
//
// Example: time.Time -> "iip"; vg.Point{X,Y float64} -> "ff";
// fixed.Point26_6{X,Y int32} -> "ii" (two int words at offsets 0 and 4).
func appendWordLeaves(t reflect.Type, base uintptr, b *strings.Builder, lay *wordLayout) bool {
	push := func(class byte, off, size uintptr) {
		b.WriteByte(class)
		lay.offs = append(lay.offs, off)
		lay.sizes = append(lay.sizes, size)
	}
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		push('i', base, t.Size())
	case reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Map, reflect.Func:
		push('p', base, wordSize)
	case reflect.Float64:
		push('f', base, wordSize)
	case reflect.String:
		push('p', base, wordSize)          // data
		push('i', base+wordSize, wordSize) // len
	case reflect.Slice:
		push('p', base, wordSize)            // data
		push('i', base+wordSize, wordSize)   // len
		push('i', base+2*wordSize, wordSize) // cap
	case reflect.Interface:
		push('p', base, wordSize)          // type/itab
		push('p', base+wordSize, wordSize) // data
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Type.Size() == 0 {
				// A zero-size field (e.g. a [0]T marker like pragma.DoNotCompare
				// in protoreflect.Value) occupies no register and no memory; skip.
				continue
			}
			if !appendWordLeaves(f.Type, base+f.Offset, b, lay) {
				return false
			}
		}
	default:
		return false
	}
	return true
}

// argIntRegs / argFloatRegs are the running arch's integer and float
// argument-register counts under Go's internal register ABI. The word path
// flattens every aggregate to one register per word and assumes each word lands
// in a register; if a side's words exceed the budget, some word would straddle to
// the stack, where the flattened stub no longer matches the real signature -- so
// the shape is dropped, never mis-marshaled. Integer ('p'/'i') and float ('f')
// words use independent register files. Only arches whose counts are verified
// here admit floats and the larger shapes; any other 64-bit LE arch (already the
// only ones past wordShapesSupported) keeps the historical small integer shapes.
var argIntRegs, argFloatRegs = func() (int, int) {
	switch runtime.GOARCH {
	case "arm64":
		return 16, 16
	case "amd64":
		return 9, 15
	default:
		return 7, 0
	}
}()

// wordsFitRegs reports whether classes (one side's flat p/i/f word string) fits
// the arch's argument registers. The params side reserves one integer register
// for the receiver.
func wordsFitRegs(classes string, isParams bool) bool {
	ints, floats := 0, 0
	for i := range len(classes) {
		if classes[i] == 'f' {
			floats++
		} else {
			ints++
		}
	}
	intBudget := argIntRegs
	if isParams {
		intBudget-- // receiver consumes one integer register
	}
	return ints <= intBudget && floats <= argFloatRegs
}

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
	if !wordsFitRegs(string(params), true) || !wordsFitRegs(string(results), false) {
		return "", "over word budget", false
	}
	return string(params) + "_" + string(results), "", true
}

// detectWordShape classifies sig into its word-shape key, or ok=false if the
// target is not 64-bit little-endian, any param/result is unclassifiable, the
// words exceed the arch's argument registers (wordsFitRegs), or no generated pool
// exists for the key (so an attach never errors on it). Drops are recorded for
// the MVM_WORDDROPS report.
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

// sigWordLayouts precomputes the per-param and per-result word layouts once at
// attach, so per-call marshaling never re-walks the types (wordLayoutOf allocates).
func sigWordLayouts(sig reflect.Type) (in, out []wordLayout) {
	in = make([]wordLayout, sig.NumIn())
	for i := range in {
		in[i], _ = wordLayoutOf(sig.In(i))
	}
	out = make([]wordLayout, sig.NumOut())
	for j := range out {
		out[j], _ = wordLayoutOf(sig.Out(j))
	}
	return in, out
}

// makeWordCore builds the stubs.CoreFunc for one word-shaped method: it
// reconstructs the args from the scattered words, re-enters the interpreter,
// and writes the result words back. A failed dispatch panics (raiseMethodErr)
// unless swallowErr (see shapeSwallowsDispatchErr): then results stay zero.
func (m *Machine) makeWordCore(t *Type, method Method, name string, form recvForm, swallowErr bool) stubs.CoreFunc {
	methodSig := method.Rtype
	inLayouts, outLayouts := sigWordLayouts(methodSig)
	return func(recv unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, fw []float64, rpw []unsafe.Pointer, rsw []uint64, rfw []float64) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := marshalArgs(methodSig, inLayouts, pw, sw, fw)
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			if !swallowErr {
				raiseMethodErr(err)
			}
			out = nil // zero result words
		}
		marshalResults(methodSig, outLayouts, out, rpw, rsw, rfw)
	}
}

// marshalArgs reconstructs each native arg Value from the scattered register
// words, consumed left-to-right in signature order (matching the generated
// dispatcher's scatter). Each arg is built in a fresh reflect.New allocation
// (typed, so the GC scans its pointer words) before its words are written in.
func marshalArgs(sig reflect.Type, layouts []wordLayout, pw []unsafe.Pointer, sw []uint64, fw []float64) []reflect.Value {
	n := sig.NumIn()
	if n == 0 {
		return nil
	}
	argv := make([]reflect.Value, n)
	pi, si, fi := 0, 0, 0
	for i := range n {
		av := reflect.New(sig.In(i))
		pi, si, fi = writeWords(layouts[i], av.UnsafePointer(), pw, sw, fw, pi, si, fi)
		argv[i] = av.Elem()
	}
	return argv
}

// marshalResults writes each result Value's words back into rpw/rsw, walking the
// same flat class sequence the generated dispatcher gathers from. A single-word
// scalar or pointer result of the exact type is read straight off the value (no
// allocation); anything needing a typed buffer (conversion, composite, interface)
// falls back to a reflect.New scratch.
func marshalResults(sig reflect.Type, layouts []wordLayout, out []reflect.Value, rpw []unsafe.Pointer, rsw []uint64, rfw []float64) {
	pi, si, fi := 0, 0, 0
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
			case t.Kind() == reflect.Float64:
				rfw[fi] = out[j].Float()
				fi++
				continue
			}
		}
		tmp := reflect.New(t)
		if j < len(out) {
			setResultValue(tmp.Elem(), out[j])
		}
		pi, si, fi = readWords(layouts[j], tmp.UnsafePointer(), rpw, rsw, rfw, pi, si, fi)
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

// writeWords writes lay's words from pw/sw/fw into the value at dst (a *t
// allocation), each at its leaf offset, returning the advanced cursors. A pointer
// word is written through a pointer-typed slot so it stays GC-visible; an integer
// word writes only its leaf's meaningful low bytes (lay.sizes[k]) so a sub-word
// field does not overrun its neighbour or the allocation.
func writeWords(lay wordLayout, dst unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, fw []float64, pi, si, fi int) (int, int, int) {
	for k := range len(lay.classes) {
		off := lay.offs[k]
		switch lay.classes[k] {
		case 'p':
			*(*unsafe.Pointer)(unsafe.Add(dst, off)) = pw[pi]
			pi++
		case 'f':
			*(*float64)(unsafe.Add(dst, off)) = fw[fi]
			fi++
		default:
			writeIntWord(unsafe.Add(dst, off), sw[si], lay.sizes[k])
			si++
		}
	}
	return pi, si, fi
}

// readWords reads lay's words from the value at src (a *t allocation) into
// rpw/rsw/rfw, symmetric to writeWords.
func readWords(lay wordLayout, src unsafe.Pointer, rpw []unsafe.Pointer, rsw []uint64, rfw []float64, pi, si, fi int) (int, int, int) {
	for k := range len(lay.classes) {
		off := lay.offs[k]
		switch lay.classes[k] {
		case 'p':
			rpw[pi] = *(*unsafe.Pointer)(unsafe.Add(src, off))
			pi++
		case 'f':
			rfw[fi] = *(*float64)(unsafe.Add(src, off))
			fi++
		default:
			rsw[si] = readIntWord(unsafe.Add(src, off), lay.sizes[k])
			si++
		}
	}
	return pi, si, fi
}

const wordSize = unsafe.Sizeof(uintptr(0))

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
