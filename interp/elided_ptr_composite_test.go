package interp

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Regression for `mvm test github.com/samber/lo` -> x/text/unicode/norm
// "undefined: info".
//
// An elided `{...}` element of a []*T literal denotes &T{...}. The BraceBlock
// inference registered the composite under the pointer element type *T instead
// of the pointee T. Because a SymPtr carries the pointee's name (it doubles as
// the embedded-field name), *T's String() rendered as "T", so registerType
// re-keyed the *T type under "T" and OVERWROTE the real T struct symbol with a
// fieldless pointer type. A later field access on T (here through a method on
// *T) then failed "undefined: <field>". Fixed by derefing the pointer element
// so the composite type is the pointee T, which the compiler auto-addresses.
func TestElidedPointerSliceComposite(t *testing.T) {
	const src = `package main

import "fmt"

type formInfo struct {
	form int
	info func(int) int
}

func lookup(i int) int { return i + 1 }

var formTable = []*formInfo{{form: 1, info: lookup}}

func (f *formInfo) run(i int) int {
	info := f.info(i)
	return f.form + info
}

func main() {
	fmt.Print(formTable[0].run(5))
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("repro.go", src); err != nil {
		t.Fatalf("eval: %v\nstderr: %s", err, stderr.String())
	}
	// formTable[0].run(5) = form(1) + info(lookup(5)=6) = 7.
	if got := strings.TrimSpace(stdout.String()); got != "7" {
		t.Errorf("got %q, want %q (stderr: %s)", got, "7", stderr.String())
	}
}
