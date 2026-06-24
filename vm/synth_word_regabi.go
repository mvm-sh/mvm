package vm

import (
	"reflect"
	"runtime"
	"strings"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// Register-ABI word-class layout (amd64, arm64, and the other regabi targets).
// Compiled on every arch but reached only when wordABI0 is false (the dead
// branch is eliminated); this keeps the classifier unit-testable on any host.
//
// classifyType maps a type to its ABI words: p = a pointer-containing register
// word, i = an integer register word, f = an FP-register word. A struct flattens
// to one word per leaf field: each scalar/pointer field takes a whole register
// even when sub-word in memory (Go's register ABI), so fixed.Point26_6
// (struct{X,Y int32}) is "ii". wordLayoutOf records each leaf's true byte offset
// and width, so packed fields and inter-field padding marshal back to the right
// place. The wasm target uses a different, stack-based decomposition; see
// synth_word_abi0.go.

// classifyType returns t's ABI register words as a string over {p, i, f, g}, or
// ok=false for a non-register-safe type (an array of length > 1, or a struct
// containing one). It is wordLayoutOf's classes string; see appendWordLeaves for
// the per-leaf rules. A scalar is one i word; a pointer-ish is p; float64 is f;
// float32 is g (a single-precision FP-register word, a distinct stub from f);
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
// word's class (p/i/f/g) and offs[k]/sizes[k] are that word's leaf byte offset and
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
// packing are fine, since each word reads/writes only its own bytes. float64 is
// one FP register ("f") and complex128 two ("ff"); float32 is one single-precision
// FP register ("g") and complex64 two ("gg") -- 'g' is a distinct class because a
// float32 stub param decodes its FP register differently than a float64 one.
// Arrays of length > 1 have no leaf rule and drop the whole type (they are
// stack-passed). The wasm path (synth_word_abi0.go) carries every type as stack
// bytes instead.
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
	case reflect.Complex128:
		push('f', base, wordSize)          // real
		push('f', base+wordSize, wordSize) // imag
	case reflect.Float32:
		push('g', base, 4) // single-precision FP-register word
	case reflect.Complex64:
		push('g', base, 4)   // real
		push('g', base+4, 4) // imag
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
		for f := range t.Fields() {
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

// wordsFitRegs reports whether classes (one side's flat p/i/f/g word string) fits
// the arch's argument registers. The params side reserves one integer register
// for the receiver.
func wordsFitRegs(classes string, isParams bool) bool {
	ints, floats := 0, 0
	for i := range len(classes) {
		if classes[i] == 'f' || classes[i] == 'g' {
			floats++ // float32 ('g') and float64 ('f') both consume an FP register
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

// regabiClassifyWordSig classifies sig into its register-ABI word-shape key
// ("params_results"), or the drop reason and ok=false. It never records telemetry
// and never consults the pool registry. classifyWordSig selects it per arch.
func regabiClassifyWordSig(sig reflect.Type) (key, reason string, ok bool) {
	if !wordShapesSupported || sig == nil || sig.Kind() != reflect.Func {
		return "", "", false
	}
	var params, results []byte
	for in := range sig.Ins() {
		c, ok := classifyType(in)
		if !ok {
			return "", "unclassifiable param/result", false
		}
		params = append(params, c...)
	}
	for out := range sig.Outs() {
		c, ok := classifyType(out)
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

// makeWordCoreRegabi builds the stubs.CoreFunc for one word-shaped method: it
// reconstructs the args from the scattered register words, re-enters the
// interpreter, and writes the result words back. A failed dispatch panics
// (raiseMethodErr) unless swallowErr: then results stay zero. makeWordCore
// selects it per arch.
func (m *Machine) makeWordCoreRegabi(t *Type, method Method, name string, form recvForm, swallowErr bool) stubs.CoreFunc {
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
			case t.Kind() == reflect.Float32:
				rfw[fi] = out[j].Float() // already the float32 value widened to float64
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
		case 'g':
			*(*float32)(unsafe.Add(dst, off)) = float32(fw[fi]) // narrow back to single
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
		case 'g':
			rfw[fi] = float64(*(*float32)(unsafe.Add(src, off))) // widen single to fw slot
			fi++
		default:
			rsw[si] = readIntWord(unsafe.Add(src, off), lay.sizes[k])
			si++
		}
	}
	return pi, si, fi
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
