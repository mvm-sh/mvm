package interp

import (
	"fmt"

	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// attachErr locates a synth-attach failure at the type's declaration.
// ErrPos lets the diagnostic chokepoint (interp.Eval) render a snippet.
type attachErr struct {
	err error
	pos int
}

func (e *attachErr) Error() string { return e.err.Error() }
func (e *attachErr) Unwrap() error { return e.err }
func (e *attachErr) ErrPos() int   { return e.pos }

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
		if err := i.attachWithEmbeds(sym.Type); err != nil {
			return err
		}
	}
	i.FillTypeSlots()
	return nil
}

// maxBaseDepth caps Base-chain walks against cyclic chains (mirrors vm.CanonicalType).
const maxBaseDepth = 1024

// attachWithEmbeds attaches t's embedded interpreted types before t itself:
// promoted-method collection reads the embeds' native method tables, filled
// only at THEIR attach, and the symbol walk above is map-ordered.
// Pre-marking breaks self-embed cycles; on error the mark is removed so a
// later Eval retries instead of silently skipping the type.
func (i *Interp) attachWithEmbeds(t *vm.Type) (err error) {
	if t == nil || i.synthAttached[t] {
		return nil
	}
	i.synthAttached[t] = true
	defer func() {
		if err != nil {
			delete(i.synthAttached, t)
		}
	}()
	// *T owns no reservation; T's attach fills both method sets. Redirect to T.
	if t.IsPtr() && t.ElemType != nil {
		return i.attachWithEmbeds(t.ElemType)
	}
	for _, emb := range t.Embedded {
		e := emb.Type
		if e != nil && e.IsPtr() && e.ElemType != nil {
			e = e.ElemType
		}
		for d := 0; e != nil && d < maxBaseDepth; d, e = d+1, e.Base {
			if err := i.attachWithEmbeds(e); err != nil {
				return err
			}
		}
	}
	if err := i.AttachSynthMethods(t); err != nil {
		if loc := i.Sources.FormatPos(t.Pos); loc != "" {
			err = fmt.Errorf("%s: %w", loc, err)
		}
		return &attachErr{err: err, pos: t.Pos}
	}
	return nil
}
