package vm

import (
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// ABI0 word-class layout (used by the wasm target; see synth_word_wasm.go).
//
// Go's wasm target passes every parameter and result in contiguous Go-stack
// memory laid out exactly like a struct of those types (ABI0), not in registers.
// So a synth stub matches a real method when its parameter/result BYTES reproduce
// that layout. We classify each side into 8-byte SLOTS: unlike the register ABI
// (one register per leaf field), sub-word struct fields PACK into a shared slot
// here -- fixed.Point26_6 (struct{X,Y int32}) is one slot, color.Color.RGBA's four
// uint32 results are two slots. A slot is 'p' iff it is exactly a pointer at an
// 8-aligned offset, 'f' iff exactly a float64, else 'i' (raw bytes, possibly
// shared by several packed fields). A float64 slot keeps class 'f' so the float
// pools are shared with the register ABI; on a stack ABI 'f' and 'i' occupy the
// same 8-byte slot, so the distinction is harmless but lets keys match.
//
// This file is build-tag-neutral so the marshaling logic is unit-testable on any
// 64-bit little-endian host (the host's own 8-byte layout exercises it); it is
// wired into classifyWordSig/makeWordCore only on wasm.

// abi0Region is one side (params or results, receiver excluded) decomposed into
// 8-byte slots, with a per-item plan to move bytes between the slot words and each
// value.
type abi0Region struct {
	classes string     // per-slot class 'p'/'i'/'f', in slot order
	items   []abi0Item // one per param/result, in signature order
}

// abi0Item is one param/result and the copy ops that (un)marshal it.
type abi0Item struct {
	typ reflect.Type
	ops []abi0Op
}

// abi0Op moves one slot word to/from a byte range of its item value. For 'p'/'f'
// the whole word is one field at dstOff; for 'i' it carries the sub-range
// [srcByte, srcByte+n) of the word so packed fields land at the right dst offset.
type abi0Op struct {
	class   byte
	slot    int     // index among slots of this class (pw/sw/fw index)
	dstOff  uintptr // byte offset within the item value
	srcByte uintptr // byte offset within the slot word ('i' only)
	n       uintptr // bytes to copy ('i' only)
}

// abi0Leaf is a primitive leaf collected during region layout.
type abi0Leaf struct {
	class byte
	off   uintptr // region-relative byte offset
}

// abi0Align rounds off up to a (a power of two).
func abi0Align(off, a uintptr) uintptr {
	if a <= 1 {
		return off
	}
	return (off + a - 1) &^ (a - 1)
}

// abi0Leaves appends t's primitive leaves at region offset base. On a stack ABI a
// value is passed as its exact memory bytes, so every type is classifiable: only
// pointer leaves (kept GC-visible via a 'p' slot) and float64 leaves (an 'f' slot,
// for key parity with the register ABI) need a non-default class; everything else
// is raw 'i' bytes. So unlike the register classifier (appendWordLeaves, which
// drops float32/complex/arrays), this accepts them as sub-word/packed byte ranges.
// ok=false is unreachable for a real type, kept only as a defensive default.
func abi0Leaves(t reflect.Type, base uintptr, out *[]abi0Leaf) bool {
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		*out = append(*out, abi0Leaf{'i', base})
	case reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Map, reflect.Func:
		*out = append(*out, abi0Leaf{'p', base})
	case reflect.Float64:
		*out = append(*out, abi0Leaf{'f', base})
	case reflect.Float32:
		*out = append(*out, abi0Leaf{'i', base}) // sub-word: raw 4 bytes, not an FP slot
	case reflect.Complex128:
		// Two float64 halves -> 'f','f', matching the register ABI's "ff" key.
		*out = append(*out, abi0Leaf{'f', base}, abi0Leaf{'f', base + wordSize})
	case reflect.Complex64:
		*out = append(*out, abi0Leaf{'i', base}) // two float32 halves packed in one slot
	case reflect.Array:
		el := t.Elem()
		for i := range t.Len() {
			if !abi0Leaves(el, base+uintptr(i)*el.Size(), out) {
				return false
			}
		}
	case reflect.String:
		*out = append(*out, abi0Leaf{'p', base}, abi0Leaf{'i', base + wordSize})
	case reflect.Slice:
		*out = append(*out, abi0Leaf{'p', base}, abi0Leaf{'i', base + wordSize}, abi0Leaf{'i', base + 2*wordSize})
	case reflect.Interface:
		*out = append(*out, abi0Leaf{'p', base}, abi0Leaf{'p', base + wordSize})
	case reflect.Struct:
		for f := range t.Fields() {
			if f.Type.Size() == 0 {
				continue // zero-size field occupies no slot
			}
			if !abi0Leaves(f.Type, base+f.Offset, out) {
				return false
			}
		}
	default:
		return false
	}
	return true
}

