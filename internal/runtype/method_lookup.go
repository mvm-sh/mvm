package runtype

import (
	"reflect"
	"unsafe"
)

// Reflect's Type.Method/MethodByName store the method's textOff-resolved code
// PC in a heap-allocated unsafe.Pointer cell (the fake funcval behind
// Method.Func). Native code PCs live outside the GC arena, but wasm code PCs
// are plain integers >= 0x1000_0000 that alias heap addresses once the arena
// has grown that large, and the write barrier plus heap scans then abort the
// process ("found bad pointer in Go heap" / "found pointer to free object").
// TypeMethodByName and friends are drop-in lookups that on wasm rebuild the
// Method from the abi tables with the PC in an unscanned uintptr cell.
// On native they defer to stock reflect.

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
	for i := range sig.NumIn() {
		in = append(in, sig.In(i))
	}
	out := make([]reflect.Type, 0, sig.NumOut())
	for i := range sig.NumOut() {
		out = append(out, sig.Out(i))
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

// typeMethodByNameABI is the wasm-safe MethodByName over the abi tables.
// Interface types never reach here (their Methods carry no Func).
func typeMethodByNameABI(t reflect.Type, name string) (reflect.Method, bool) {
	at := rtypePtr(t)
	for i, m := range exportedMethodsOf(at) {
		if nameOffString(at, m.Name) != name {
			continue
		}
		mtyp := (*abiType)(resolveTypeOff(unsafe.Pointer(at), int32(m.Mtyp)))
		if mtyp == nil {
			return reflect.Method{}, false
		}
		mt := methodTypeWithRecv(t, asReflectType(mtyp))
		pc := uintptr(resolveTextOff(unsafe.Pointer(at), int32(m.Tfn)))
		return reflect.Method{
			Name:  name,
			Type:  mt,
			Func:  forgeFuncValue(mt, pc),
			Index: i,
		}, true
	}
	return reflect.Method{}, false
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
		mtyp := (*abiType)(resolveTypeOff(unsafe.Pointer(at), int32(m.Mtyp)))
		if mtyp == nil {
			continue
		}
		mt := methodTypeWithRecv(t, asReflectType(mtyp))
		pc := uintptr(resolveTextOff(unsafe.Pointer(at), int32(m.Tfn)))
		out = append(out, reflect.Method{
			Name:  nameOffString(at, m.Name),
			Type:  mt,
			Func:  forgeFuncValue(mt, pc),
			Index: i,
		})
	}
	return out
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
