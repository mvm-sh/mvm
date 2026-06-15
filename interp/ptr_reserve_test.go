package interp

import (
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// Reservations key only on value types, never pointer *Types. A derived,
// named *T type with a filled method table (proto's noenforceutf8_test.go: a
// (*T)(nil) conversion symbol + resolveIfaceMethods on &T{}) reached the attach
// walk and failed "synth: ptr-method type T has no pointer reservation at
// attach". The walk must redirect a pointer to its elem, which owns the
// reservation and fills both method sets.
func TestAttachPtrTypeRedirectsToElem(t *testing.T) {
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	src := `
		type T struct{ X int }
		func (m *T) String() string { return "t" }
		var _ = T{}.X
	`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	sym, ok := i.Symbols["T"]
	if !ok || sym.Type == nil {
		t.Fatal("type T not found in symbol table")
	}
	valueT := sym.Type

	// Proto's derived *T symbol: named after T, over T's reserved *T rtype, with
	// the method table filled (as resolveIfaceMethods does for &T{}).
	derivedPtr := vm.PointerTo(valueT)
	ptrT := &vm.Type{
		Name:     valueT.Name,
		ElemType: valueT,
		Rtype:    derivedPtr.Rtype,
		Methods:  valueT.Methods,
	}
	if !ptrT.IsPtr() || ptrT.Name == "" {
		t.Fatalf("constructed *T: name=%q isPtr=%v, want a named pointer type", ptrT.Name, ptrT.IsPtr())
	}
	i.Symbols["*T"] = &symbol.Symbol{Kind: symbol.Type, Type: ptrT}

	// Re-run the attach walk, now including *T. Without the redirect it errors.
	i.synthAttached = nil
	if err := i.attachSynthMethods(); err != nil {
		t.Fatalf("attachSynthMethods over a method-bearing *T symbol: %v", err)
	}
}
