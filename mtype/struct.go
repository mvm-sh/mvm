package mtype

import (
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

var (
	// Unique placeholder field names keep reflect.StructOf from returning a
	// cached (shared) rtype.
	placeholderSeq atomic.Uint64

	// Byte size of reflect's internal structType, probed at init.
	structTypeSize uintptr

	intRtype = reflect.TypeFor[int]()

	// Pins every source rtype whose internal arrays patchRtype aliases by pointer
	// (the raw copy bypasses the GC write barrier), so the collector can't free
	// them while a placeholder still points in -- a flaky go1.26 "bad pointer".
	patchKeepAliveMu sync.Mutex
	patchKeepAlive   []reflect.Type
)

func init() {
	// Probe structType's size by scanning a built struct for its Fields slice
	// header; the header is fixed-size, so the result is field-count-independent.
	const nfields = 7
	sf := make([]reflect.StructField, nfields)
	for i := range sf {
		sf[i] = reflect.StructField{Name: string(rune('A' + i)), Type: intRtype}
	}
	rt := reflect.StructOf(sf)
	data := rtypeData(rt)
	ws := unsafe.Sizeof(uintptr(0))
	for off := ws; off < 256; off += ws {
		lenp := (*int)(unsafe.Add(data, off+ws))
		capp := (*int)(unsafe.Add(data, off+2*ws))
		if *lenp == nfields && *capp >= nfields {
			structTypeSize = off + 3*ws
			return
		}
	}
	panic("mtype: cannot determine reflect structType size")
}

// rtypeData extracts the *rtype data pointer from a reflect.Type interface.
func rtypeData(t reflect.Type) unsafe.Pointer {
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&t))[1]
}

// patchRtype overwrites dst's rtype bytes with src's, skipping Str/PtrToThis
// (the nameOff/typeOff at offsets 40-47): those are registered per heap address,
// so copying or zeroing them crashes rtype.String(); dst's originals stay valid.
func patchRtype(dst, src reflect.Type) {
	// Pin src; its arrays are aliased below without a write barrier.
	patchKeepAliveMu.Lock()
	patchKeepAlive = append(patchKeepAlive, src)
	patchKeepAliveMu.Unlock()

	d := rtypeData(dst)
	s := rtypeData(src)
	for i := range uintptr(40) {
		*(*byte)(unsafe.Add(d, i)) = *(*byte)(unsafe.Add(s, i))
	}
	for i := uintptr(48); i < structTypeSize; i++ {
		*(*byte)(unsafe.Add(d, i)) = *(*byte)(unsafe.Add(s, i))
	}
}

// NewStructType creates a forward-declared struct placeholder named name (empty
// for anonymous). Register it, then call SetFields to finalize. The placeholder
// is symbolic (Rtype nil); comp materializes the rtype once the fields are set.
func NewStructType(name string) *Type {
	return &Type{Name: name, kind: reflect.Struct, Placeholder: true}
}

// NewPlaceholderRtype builds a fresh, uniquely-shaped struct rtype used to break
// pointer cycles while materializing a named struct: install it as the type's
// Rtype before materializing fields (so a *T built mid-recursion resolves), then
// PatchRtype it in place with the real layout.
func NewPlaceholderRtype(name string) reflect.Type {
	n := placeholderSeq.Add(1)
	sf := []reflect.StructField{{Name: placeholderFieldName(name, n), Type: intRtype}}
	return reflect.StructOf(sf)
}

// PatchRtype overwrites dst's struct-rtype bytes with src's, preserving dst's
// identity (so derived/pointer rtypes captured against dst stay valid).
func PatchRtype(dst, src reflect.Type) { patchRtype(dst, src) }

// placeholderFieldName builds a unique exported identifier for the placeholder's
// sole field; the leading "P" guarantees export regardless of name's first rune.
func placeholderFieldName(name string, n uint64) string {
	var b strings.Builder
	b.WriteByte('P')
	for _, r := range name {
		if r == '_' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	b.WriteByte('_')
	b.WriteString(strconv.FormatUint(n, 10))
	return b.String()
}

// SetFields finalizes a forward-declared struct from src, patching the rtype in
// place so derived types (e.g. PointerTo) see the real layout.
func (t *Type) SetFields(src *Type) {
	if t.Rtype == nil {
		// Symbolic placeholder (post-flip): adopt src's symbolic shape; the rtype
		// is materialized later at comp. Identity (*Type pointer) is preserved so
		// forward references stay bound.
		t.kind = reflect.Struct
		t.Fields = src.Fields
		t.Embedded = src.Embedded
		t.Tags = src.Tags
		t.Placeholder = false
		return
	}
	if t.Rtype.Kind() != reflect.Struct || !t.Placeholder {
		// Shared read-only rtype (e.g. a bare-name collision): patchRtype would
		// memcpy onto read-only memory, so adopt src's layout by reference.
		t.Rtype = src.Rtype
		t.Fields = src.Fields
		t.Embedded = src.Embedded
		t.Placeholder = false
		return
	}
	patchRtype(t.Rtype, src.Rtype)
	t.Fields = src.Fields
	t.Embedded = src.Embedded
	t.Placeholder = false
}
