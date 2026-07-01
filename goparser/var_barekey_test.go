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

// Phase 2 clears importingPkg, so funcDeclKey must qualify by CompilingPkg.
func TestFuncDeclKeyPrefersCompilingPkg(t *testing.T) {
	p := NewParser(golang.GoSpec, false)
	p.CompilingPkg = "bytes"
	if got := p.funcDeclKey("growSlice"); got != "bytes.growSlice" {
		t.Errorf("CompilingPkg: got %q, want bytes.growSlice", got)
	}
	p.CompilingPkg = ""
	p.importingPkg = "encoding/gob"
	if got := p.funcDeclKey("growSlice"); got != "encoding/gob.growSlice" {
		t.Errorf("importingPkg: got %q, want encoding/gob.growSlice", got)
	}
}

// The dispatch sites use symGet, so a foreign same-named generic at the bare key
// can't hijack a call here (the growSlice[E] collision at a use site).
func TestGenericDispatchNotHijackedByForeignAlias(t *testing.T) {
	p := NewParser(golang.GoSpec, false)
	p.importingPkg = "p"
	p.Symbols["Name"] = &symbol.Symbol{ // foreign bare alias
		Name: "otherpkg.Name", Kind: symbol.Generic, Index: symbol.UnsetAddr,
		Data: &genericTemplate{name: "Name", isFunc: true, typeParams: []typeParam{{name: "T"}}},
	}
	toks, err := p.scan("Name(1)", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.parseExpr(toks, ""); err != nil {
		t.Fatalf("foreign bare-alias generic hijacked the call: %v", err)
	}
}

// A non-generic func must not be dropped when another package's same-named
// generic leaked to the bare key (bytes.growSlice vs gob's growSlice[E]).
func TestParseFuncNotSkippedByForeignGeneric(t *testing.T) {
	p := NewParser(golang.GoSpec, false)
	p.SymSet("growSlice", &symbol.Symbol{Name: "growSlice", Kind: symbol.Generic, Index: symbol.UnsetAddr})
	p.CompilingPkg = "bytes"
	toks, err := p.scan("func growSlice() {}", false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.parseFunc(toks)
	if err != nil {
		t.Fatalf("parseFunc: %v", err)
	}
	if out == nil {
		t.Fatal("bytes.growSlice body skipped as a generic template")
	}
}
