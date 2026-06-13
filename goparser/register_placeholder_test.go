package goparser

import (
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
)

// registerStructPlaceholder must reuse one *Type across a same-compile fixpoint
// re-parse (so a grouped type decl re-run by the outer loop does not mint a twin),
// yet hand a redefinition in a later compile a fresh identity.
func TestRegisterStructPlaceholderGeneration(t *testing.T) {
	p := NewParser(golang.GoSpec, false)
	const key = "pkg.T"

	// Generation 1 (one resolveDecls).
	p.genCounter++
	p.curGen = p.genCounter
	a := p.registerStructPlaceholder(key, "T")

	// Same gen, still an unfilled placeholder: reuse.
	if got := p.registerStructPlaceholder(key, "T"); got != a {
		t.Fatal("same-gen reuse of an unfilled placeholder failed")
	}

	// The decl fills the placeholder in place (grouped-decl first pass).
	a.Placeholder = false

	// Same gen, now filled: a fixpoint re-parse must reuse, not mint a twin.
	if got := p.registerStructPlaceholder(key, "T"); got != a {
		t.Fatal("same-gen re-parse minted a twin instead of reusing the filled placeholder")
	}

	// A later compile (e.g. REPL redefinition) gets a fresh identity.
	p.genCounter++
	p.curGen = p.genCounter
	if got := p.registerStructPlaceholder(key, "T"); got == a {
		t.Fatal("cross-gen redefinition reused the prior type instead of minting a fresh one")
	}
}
