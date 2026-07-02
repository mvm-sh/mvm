package vm

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/mvm-sh/mvm/internal/derive"
	"github.com/mvm-sh/mvm/internal/wordabi"
)

// globalMethodStructs is the wasm-only cross-machine cache of method-bearing
// struct rtypes (see ActiveRtypeCache). Accessed under derive's derivedMu.
var globalMethodStructs map[derive.MethodStructKey]*derive.SynthReservation

// ifaceShapes collects the word keys iface-method sigs demand (MVM_IFACESHAPES).
// Generated pools must cover this set for interface satisfaction; attach-only
// shapes outside it never affect reflect.Implements.
var (
	ifaceShapesMu sync.Mutex
	ifaceShapes   = map[string]string{} // key -> one example sig
)

// IfaceShapeReport lists the collected keys, one "ifaceshape key example-sig" per line.
func IfaceShapeReport() string {
	ifaceShapesMu.Lock()
	defer ifaceShapesMu.Unlock()
	keys := make([]string, 0, len(ifaceShapes))
	for k := range ifaceShapes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "ifaceshape %s %s\n", k, ifaceShapes[k])
	}
	return b.String()
}

// derive cannot import vm, so vm injects the hooks needing vm internals here.
func init() {
	derive.ShareMethodCarriers = synthSharedPC
	derive.ShapeAvailable = func(sig reflect.Type) bool {
		if _, ok := detectShape(sig); ok {
			return true
		}
		return wordShapeAvailable(sig)
	}
	if os.Getenv("MVM_IFACESHAPES") != "" {
		derive.IfaceShapeLog = func(sig reflect.Type) {
			if _, ok := detectShape(sig); ok {
				return // a typed pool serves it; no word pool needed
			}
			key, _, ok := wordabi.ClassifyWordSig(sig)
			if !ok {
				return
			}
			ifaceShapesMu.Lock()
			if _, seen := ifaceShapes[key]; !seen {
				ifaceShapes[key] = sig.String()
			}
			ifaceShapesMu.Unlock()
		}
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
