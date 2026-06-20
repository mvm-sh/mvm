package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// runProg evaluates src and returns its stdout, failing on any eval error.
func runProg(t *testing.T, name, src string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval(name, src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String()
}

// new(reflect.Value) reflected as **reflect.Value: the shim pre-seeds the
// "reflect.Value" symbol, and the compiler's Period handler filled its Data
// slot with the raw (*reflect.Value)(nil) native value instead of the zero
// reflect.Value, so PtrNew built a pointer to a pointer. (go-spew
// TestInvalidReflectValue.)
func TestNewReflectValuePointerType(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

func main() {
	v := new(reflect.Value)
	fmt.Printf("%T\n", v)
}
`
	if got, want := runProg(t, "rv.go", src), "*reflect.Value\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A map literal/assignment of an untyped constant value into a named-string
// element type left the value as a plain string, which SetMapIndex rejected.
// MapSet now adopts the named element type like the key path. (go-spew
// TestFormatter map[pstringer]pstringer{"one": "1"}.)
func TestMapNamedStringElemConvert(t *testing.T) {
	src := `package main

import "fmt"

type pstringer string

func main() {
	m := map[string]pstringer{"a": "x"}
	m["b"] = "y"
	fmt.Println(m["a"], m["b"])
}
`
	if got, want := runProg(t, "mapelem.go", src), "x y\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// complex() of a typed float32 constant must yield complex64, not complex128:
// the deref helper widened any typed numeric const to float64. (go-spew
// complex64 dump/format tests.)
func TestComplexTypedFloat32Const(t *testing.T) {
	src := `package main

import "fmt"

func main() {
	a := complex(float32(6), -2)
	b := complex(float64(6), -2)
	fmt.Printf("%T %T\n", a, b)
}
`
	if got, want := runProg(t, "cx.go", src), "complex64 complex128\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
