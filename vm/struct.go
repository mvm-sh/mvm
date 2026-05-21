package vm

import (
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe" //nolint:depguard
)

var (
	// placeholderSeq ensures each placeholder gets a unique reflect type,
	// preventing reflect.StructOf from returning a cached (shared) rtype.
	placeholderSeq atomic.Uint64

	// structTypeSize is the byte size of reflect's internal structType.
	// Computed at init time by probing reflect internals.
	structTypeSize uintptr

	intRtype = reflect.TypeOf(0)
)

func init() {
	// Create a struct type with a distinctive field count and scan for
	// the Fields slice header to determine structType size.
	// structType layout: abi.Type (base) + abi.Name (*byte) + []structField.
	// The slice header is fixed-size, so structType has the same size
	// regardless of field count.
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
	panic("vm: cannot determine reflect structType size")
}

// rtypeData extracts the *rtype data pointer from a reflect.Type interface.
func rtypeData(t reflect.Type) unsafe.Pointer {
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&t))[1]
}

// patchRtype overwrites dst's internal rtype with src's rtype bytes,
// skipping the Str (nameOff) and PtrToThis (typeOff) fields at byte
// offsets 40-47 in abi.Type.
//
// These 4-byte offsets are registered in reflect's global offset map
// for each rtype's heap address. Copying them from src crashes because
// the dst has a different address ("nameOff/typeOff base pointer out of
// range"). Zeroing them also crashes because rtype.String() cannot
// resolve offset 0. Keeping dst's originals is safe: they were
// registered when the placeholder was created by reflect.StructOf.
func patchRtype(dst, src reflect.Type) {
	d := rtypeData(dst)
	s := rtypeData(src)
	for i := uintptr(0); i < 40; i++ {
		*(*byte)(unsafe.Add(d, i)) = *(*byte)(unsafe.Add(s, i))
	}
	for i := uintptr(48); i < structTypeSize; i++ {
		*(*byte)(unsafe.Add(d, i)) = *(*byte)(unsafe.Add(s, i))
	}
}

// NewStructType creates a forward-declared struct type named name (the empty
// string for anonymous placeholders). Register it in the symbol table, then
// call SetFields to finalize.
func NewStructType(name string) *Type {
	// Each placeholder must have a unique field name to prevent reflect.StructOf
	// from returning a cached (shared) rtype, which would cause data races when
	// multiple struct types are patched concurrently. Embedding name makes the
	// finalized type's String() identify the interpreted type in native
	// diagnostics (gob, fmt %T, reflect panics): SetFields keeps the
	// placeholder's name string (see patchRtype), so without name those report
	// an opaque "struct { P<n> int }".
	n := placeholderSeq.Add(1)
	sf := []reflect.StructField{{Name: placeholderFieldName(name, n), Type: intRtype}}
	return &Type{Rtype: reflect.StructOf(sf), Placeholder: true}
}

// placeholderFieldName builds a unique, exported, valid Go identifier for a
// placeholder struct's sole field. The leading "P" guarantees an exported
// leading letter (reflect.StructOf rejects unexported fields without a
// PkgPath) regardless of name's first rune.
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

// SetFields finalizes a forward-declared struct type using src's definition.
// It patches the internal reflect.Type in place so that any derived types
// (e.g., pointer types created via PointerTo) automatically see the real layout.
func (t *Type) SetFields(src *Type) {
	if t.Rtype == nil || t.Rtype.Kind() != reflect.Struct || !t.Placeholder {
		// Not a fresh reflect.StructOf placeholder: a bare-name type collision
		// (e.g. another package's `type X uint16`) may have left a shared,
		// read-only static rtype here. patchRtype would memcpy onto read-only
		// memory and crash; adopt src's layout by reference instead.
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
