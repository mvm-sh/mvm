package vm

import (
	"reflect"

	"github.com/mvm-sh/mvm/derive"
)

// derive cannot import vm, so vm injects the hooks needing vm internals here.
func init() {
	derive.ShapeAvailable = func(sig reflect.Type) bool {
		if _, ok := detectShape(sig); ok {
			return true
		}
		return wordShapeAvailable(sig)
	}
	derive.ActiveRtypeCache = func() *map[derive.MethodStructKey]*derive.SynthReservation {
		m := ActiveMachine()
		if m == nil {
			return nil
		}
		return &m.sharedMethodStructs
	}
}
