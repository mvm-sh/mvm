package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Taking the address of a function parameter must yield a pointer independent
// of the caller's argument variable: a Go parameter is a copy. detachByValueArgs
// only detached struct/array args, so a slice (or other reference-kind) param's
// &param aliased the caller's slot. Reassigning the caller's variable then
// mutated an already-escaped &param -- this corrupted sync.Pool workspaces in
// gonum/mat (putFloat64s(&w) keeps a stale header after Inverse reslices work).
func TestParamAddrDetach(t *testing.T) {
	src := `package main

import "fmt"

var saved *[]float64

func store(w []float64) { saved = &w } // address of the parameter

func main() {
	x := make([]float64, 10, 64)
	store(x)
	before := cap(*saved)
	x = make([]float64, 20, 128) // reassign the caller's variable
	_ = x
	after := cap(*saved)
	fmt.Println(before, after)
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "64 64\n"; got != want {
		t.Errorf("&param aliased caller: got %q, want %q", got, want)
	}
}

// A nil reference-kind param that is cell-boxed for &param still needs its own
// addressable cell storage, or a write through the pointer is lost vs a plain
// read (HeapAlloc only re-homed addressable values; a nil header is not
// addressable).
func TestParamAddrDetachNil(t *testing.T) {
	src := `package main

import "fmt"

func f(s []int) {
	p := &s
	*p = []int{1, 2}
	fmt.Println(s)
}

func main() { f(nil) }
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "[1 2]\n"; got != want {
		t.Errorf("write through &param lost on nil arg: got %q, want %q", got, want)
	}
}
