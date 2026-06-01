package interp

import (
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// attachSynthMethods walks every compiled type and asks the machine to
// install a synthesized rtype carrying that type's methods.
//
// Idempotency: each *vm.Type is attached at most once per Interp lifetime.
// The compiler aliases symbols under both bare and pkg-qualified keys, so
// the same *Type is reached twice per walk; and re-entrant Eval
// (test_cmd's package + _testmain) walks again.
// Without dedup the S1 stub pool exhausts after ~64 packages.
//
// The reserve/fill path installs methods into each type's reserved synth
// identity in place, so no rtype swap or cascade is needed; Compiler.FillTypeSlots
// then settles the deferred c.Data slots to those final rtypes.
// See [[project_synth_rtype_poc]] and [[project_symbolic_types_refactor]].
func (i *Interp) attachSynthMethods() error {
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
		if err := i.AttachSynthMethods(sym.Type); err != nil {
			return err
		}
		i.synthAttached[sym.Type] = true
	}
	i.FillTypeSlots()
	return nil
}
