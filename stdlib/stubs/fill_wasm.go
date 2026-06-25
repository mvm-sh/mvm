//go:build wasm

package stubs

import (
	"github.com/mvm-sh/mvm/runtype"
)

// sharedStub is the single dispatch PC wired into every synth method on wasm.
// It is never invoked: on the all-interpreted wasm target no native caller
// dispatches an interpreted method through an itab or native-internal reflect
// (interpreted code uses IfaceCall; interpreted reflect is intercepted by the
// vm). The PC exists only so the synth rtype carries a method set that reflect
// introspection (Implements/NumMethod/MethodByName) can read. Dropping the
// per-signature pools removes ~53k generated stub functions from the binary.
func sharedStub() {
	panic("stubs: shared wasm stub invoked; native dispatch of an interpreted method is unsupported on the all-interpreted wasm target")
}

var sharedStubPC = runtype.FuncPC(sharedStub)

// FillMethods installs methods into a reserved rtype, wiring every method's
// Ifn/Tfn to the one shared stub PC (see sharedStub).
func FillMethods(res *runtype.Reservation, methods []Method) error {
	specs := make([]runtype.MethodSpec, len(methods))
	for i, m := range methods {
		specs[i] = runtype.MethodSpec{
			Name:     m.Name,
			Exported: m.Exported,
			PkgPath:  m.PkgPath,
			Sig:      m.Sig,
			StubPC:   sharedStubPC,
		}
	}
	return res.Fill(specs)
}
