package interp

import (
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

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
	genInst := &vm.Type{Name: "Box#int", Rtype: box.Rtype, Methods: box.Methods}
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
	plain := &vm.Type{Name: "Boxish", Rtype: box2.Rtype, Methods: box2.Methods}
	j.Symbols["Boxish"] = &symbol.Symbol{Kind: symbol.Type, Type: plain}
	j.synthAttached = nil
	if err := j.attachSynthMethods(); err == nil {
		t.Fatal("want error for a non-generic unreserved method-bearing type, got nil")
	}
}
