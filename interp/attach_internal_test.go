package interp

// White-box tests for the synth method-attach walk (attachSynthMethods). They
// poke unexported state (synthAttached, hand-built *Type symbols) to exercise
// reservation edge cases that cannot be reached through the public Eval API, so
// they stay in package interp rather than moving to interptest.

import (
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/derive"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/symbol"
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
	derivedPtr := derive.PointerTo(valueT)
	ptrT := &mtype.Type{
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

// A monomorphized generic instance (name contains '#') can reach the synth attach
// walk with a filled ptr-method table but NO reservation: it materialized before
// the template propagated its methods, so it took the methodless shortcut at the
// reserve gate. Its rtype is a plain unreserved struct with no method slots, so the
// attach must skip rather than abort ("synth: ptr-method ... has no pointer
// reservation"). This is grpc's log.Logger prefix field atomic.Pointer[string].
// A NON-generic unreserved type with methods is still an error (gate invariant).
func TestAttachGenericInstanceNoReservationSkips(t *testing.T) {
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	src := `
		type Box struct{ X int }
		func (b *Box) String() string { return "box" }
		var _ = Box{}.X
	`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	box := i.Symbols["Box"].Type

	// A generic instance "Box#int": a fresh value *Type (not in the reservation
	// registry) carrying Box's filled ptr-method table over Box's struct rtype.
	genInst := &mtype.Type{Name: "Box#int", Rtype: box.Rtype, Methods: box.Methods}
	if genInst.IsPtr() || !strings.Contains(genInst.Name, "#") {
		t.Fatalf("setup: want a named-value generic instance, got isPtr=%v name=%q", genInst.IsPtr(), genInst.Name)
	}
	i.Symbols["Box#int"] = &symbol.Symbol{Kind: symbol.Type, Type: genInst}

	i.synthAttached = nil
	if err := i.attachSynthMethods(); err != nil {
		t.Fatalf("attachSynthMethods over an unreserved generic instance: %v", err)
	}

	// Guard preserved: a NON-generic unreserved type with methods still errors.
	j := NewInterpreter(golang.GoSpec)
	j.ImportPackageValues(stdlib.Values)
	if _, err := j.Eval("test", src); err != nil {
		t.Fatalf("Eval2: %v", err)
	}
	box2 := j.Symbols["Box"].Type
	plain := &mtype.Type{Name: "Boxish", Rtype: box2.Rtype, Methods: box2.Methods}
	j.Symbols["Boxish"] = &symbol.Symbol{Kind: symbol.Type, Type: plain}
	j.synthAttached = nil
	if err := j.attachSynthMethods(); err == nil {
		t.Fatal("want error for a non-generic unreserved method-bearing type, got nil")
	}
}
