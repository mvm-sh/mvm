package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A synth rtype's method table must hold more than the old 16-method cap, so a
// reflect-driven test suite (grpctest.RunSubTests / testify) enumerating every
// Test* method via reflect.Type.Method does not silently lose methods past 16.
func TestSynthMethodCapAboveSixteen(t *testing.T) {
	const src = `package main

import "reflect"

type S struct{}

func (S) M00() {}
func (S) M01() {}
func (S) M02() {}
func (S) M03() {}
func (S) M04() {}
func (S) M05() {}
func (S) M06() {}
func (S) M07() {}
func (S) M08() {}
func (S) M09() {}
func (S) M10() {}
func (S) M11() {}
func (S) M12() {}
func (S) M13() {}
func (S) M14() {}
func (S) M15() {}
func (S) M16() {}
func (S) M17() {}
func (S) M18() {}
func (S) M19() {}

func main() {
	println(reflect.TypeOf(S{}).NumMethod())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "20\n"; got != want {
		t.Errorf("NumMethod: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}
