package runtype

import (
	"reflect"
	"sort"
	"unsafe"
)

// Imethod is one interface method: name, export status, and no-receiver
// signature (e.g. func() bool for Timeout).
type Imethod struct {
	Name     string
	Exported bool
	Sig      reflect.Type
}

// InterfaceOf builds a synthetic interface rtype whose required method set is
// methods, so native reflect AssignableTo/Implements resolve satisfaction
// against the real methods rather than a methodless any.
// Methods sit inline in InterfaceType.Methods (no uncommon overlay, no stub
// pool: an interface declares methods, it does not implement them).
// Each Imethod.Sig must be the canonical no-receiver func type: reflect.FuncOf
// and vm.FuncOf dedup via the runtime type table, so the rtype pointer the
// runtime compares against the concrete method's matches.
// An empty method set yields any; name is stamped into Str; result is anonymous.
func InterfaceOf(name, pkgPath string, methods []Imethod) reflect.Type {
	if len(methods) == 0 {
		return reflect.TypeFor[any]()
	}
	// error: a non-empty interface, correct layout/GCData/Equal for the 2-word iface.
	proto := rtypePtr(reflect.TypeFor[error]())

	b := new(synthIface)
	b.t.abiType = *proto
	stampIfaceHeader(&b.t.abiType, name)
	b.t.PkgPath = encodeName(pkgPath, false)

	order := make([]int, len(methods))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		return methods[order[i]].Name < methods[order[j]].Name
	})
	ims := make([]abiImethod, len(methods))
	for i, idx := range order {
		mm := methods[idx]
		ims[i] = abiImethod{
			Name: addReflectOff(unsafe.Pointer(encodeName(mm.Name, mm.Exported).Bytes)),
			Typ:  addReflectOff(unsafe.Pointer(rtypePtr(mm.Sig))),
		}
	}
	b.methods = ims
	b.t.Methods = ims

	registerLayout(&b.t.abiType, proto)
	return asReflectType(&b.t.abiType)
}

// methods keeps the abiImethod backing array alive alongside the rtype.
type synthIface struct {
	t       abiInterfaceType
	methods []abiImethod
}

// stampIfaceHeader clears tflagUncommon (interface methods live in
// InterfaceType.Methods; with the bit set the runtime reads the PkgPath/Methods
// region as a bogus uncommon header) and tflagNamed (the result is anonymous).
func stampIfaceHeader(t *abiType, name string) {
	t.TFlag &^= (tflagExtraStar | tflagUncommon | tflagNamed)
	t.Hash = nextSyntheticHash()
	t.PtrToThis = 0
	t.Str = addReflectOff(unsafe.Pointer(encodeName(name, false).Bytes))
}
