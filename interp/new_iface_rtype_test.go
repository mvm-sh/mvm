package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// new(io.Reader) emitted PtrNew against the type's zero-VALUE slot, which
// NewValue collapses to interface{} for interface/func kinds (heterogeneous
// var storage). The pointer thus reflected as *interface{} with no methods,
// so reflect.TypeOf(new(io.Reader)).Elem().Implements(...) was always true
// (TestImplements/TestAssignableTo). new now builds the pointer from the
// precise type descriptor, keeping the declared element type and method set.
func TestNewInterfaceRtypeKeepsMethods(t *testing.T) {
	src := `package main

import (
	"fmt"
	"io"
	"reflect"
)

func main() {
	e := reflect.TypeOf(new(io.Reader)).Elem()
	fmt.Println(e.String(), e.NumMethod())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("newiface.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "io.Reader 1\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
