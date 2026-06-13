package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A single-method interface whose method returns a FORWARD-declared interface:
// `x := eds.Get(0); x.Name()` inferred x's type from the reflect-materialized
// method signature, whose result was erased to bare interface{} whenever Item
// had not yet materialized to a named reflect interface. Materialization order
// is map-dependent, so "undefined: Name" appeared on most (not all) compiles.
// resolveIfaceMethodSym now uses the interpreted symbolic Sig, which keeps
// Item's method set regardless of materialization order.
//
// No concrete implementation of Name exists, so findConcreteFuncSym cannot
// rescue the call -- the result type must carry the method set itself.
func TestIfaceForwardResultMethodSet(t *testing.T) {
	src := `package main

type List interface {
	Get(i int) Item
}

type Item interface {
	Name() string
}

func use(eds List) string {
	if eds == nil {
		return ""
	}
	x := eds.Get(0)
	return x.Name()
}

func main() {
	_ = use
}
`
	// Forward-ref materialization order is map-dependent; run repeatedly with
	// fresh interpreters so a regression (undefined: Name) surfaces reliably.
	for k := range 16 {
		var stdout, stderr bytes.Buffer
		i := NewInterpreter(golang.GoSpec)
		i.ImportPackageValues(stdlib.Values)
		i.SetIO(os.Stdin, &stdout, &stderr)
		if _, err := i.Eval("test", src); err != nil {
			t.Fatalf("iter %d Eval: %v", k, err)
		}
	}
}
