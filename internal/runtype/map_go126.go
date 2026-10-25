//go:build !go1.27

package runtype

import (
	"reflect"
	"unsafe"
)

// abiMapType mirrors internal/abi.MapType up to Go 1.26.
type abiMapType struct {
	abiType
	Key       *abiType
	Elem      *abiType
	Group     *abiType
	Hasher    func(unsafe.Pointer, uintptr) uintptr
	GroupSize uintptr
	SlotSize  uintptr
	ElemOff   uintptr
	Flags     uint32
	_         uint32 // align following UncommonType on 32 and 64-bit targets
}

// setMapGroup sets mt's group type and geometry for the key and elem types,
// stored as given (indirection already applied), as reflect.MapOf does. n
// makes the field names unique, so reflect.StructOf builds a new layout.
func setMapGroup(mt *abiMapType, key, elem reflect.Type, n string) {
	slot := reflect.StructOf([]reflect.StructField{
		{Name: "K" + n, Type: key},
		{Name: "E" + n, Type: elem},
	})
	group := reflect.StructOf([]reflect.StructField{
		{Name: "C" + n, Type: reflect.TypeFor[uint64]()},
		{Name: "S" + n, Type: reflect.ArrayOf(mapGroupSlots, slot)},
	})
	mt.Group = rtypePtr(group)
	mt.GroupSize = group.Size()
	mt.SlotSize = slot.Size()
	mt.ElemOff = slot.Field(1).Offset
}
