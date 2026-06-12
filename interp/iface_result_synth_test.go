package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// An interface method returning an interface, dispatched on a value stored in
// a native slice element (make([]Iface, n) + IndexSet): the call used to go
// through the synth-attached stub, whose result marshaling dropped the
// interpreted concrete (it does not implement the synth iface rtype) to nil
// (gonum/mat HOGSVD: d.T() returned nil, then x.Dims() nil-deref'd). IfaceCall
// now dispatches synth-rtype receivers through the interpreter directly.
func TestIfaceResultThroughNativeSliceElem(t *testing.T) {
	src := `package main

import "fmt"

type Matrix interface {
	Dims() (int, int)
	T() Matrix
}

type Dense struct{ r, c int }

func (d *Dense) Dims() (int, int) { return d.r, d.c }
func (d *Dense) T() Matrix        { return Transpose{d} }

type Transpose struct{ M Matrix }

func (t Transpose) Dims() (int, int) { r, c := t.M.Dims(); return c, r }
func (t Transpose) T() Matrix        { return t.M }

func main() {
	data := make([]Matrix, 2)
	for i := range data {
		data[i] = &Dense{3, 2}
	}
	for _, d := range data {
		r, c := d.T().Dims()
		fmt.Println(r, c)
	}
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "2 3\n2 3\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
