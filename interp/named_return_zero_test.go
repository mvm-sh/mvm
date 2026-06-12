package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Regression for `mvm test golang.org/x/text/language` -> ExampleMatcher.
//
// Named-return slots were zero-initialized only for struct/array/slice/map
// kinds; an unassigned scalar/string/interface named return left the slot an
// invalid Value{}, which crashed when the caller boxed the result into an
// interface (fmt.Println variadic pack): "reflect: Set on zero Value".
func TestNamedReturnUnassignedZero(t *testing.T) {
	src := `package main

import "fmt"

func bareInt() (i int)       { return }
func explInt() (i int)       { return i }
func bareStr() (s string)    { return }
func bareErr() (err error)   { return }
func bareMulti() (t string, index int, b byte) { return t, index, b }

func main() {
	fmt.Println(bareInt(), explInt(), bareStr(), bareErr())
	fmt.Println(bareMulti())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("named_return_zero.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "0 0  <nil>\n 0 0\n"
	if got := stdout.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
