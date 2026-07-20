package runtype

import (
	"reflect"
	"unsafe"
)

// Reflect's Type.Method/MethodByName store the method's textOff-resolved code
// PC in a heap-allocated unsafe.Pointer cell.
// Native code PCs live outside the GC arena, but wasm code PCs
// are plain integers >= 0x1000_0000 that alias heap addresses once the arena
// has grown that large, and the write barrier plus heap scans then abort the
// process ("found bad pointer in Go heap" / "found pointer to free object").
// TypeMethodByName and friends are drop-in lookups that on wasm rebuild the
// Method from the abi tables with the PC in an unscanned uintptr cell.
// On native they defer to stock reflect.

// The abi walk is wired in only by method_lookup_wasm.go; natively it is
// compiled for the regression tests comparing it against stock reflect.
// Anchor the roots so the native lint pass does not flag the family unused.
var _ = []any{
	typeMethodByNameABI, typeMethodABI, typeMethodsABI,
	typeMethodNamesABI, typeHasMethodByNameABI, bindABIMethod,
}

// uncommonOf returns t's UncommonType, mirroring internal/abi.(*Type).Uncommon.
func uncommonOf(t *abiType) *abiUncommon {
	if t.TFlag&tflagUncommon == 0 {
		return nil
	}
	const kindMask = (1 << 5) - 1
	var sz uintptr
	switch t.Kind & kindMask {
	case kindStruct:
		sz = unsafe.Sizeof(abiStructType{})
	case kindPointer:
		sz = unsafe.Sizeof(abiPtrType{})
	case kindFunc:
		sz = unsafe.Sizeof(abiFuncType{})
	case kindSlice:
		sz = unsafe.Sizeof(abiSliceType{})
	case kindArray:
		sz = unsafe.Sizeof(abiArrayType{})
	case kindChan:
		sz = unsafe.Sizeof(abiChanType{})
	case kindMap:
		sz = unsafe.Sizeof(abiMapType{})
	case kindInterface:
		sz = unsafe.Sizeof(abiInterfaceType{})
	default:
		sz = unsafe.Sizeof(abiType{})
	}
	return (*abiUncommon)(unsafe.Add(unsafe.Pointer(t), sz))
}

// exportedMethodsOf returns the exported prefix of t's method table.
func exportedMethodsOf(t *abiType) []abiMethod {
	u := uncommonOf(t)
	if u == nil || u.Xcount == 0 {
		return nil
	}
	return (*[1 << 16]abiMethod)(unsafe.Add(unsafe.Pointer(u),
		uintptr(u.Moff)))[:u.Xcount:u.Xcount]
}

// nameOffString decodes the abi.Name record at a NameOff resolved against base.
func nameOffString(base *abiType, off uint32) string {
	p := (*byte)(resolveNameOff(unsafe.Pointer(base), int32(off)))
	if p == nil {
		return ""
	}
	// abi.Name: flags byte, uvarint length, bytes.
	n := uintptr(0)
	shift := uint(0)
	i := uintptr(1)
	for {
		b := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		i++
		n |= uintptr(b&0x7f) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
	}
	return unsafe.String((*byte)(unsafe.Add(unsafe.Pointer(p), i)), int(n))
}

// methodTypeWithRecv rebuilds the reflect Method type (receiver first).
func methodTypeWithRecv(recv reflect.Type, sig reflect.Type) reflect.Type {
	in := make([]reflect.Type, 0, 1+sig.NumIn())
	in = append(in, recv)
	for t := range sig.Ins() {
		in = append(in, t)
	}
	out := make([]reflect.Type, 0, sig.NumOut())
	for t := range sig.Outs() {
		out = append(out, t)
	}
	return reflect.FuncOf(in, out, sig.IsVariadic())
}

// forgeFuncValue builds the same fake-funcval Value reflect.Type.Method
// returns for Method.Func, but with the code pointer in an unscanned uintptr
// cell instead of a GC-scanned unsafe.Pointer cell.
func forgeFuncValue(mt reflect.Type, pc uintptr) reflect.Value {
	cell := new(uintptr) // pointer-free alloc: the GC never reads the PC
	*cell = pc
	type rvHeader struct {
		typ  unsafe.Pointer
		ptr  unsafe.Pointer
		flag uintptr
	}
	out := reflect.Zero(mt)
	(*rvHeader)(unsafe.Pointer(&out)).ptr = unsafe.Pointer(cell)
	return out
}

