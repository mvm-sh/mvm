package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A struct field of pointer type (`mat *BandDense`) is parsed as a clone of
// the *BandDense mtype carrying the FIELD name. When the pointer type also
// carries methods (registerMethods filled them for an interface check), the
// reserve gate minted a named rtype from the field name: reflect.TypeOf on a
// field select reported "main.mat" and DeepEqual against a plain *BandDense
// failed (gonum/mat TestNewBand and friends). A non-defined clone now keeps
// the base layout.
func TestFieldClonePtrKeepsBaseIdentity(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type Matrix interface {
	Dims() (int, int)
}

type Band struct{ Rows int }

type BandDense struct{ mat Band }

func (m *BandDense) Dims() (int, int) { return m.mat.Rows, m.mat.Rows }

var _ Matrix = (*BandDense)(nil)

func main() {
	tests := []struct {
		mat *BandDense
	}{
		{mat: &BandDense{mat: Band{1}}},
	}
	b := &BandDense{mat: Band{1}}
	fmt.Println(reflect.TypeOf(tests[0].mat))
	fmt.Println(reflect.TypeOf(tests[0].mat) == reflect.TypeOf(b))
	fmt.Println(reflect.DeepEqual(b, tests[0].mat))
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "*main.BandDense\ntrue\ntrue\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
