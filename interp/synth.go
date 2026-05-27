package interp

import (
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
	"github.com/mvm-sh/mvm/vm/synth"
)

// attachSynthMethods walks every compiled type and asks the machine to
// install a synthesized rtype carrying that type's methods.
// No-op when synth.Enabled() is false (the default).
//
// Idempotency: each *vm.Type is attached at most once per Interp lifetime.
// The compiler aliases symbols under both bare and pkg-qualified keys
// (compiler.go:136), so the same *Type is reached twice per walk; and
// re-entrant Eval (test_cmd's package + _testmain) walks again.
// Without dedup the S1 stub pool exhausts after ~64 packages.
// See [[project_synth_rtype_poc]] for the design.
func (i *Interp) attachSynthMethods() error {
	if !synth.Enabled() {
		return nil
	}
	if i.synthAttached == nil {
		i.synthAttached = map[*vm.Type]bool{}
	}
	for _, sym := range i.Symbols {
		if sym.Kind != symbol.Type || sym.Type == nil {
			continue
		}
		if i.synthAttached[sym.Type] {
			continue
		}
		i.synthAttached[sym.Type] = true
		if err := i.AttachSynthMethods(sym.Type); err != nil {
			return err
		}
	}
	return nil
}
