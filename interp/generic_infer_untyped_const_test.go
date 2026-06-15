package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Regression for `mvm test github.com/microcosm-cc/bluemonday` ->
// golang.org/x/net/html/escape.go `slices.Index(b, '&')`:
// "type []uint8 does not satisfy constraint".
//
// Go infers generic type params from typed args and constraints first, using an
// untyped constant's default type only as a fallback. mvm bound E from '&'
// (default rune) in the first pass, shadowing E=byte from the S ~[]E constraint,
// so the [S ~[]E] check then saw []uint8 ~ []int32 and failed. Untyped-const
// args are now deferred to a fallback pass after constraint inference.
func TestGenericInferUntypedConstDeferred(t *testing.T) {
	src := `package main

import "fmt"

func Index[S ~[]E, E comparable](s S, v E) int {
	for i := range s {
		if s[i] == v {
			return i
		}
	}
	return -1
}

func First[T any](x T) T { return x }

func main() {
	b := []byte("a&b")
	fmt.Println(Index(b, '&')) // E=byte from S, not rune from '&'
	i8 := []int8{1, 2, 3}
	fmt.Println(Index(i8, 2)) // E=int8 from S, not int from 2
	fmt.Println(First(5))     // fallback: T=int from the untyped const
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("infer_untyped_const.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "1\n1\n5\n"; got != want {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}
