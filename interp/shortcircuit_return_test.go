package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// `return a || b` (and `&&`) whose final operand is a bare local fuses the
// trailing GetLocal into GetLocalReturn, which elides the separate Return. But
// the short-circuit JumpSetTrue/JumpSetFalse target a merge label sitting at
// that elided Return: with it gone the jump fell through into the next
// function's body, cascading through the whole program's func-definition
// skip-jumps into main and re-running it (an infinite loop). Minimized from
// `mvm test google.golang.org/protobuf/reflect/protodesc` (isValidFieldNumber:
// `return MinValidNumber <= n && (n <= MaxValidNumber || isMessageSet)`).
func TestShortCircuitTailReturn(t *testing.T) {
	src := `package main

import "fmt"

func or(a, b bool) bool      { return a || b }
func and(a, b bool) bool     { return a && b }
func valid(n, mx int, m bool) bool { return 1 <= n && (n <= mx || m) }

func main() {
	fmt.Println(or(true, false), or(false, false))
	fmt.Println(and(true, true), and(true, false))
	fmt.Println(valid(5, 10, false), valid(20, 10, false), valid(20, 10, true))
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("shortcircuit_return.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "true false\ntrue false\ntrue false true\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}
