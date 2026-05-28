package runtype

import "encoding/binary"

// encodeName produces an internal/abi.Name-format byte record.
// Format: one flag byte (bit0=exported, bit1=has-tag, bit2=has-pkgpath,
// bit3=embedded), then uvarint(len(name)) and the name bytes.
// We avoid linknaming reflect.newName to keep the package buildable without
// -checklinkname=0.
// Phase 1 only creates names with no tag and no pkgpath.
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
