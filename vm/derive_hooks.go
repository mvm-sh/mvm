package vm

import (
	"reflect"

	"github.com/mvm-sh/mvm/internal/derive"
)

// globalMethodStructs is the wasm-only cross-machine cache of method-bearing
// struct rtypes (see ActiveRtypeCache). Accessed under derive's derivedMu.
var globalMethodStructs map[derive.MethodStructKey]*derive.SynthReservation

// derive cannot import vm, so vm injects the hooks needing vm internals here.
func init() {
	derive.ShareMethodCarriers = synthSharedPC
	derive.ShapeAvailable = func(sig reflect.Type) bool {
		if _, ok := detectShape(sig); ok {
			return true
		}
		return wordShapeAvailable(sig)
	}
	derive.ActiveRtypeCache = func() *map[derive.MethodStructKey]*derive.SynthReservation {
		if synthSharedPC {
			// wasm: a synth fill captures no *Machine (fill_wasm.go) and callMethod
			// re-enters on a pooled runner, so a process-global cache is sound and
			// gives a named type one attached rtype across every machine. Per-Machine
			// caches split it, so a child's rtype fails reflect.Implements. Like sharedStructs.
			return &globalMethodStructs
		}
		m := ActiveMachine()
		if m == nil {
			return nil
		}
		return &m.sharedMethodStructs
	}
}
