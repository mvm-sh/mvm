//go:build go1.27

package runtype

import "unsafe"

// abiMapType mirrors internal/abi.MapType from Go 1.27, which describes the
// keys and elems of a group by offsets and strides, for both group layouts
// (see setMapGroup).
type abiMapType struct {
	abiType
	Key        *abiType
	Elem       *abiType
	Group      *abiType
	Hasher     func(unsafe.Pointer, uintptr) uintptr
	GroupSize  uintptr
	KeysOff    uintptr
	KeyStride  uintptr
	ElemsOff   uintptr
	ElemStride uintptr
	ElemOff    uintptr // interleaved layout only
	Flags      uint32
	_          uint32 // align following UncommonType on 32 and 64-bit targets
}
