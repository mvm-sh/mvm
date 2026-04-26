package symbol

import (
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// Package is a package struct containing source or binary values.
type Package struct {
	Path   string
	Bin    bool
	Values map[string]vm.Value
}

// BinPkg returns a binary package from a map of reflect values.
func BinPkg(m map[string]reflect.Value, name string) *Package {
	p := &Package{Path: name, Bin: true, Values: map[string]vm.Value{}}
	for k, v := range m {
		// Remove the extra indirection from &var wrapping so the compiler
		// and VM see the variable's actual type (e.g. *T instead of **T).
		if v.Kind() == reflect.Pointer && v.Elem().CanSet() {
			v = v.Elem()
		}
		p.Values[k] = vm.FromReflect(v)
	}
	return p
}
