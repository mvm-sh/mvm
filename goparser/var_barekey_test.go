package goparser

import (
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/symbol"
)

// A package-level `var X T = ...` declared while parsing an imported package must
// register under the pkg-qualified key even when a same-named bare symbol already
// resolves via symGet (a universe-style alias, Name "X" not "<pkg>.X", which
// foreignBareAlias keeps). Otherwise the type write hits a nil slot and panics --
// net/http's golang.org/x/text/transform.Discard vs a bare Discard on wasm.
func TestVarDeclBareKeyCollision(t *testing.T) {
	p := NewParser(golang.GoSpec, false)
	p.pkgName = "p"
	p.importingPkg = "p"
	p.SymAdd(symbol.UnsetAddr, "Discard", nilValue, symbol.Var, nil) // bare alias

	toks, err := p.scan("var Discard int = 1", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ParseDecl(toks); err != nil {
		t.Fatalf("ParseDecl: %v", err)
	}
	sym, ok := p.Symbols["p.Discard"]
	if !ok {
		t.Fatal("p.Discard not registered under its qualified key")
	}
	if sym.Type == nil {
		t.Fatal("p.Discard registered without its declared type")
	}
}
