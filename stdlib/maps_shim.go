package stdlib

import "github.com/mvm-sh/mvm/goparser"

// maps.Clone delegates to an unexported `clone(m any) any` //go:linkname'd to a
// runtime intrinsic mvm can't resolve. This overlay fills that forward-declared
// stub with a reflect-based body, so the mirror keeps maps.go verbatim.
const mapsCloneShim = `package maps

import "reflect"

func clone(m any) any {
	if m == nil {
		return nil
	}
	rv := reflect.ValueOf(m)
	if rv.Kind() != reflect.Map {
		return m
	}
	out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
	it := rv.MapRange()
	for it.Next() {
		out.SetMapIndex(it.Key(), it.Value())
	}
	return out.Interface()
}
`

func init() {
	goparser.RegisterSourceOverlay("maps", "mvm_maps_clone.go", mapsCloneShim)
}
