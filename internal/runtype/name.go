package runtype

import (
	"encoding/binary"
	"unsafe"
)

// encodeName produces an internal/abi.Name-format byte record.
// Format: one flag byte (bit0=exported, bit1=has-tag, bit2=has-pkgpath,
// bit3=embedded), then uvarint(len(name)) and the name bytes.
// We avoid linknaming reflect.newName to keep the package buildable without
// -checklinkname=0.
// Names have no tag; pkgpath is handled separately by encodeNamePkg.
func encodeName(name string, exported bool) abiName {
	var flags byte
	if exported {
		flags |= 0b0001
	}
	var lenBuf [binary.MaxVarintLen64]byte
	lenSize := binary.PutUvarint(lenBuf[:], uint64(len(name)))

	buf := make([]byte, 1+lenSize+len(name))
	buf[0] = flags
	copy(buf[1:], lenBuf[:lenSize])
	copy(buf[1+lenSize:], name)
	return abiName{Bytes: &buf[0]}
}

// encodeNamePkg is encodeName with an embedded import path (abi.Name bit2), so
// native reflect.Implements can match an unexported method against an unexported
// interface method. Layout: flags(bit2) | uvarint(len) | name | 4-byte nameOff
// to the path's name record (native order). pkgPath == "" uses the plain form.
func encodeNamePkg(name string, exported bool, pkgPath string) abiName {
	if pkgPath == "" {
		return encodeName(name, exported)
	}
	pkgOff := addReflectOff(unsafe.Pointer(encodeName(pkgPath, false).Bytes))

	var flags byte = 0b0100 // bit2: import path follows
	if exported {
		flags |= 0b0001
	}
	var lenBuf [binary.MaxVarintLen64]byte
	lenSize := binary.PutUvarint(lenBuf[:], uint64(len(name)))

	buf := make([]byte, 1+lenSize+len(name)+4)
	buf[0] = flags
	copy(buf[1:], lenBuf[:lenSize])
	copy(buf[1+lenSize:], name)
	binary.NativeEndian.PutUint32(buf[1+lenSize+len(name):], uint32(pkgOff))
	return abiName{Bytes: &buf[0]}
}
