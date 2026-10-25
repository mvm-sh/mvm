//go:build go1.27 && goexperiment.mapsplitgroup

package runtype

import "reflect"

// setMapGroup sets mt's group type and geometry for the key and elem types,
// stored as given (indirection already applied), as reflect.MapOf does for
// the split group layout (KK...VV...) of GOEXPERIMENT=mapsplitgroup. n makes
// the field names unique, so reflect.StructOf builds a new layout.
func setMapGroup(mt *abiMapType, key, elem reflect.Type, n string) {
	group := reflect.StructOf([]reflect.StructField{
		{Name: "C" + n, Type: reflect.TypeFor[uint64]()},
		{Name: "K" + n, Type: reflect.ArrayOf(mapGroupSlots, key)},
		{Name: "E" + n, Type: reflect.ArrayOf(mapGroupSlots, elem)},
	})
	mt.Group = rtypePtr(group)
	mt.GroupSize = group.Size()
	mt.KeysOff = group.Field(1).Offset
	mt.KeyStride = key.Size()
	mt.ElemsOff = group.Field(2).Offset
	mt.ElemStride = elem.Size()
	mt.ElemOff = 0
}
