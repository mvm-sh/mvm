package derive

import (
	"reflect"
	"sync"

	"github.com/mvm-sh/mvm/mtype"
)

var nativeLayoutTypes sync.Map // qualified type name -> native reflect.Type

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
