package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/vm/synth"
)

func TestSynthStringerEndToEnd(t *testing.T) {
	t.Setenv("MVM_SYNTH", "1")

	const src = `package main

import "fmt"

type Greeter struct {
	Name string
}

func (g Greeter) String() string { return "hello " + g.Name }

func main() {
	var s fmt.Stringer = Greeter{Name: "world"}
	fmt.Print(s.String())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("synth_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "hello world"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthPtrStringerEndToEnd is the pointer-receiver counterpart of
// TestSynthStringerEndToEnd: Phase 2a synthesizes a *T rtype via
// attachPtrType and wires PtrToThis so &T satisfies fmt.Stringer.
func TestSynthPtrStringerEndToEnd(t *testing.T) {
	t.Setenv("MVM_SYNTH", "1")

	const src = `package main

import "fmt"

type Counter struct {
	N int
}

func (c *Counter) String() string { return fmt.Sprintf("count=%d", c.N) }

func main() {
	c := &Counter{N: 7}
	var s fmt.Stringer = c
	fmt.Print(s.String())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("synth_ptr_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "count=7"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthAttachIdempotent verifies that a single Eval consumes the
// expected number of S1 slots: one per distinct synth-attached *Type.
// The compiler aliases each Type symbol under bare and pkg-qualified keys
// (compiler.go:136), so without per-*Type dedup the walker would attach the
// same type twice, doubling slot consumption.
func TestSynthAttachIdempotent(t *testing.T) {
	t.Setenv("MVM_SYNTH", "1")

	const src = `package main

import "fmt"

type T struct{ N int }

func (t T) String() string { return fmt.Sprintf("n=%d", t.N) }

func main() {
	var s fmt.Stringer = T{N: 3}
	fmt.Print(s.String())
}
`
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	var stdout, stderr bytes.Buffer
	i.SetIO(os.Stdin, &stdout, &stderr)

	before := synth.SlotsUsedS1()
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	after := synth.SlotsUsedS1()
	if got, want := after-before, uint32(1); got != want {
		t.Errorf("SlotsUsedS1 delta = %d, want %d (alias dedup broken)", got, want)
	}
}