// methodFromABI builds the reflect.Method for t's i'th exported abi method,
// forged-Func like the lookups.
func methodFromABI(t reflect.Type, at *abiType, i int, m abiMethod) (reflect.Method, bool) {
	mtyp := (*abiType)(resolveTypeOff(unsafe.Pointer(at), int32(m.Mtyp)))
	if mtyp == nil {
		return reflect.Method{}, false
	}
	mt := methodTypeWithRecv(t, asReflectType(mtyp))
	pc := uintptr(resolveTextOff(unsafe.Pointer(at), int32(m.Tfn)))
	return reflect.Method{
		Name:  nameOffString(at, m.Name),
		Type:  mt,
		Func:  forgeFuncValue(mt, pc),
		Index: i,
	}, true
}

// typeMethodByNameABI is the wasm-safe MethodByName over the abi tables.
// Interface types never reach here (their Methods carry no Func).
func typeMethodByNameABI(t reflect.Type, name string) (reflect.Method, bool) {
	at := rtypePtr(t)
	for i, m := range exportedMethodsOf(at) {
		if nameOffString(at, m.Name) == name {
			return methodFromABI(t, at, i, m)
		}
	}
	return reflect.Method{}, false
}

// typeMethodABI builds the i'th exported Method (Value.Method's index space).
func typeMethodABI(t reflect.Type, i int) (reflect.Method, bool) {
	at := rtypePtr(t)
	ms := exportedMethodsOf(at)
	if i < 0 || i >= len(ms) {
		return reflect.Method{}, false
	}
	return methodFromABI(t, at, i, ms[i])
}

// typeHasMethodByNameABI reports the method's presence without building it.
func typeHasMethodByNameABI(t reflect.Type, name string) bool {
	return exportedMethodIndexABI(t, name) >= 0
}

// exportedMethodIndexABI returns name's index among t's exported methods, -1 if absent.
// This index is Value.Method's index space, so v.Method(i) replaces
// v.MethodByName(name) without the Type.MethodByName call the latter hides
// (reflect/value.go MethodByName resolves the index via Type.MethodByName,
// which spills the method PC).
func exportedMethodIndexABI(t reflect.Type, name string) int {
	at := rtypePtr(t)
	for i, m := range exportedMethodsOf(at) {
		if nameOffString(at, m.Name) == name {
			return i
		}
	}
	return -1
}

// typeMethodsABI builds all exported Methods, forged-Func like the lookup.
func typeMethodsABI(t reflect.Type) []reflect.Method {
	at := rtypePtr(t)
	ms := exportedMethodsOf(at)
	out := make([]reflect.Method, 0, len(ms))
	for i, m := range ms {
		if rm, ok := methodFromABI(t, at, i, m); ok {
			out = append(out, rm)
		}
	}
	return out
}

// bindABIMethod returns a bound method func Value calling through the forged
// unbound Method.Func. Stock bound method values (Value.Method) re-resolve the
// code PC on every call in reflect's methodReceiver, which heap-spills it into
// an escaping GC-scanned unsafe.Pointer cell; on wasm the PC aliases a heap
// address and the write barrier aborts ("found bad pointer in Go heap").
func bindABIMethod(m reflect.Method, recv reflect.Value) reflect.Value {
	recv = Exportable(recv)
	mt := m.Type
	in := make([]reflect.Type, 0, mt.NumIn()-1)
	for i := 1; i < mt.NumIn(); i++ {
		in = append(in, mt.In(i))
	}
	out := make([]reflect.Type, mt.NumOut())
	for i := range out {
		out[i] = mt.Out(i)
	}
	sig := reflect.FuncOf(in, out, mt.IsVariadic())
	mf := m.Func
	return reflect.MakeFunc(sig, func(args []reflect.Value) []reflect.Value {
		full := append(make([]reflect.Value, 0, 1+len(args)), recv)
		full = append(full, args...)
		if mt.IsVariadic() {
			return mf.CallSlice(full)
		}
		return mf.Call(full)
	})
}

// typeMethodNamesABI lists exported method names without building Methods.
func typeMethodNamesABI(t reflect.Type) []string {
	at := rtypePtr(t)
	ms := exportedMethodsOf(at)
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = nameOffString(at, m.Name)
	}
	return names
}
