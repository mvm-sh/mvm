package derive

import (
	"reflect"
	"sync"

	"github.com/mvm-sh/mvm/mtype"
)

var nativeLayoutTypes sync.Map // qualified type name -> native reflect.Type

var nativeIdentityTypes sync.Map // qualified type name -> native reflect.Type

// RegisterNativeIdentity makes an interpreted method-bearing type reuse the host
// rtype, so values also arriving from a native pkg that links it (fs.PathError from
// os.Open) keep one identity across the boundary.
func RegisterNativeIdentity(name string, rt reflect.Type) {
	nativeIdentityTypes.Store(name, rt)
}

// nativeIdentityFor returns the registered host rtype for t, or nil.
// Import path, kind, and shape (field count, interface method names) must
// match, so a same-named type from another package or a stdlib version skew
// falls back to synth.
func nativeIdentityFor(t *mtype.Type) reflect.Type {
	if t.Name == "" {
		return nil
	}
	v, ok := nativeIdentityTypes.Load(QualifiedTypeName(t))
	if !ok {
		return nil
	}
	rt := v.(reflect.Type)
	if rt.Kind() != t.Kind() || rt.PkgPath() != RtypePkgPath(t) {
		return nil
	}
	switch rt.Kind() {
	case reflect.Struct:
		if rt.NumField() != len(t.Fields) {
			return nil
		}
	case reflect.Interface:
		if rt.NumMethod() != len(t.IfaceMethods) {
			return nil
		}
		for i := range t.IfaceMethods {
			if _, ok := rt.MethodByName(t.IfaceMethods[i].Name); !ok {
				return nil
			}
		}
	}
	return rt
}

// HasNativeIdentity reports whether t reuses a host rtype.
// Its methods dispatch natively, so synth attach is skipped.
func HasNativeIdentity(t *mtype.Type) bool {
	return nativeIdentityFor(t) != nil
}

// RegisterNativeLayout marks a named interpreted struct type whose field layout
// must keep native non-empty interface fields as iface, not the erased interface{}
// (see BuildStructRtypeKeepIface). For a type interpreted for behavior but stored
// into a native field (e.g. log.Logger -> http.Server.ErrorLog), where the erased
// eface would crash a native reflect walk. Methods stay interpreted.
func RegisterNativeLayout(name string, rt reflect.Type) {
	nativeLayoutTypes.Store(name, rt)
}

// nativeLayoutRegistered reports whether t was registered. The native field count
// must match, so a stdlib version skew falls back to the synth layout.
func nativeLayoutRegistered(t *mtype.Type) bool {
	v, ok := nativeLayoutTypes.Load(QualifiedTypeName(t))
	if !ok {
		return false
	}
	rt := v.(reflect.Type)
	return rt.Kind() == reflect.Struct && rt.NumField() == len(t.Fields)
}