// classifyABI0Region decomposes the ABI0 layout of types (one side: params or
// results) into 8-byte slots and a per-item copy plan. ok=false only if a type is
// unclassifiable. The region size is rounded UP to a multiple of 8: Go's ABI0
// pads each side to a pointer-word boundary (a side's results start 8-aligned
// after the params, and the frame is 8-padded), so a sub-word tail (a lone bool
// result, a trailing int32) sits in a full slot whose high bytes are frame
// padding -- the leaf ops touch only the real bytes, so reading/writing the whole
// slot stays within the caller's frame.
func classifyABI0Region(types []reflect.Type) (abi0Region, bool) {
	var leaves []abi0Leaf
	starts := make([]uintptr, len(types))
	off := uintptr(0)
	for i, t := range types {
		off = abi0Align(off, uintptr(t.Align()))
		starts[i] = off
		if t.Size() == 0 {
			continue
		}
		if !abi0Leaves(t, off, &leaves) {
			return abi0Region{}, false
		}
		off += t.Size()
	}
	nslots := int((off + wordSize - 1) / wordSize)
	classes := make([]byte, nslots)
	for i := range classes {
		classes[i] = 'i'
	}
	for _, lf := range leaves {
		switch lf.class {
		case 'p':
			classes[lf.off/wordSize] = 'p'
		case 'f':
			classes[lf.off/wordSize] = 'f'
		}
	}
	classIdx := make([]int, nslots)
	var np, ni, nf int
	for k := range classes {
		switch classes[k] {
		case 'p':
			classIdx[k] = np
			np++
		case 'f':
			classIdx[k] = nf
			nf++
		default:
			classIdx[k] = ni
			ni++
		}
	}
	items := make([]abi0Item, len(types))
	for i, t := range types {
		items[i].typ = t
		if t.Size() == 0 {
			continue
		}
		istart := starts[i]
		iend := istart + t.Size()
		for k := int(istart / wordSize); k <= int((iend-1)/wordSize); k++ {
			slotStart := uintptr(k) * wordSize
			switch classes[k] {
			case 'p', 'f':
				items[i].ops = append(items[i].ops, abi0Op{
					class:  classes[k],
					slot:   classIdx[k],
					dstOff: slotStart - istart,
				})
			default: // 'i'
				lo := maxU(slotStart, istart)
				hi := minU(slotStart+wordSize, iend)
				items[i].ops = append(items[i].ops, abi0Op{
					class:   'i',
					slot:    classIdx[k],
					dstOff:  lo - istart,
					srcByte: lo - slotStart,
					n:       hi - lo,
				})
			}
		}
	}
	return abi0Region{classes: string(classes), items: items}, true
}

// abi0ClassifyWordSig classifies sig into its ABI0 word-shape key
// ("params_results"), mirroring classifyWordSig's contract for the wasm build.
func abi0ClassifyWordSig(sig reflect.Type) (key, reason string, ok bool) {
	if !wordShapesSupported || sig == nil || sig.Kind() != reflect.Func {
		return "", "", false
	}
	pr, ok := classifyABI0Region(typeList(sig.NumIn(), sig.In))
	if !ok {
		return "", "unclassifiable param/result", false
	}
	rr, ok := classifyABI0Region(typeList(sig.NumOut(), sig.Out))
	if !ok {
		return "", "unclassifiable param/result", false
	}
	return pr.classes + "_" + rr.classes, "", true
}

// typeList materializes n types from an indexed accessor (sig.In / sig.Out).
func typeList(n int, at func(int) reflect.Type) []reflect.Type {
	ts := make([]reflect.Type, n)
	for i := range ts {
		ts[i] = at(i)
	}
	return ts
}

