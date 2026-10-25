//go:build go1.27 && !goexperiment.mapsplitgroup

package runtype

import "reflect"

// setMapGroup sets mt's group type and geometry for the key and elem types,
// stored as given (indirection already applied), as reflect.MapOf does for
// the interleaved group layout (KVKV...), the default. n makes the field
// names unique, so reflect.StructOf builds a new layout.
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
	mt.KeysOff = group.Field(1).Offset
	mt.KeyStride = slot.Size()
	mt.ElemsOff = group.Field(1).Offset + slot.Field(1).Offset
	mt.ElemStride = slot.Size()
	mt.ElemOff = slot.Field(1).Offset
}
