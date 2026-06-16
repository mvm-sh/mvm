package interp

import (
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/vm"
)

// mvm test's drop-retry loop recompiles a test unit several times on one interp,
// minting a fresh *Type for each declared method-bearing type each pass. Those
// passes must converge on ONE rtype within a Machine: a value captured in one
// pass (e.g. reflect.TypeOf((*T)(nil)) stored in a native MessageInfo
// .GoReflectType) otherwise no longer == a value built in a later pass, which
// crashed proto's noenforceutf8 MessageOf "type mismatch: got *T, want *T".
//
// This mirrors the second pass: a fresh *Type for the same Go type (same name,
// layout, method set) is materialized under the same Machine and must adopt the
// first pass's reserved rtype rather than reserving a distinct one.
func TestMethodBearingRtypeSharedWithinMachine(t *testing.T) {
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	const src = `
		type T struct {
			A string
			B []byte
		}
		func (m *T) M() string { return m.A }
	`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	sym, ok := i.Symbols["T"]
	if !ok || sym.Type == nil || sym.Type.Rtype == nil {
		t.Fatal("materialized type T not found")
	}
	t1 := sym.Type

	// A second pass's fresh, still-symbolic *Type for the same Go type: same
	// name/layout/methods, no rtype yet.
	t2v := *t1
	t2 := &t2v
	t2.Rtype = nil

	prev := vm.SetActiveMachine(i.Machine)
	defer vm.SetActiveMachine(prev)
	rt2 := vm.MaterializeRtype(t2)
	if rt2 == nil {
		t.Fatal("second-pass materialize returned nil")
	}
	if rt2 != t1.Rtype {
		t.Fatalf("method-bearing type T got a distinct rtype on the second pass: %p vs %p (cross-Eval dup)", rt2, t1.Rtype)
	}
}
