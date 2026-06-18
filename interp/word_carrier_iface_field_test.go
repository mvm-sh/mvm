package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A named word-carrier scalar (type NInt int) boxed into an interface that is
// stored in a struct field, then dispatched via a value-receiver method, must
// pass the real value as the receiver. The re-wrap of the field's non-addressable
// numeric concrete built the Iface.Val with data in ref but a stale num=0; the
// method body reads num, so the receiver came through as the zero value.
func TestWordCarrierIfaceFieldReceiver(t *testing.T) {
	const src = `package main

import "fmt"

type I interface{ V() string }

type NInt int
func (n NInt) V() string { return fmt.Sprintf("%d", int(n)) }

type NFloat float64
func (f NFloat) V() string { return fmt.Sprintf("%g", float64(f)) }

type box struct{ i I }

func main() {
	b := box{i: NInt(5)}             // composite-literal field
	fmt.Println(b.i.V())             // want 5

	var b2 box
	b2.i = NInt(7)                   // field assignment
	fmt.Println(b2.i.V())            // want 7

	var iv I = NInt(9)
	b3 := box{i: iv}                 // already-boxed iface into field
	fmt.Println(b3.i.V())            // want 9

	fmt.Println(box{i: NFloat(2.5)}.i.V()) // want 2.5
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "5\n7\n9\n2.5\n"; got != want {
		t.Errorf("output: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}
