package interp

import (
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
	"github.com/mvm-sh/mvm/vm/synth"
)

// attachSynthMethods walks every compiled type and asks the machine to
// install a synthesized rtype carrying that type's methods.
// No-op when synth.Enabled() is false (the default; opt in via MVM_SYNTH=1).
//
// Idempotency: each *vm.Type is attached at most once per Interp lifetime.
// The compiler aliases symbols under both bare and pkg-qualified keys
// (compiler.go:136), so the same *Type is reached twice per walk; and
// re-entrant Eval (test_cmd's package + _testmain) walks again.
// Without dedup the S1 stub pool exhausts after ~64 packages.
//
// Known gap (Phase 3a sweep): replacing t.Rtype does NOT refresh derived
// rtypes (reflect.PointerTo, SliceOf, MapOf) that the compiler captured
// against the pre-synth layout and baked into c.Data immediates.
// The compiler-internal dedup maps (typeSyms, zeroTypeIdxs) now key on
// *vm.Type pointer identity (step 1 of the symbolic-types refactor), so
// the t.Rtype swap itself no longer desyncs caches; only the derived-rtype
// cascade remains.
// Currently mvm-level iface dispatch routes through *vm.Type so most
// observable behavior is correct; native-reflect paths (json.Marshal,
// MethodByName etc.) get the synth rtype only on values built post-attach.
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
		if err := i.AttachSynthMethods(sym.Type); err != nil {
			return err
		}
		i.synthAttached[sym.Type] = true
	}
	return nil
}