// makeWordCoreABI0 builds the stubs.CoreFunc for one word-shaped method on a
// stack ABI: it reconstructs the args from the slot words, re-enters the
// interpreter, and writes the result words back. A failed dispatch panics
// (raiseMethodErr) unless swallowErr: then results stay zero. makeWordCore
// selects it per arch.
func (m *Machine) makeWordCoreABI0(t *Type, method Method, name string, form recvForm, swallowErr bool) stubs.CoreFunc {
	methodSig := method.Rtype
	inRegion, _ := classifyABI0Region(typeList(methodSig.NumIn(), methodSig.In))
	outRegion, _ := classifyABI0Region(typeList(methodSig.NumOut(), methodSig.Out))
	return func(recv unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, fw []float64, rpw []unsafe.Pointer, rsw []uint64, rfw []float64) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := abi0MarshalArgs(inRegion, pw, sw, fw)
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			if !swallowErr {
				raiseMethodErr(err)
			}
			out = nil // zero result words
		}
		abi0MarshalResults(outRegion, out, rpw, rsw, rfw)
	}
}

// abi0MarshalArgs reconstructs each native arg Value from the slot words. A 'p'
// slot is written through a pointer-typed slot so it stays GC-visible; an 'i' slot
// copies its meaningful bytes (packed fields share a word). Each arg is a fresh
// reflect.New allocation, so pointer words land in GC-scanned memory.
func abi0MarshalArgs(r abi0Region, pw []unsafe.Pointer, sw []uint64, fw []float64) []reflect.Value {
	if len(r.items) == 0 {
		return nil
	}
	argv := make([]reflect.Value, len(r.items))
	for i := range r.items {
		it := &r.items[i]
		av := reflect.New(it.typ)
		dst := av.UnsafePointer()
		for _, op := range it.ops {
			switch op.class {
			case 'p':
				*(*unsafe.Pointer)(unsafe.Add(dst, op.dstOff)) = pw[op.slot]
			case 'f':
				*(*float64)(unsafe.Add(dst, op.dstOff)) = fw[op.slot]
			default:
				writeIntWordBytes(unsafe.Add(dst, op.dstOff), sw[op.slot], op.srcByte, op.n)
			}
		}
		argv[i] = av.Elem()
	}
	return argv
}

// abi0MarshalResults writes each result Value's bytes back into rpw/rsw/rfw,
// symmetric to abi0MarshalArgs. Packed results sharing an 'i' slot accumulate into
// the same word (readIntWordBytes preserves the word's other bytes).
func abi0MarshalResults(r abi0Region, out []reflect.Value, rpw []unsafe.Pointer, rsw []uint64, rfw []float64) {
	for j := range r.items {
		it := &r.items[j]
		tmp := reflect.New(it.typ)
		if j < len(out) {
			setResultValue(tmp.Elem(), out[j])
		}
		src := tmp.UnsafePointer()
		for _, op := range it.ops {
			switch op.class {
			case 'p':
				rpw[op.slot] = *(*unsafe.Pointer)(unsafe.Add(src, op.dstOff))
			case 'f':
				rfw[op.slot] = *(*float64)(unsafe.Add(src, op.dstOff))
			default:
				rsw[op.slot] = readIntWordBytes(rsw[op.slot], unsafe.Add(src, op.dstOff), op.srcByte, op.n)
			}
		}
	}
}

// writeIntWordBytes copies n bytes of word w (from byte srcByte within w) to dst
// (little-endian).
func writeIntWordBytes(dst unsafe.Pointer, w uint64, srcByte, n uintptr) {
	for i := range n {
		*(*byte)(unsafe.Add(dst, i)) = byte(w >> (8 * (srcByte + i)))
	}
}

// readIntWordBytes returns w with bytes [srcByte, srcByte+n) replaced by the n
// bytes at src; w's other bytes are preserved so packed fields sharing a slot
// accumulate into one word (little-endian).
func readIntWordBytes(w uint64, src unsafe.Pointer, srcByte, n uintptr) uint64 {
	for i := range n {
		shift := 8 * (srcByte + i)
		w &^= uint64(0xff) << shift
		w |= uint64(*(*byte)(unsafe.Add(src, i))) << shift
	}
	return w
}

func minU(a, b uintptr) uintptr {
	if a < b {
		return a
	}
	return b
}

func maxU(a, b uintptr) uintptr {
	if a > b {
		return a
	}
	return b
}
