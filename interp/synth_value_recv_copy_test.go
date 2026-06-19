package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A value-receiver method invoked through the native synth bridge (here native
// fmt calling fmt.Formatter.Format) got the boxed interface value aliased as its
// receiver, not a copy: makeRecvValue's recvDeref form returned NewAt(rtype,
// recv).Elem() over the caller's storage. A field write in the body then leaked
// back into the interface, so a second call saw the mutation. Go value-receiver
// semantics require a copy. (gonum/mat TestFormat: formatter.Format sets the nil
// f.format field on the first %v, breaking the later %#v Go-syntax branch.)
func TestSynthValueRecvCopy(t *testing.T) {
	src := `package main

import "fmt"

// Two fields keep T off the direct-iface fast path, so the synth bridge takes
// the recvDeref form (the interface word is an address into boxed storage).
type T struct {
	tag  string
	hook func()
}

func (v T) Format(f fmt.State, c rune) {
	if v.hook == nil {
		fmt.Fprint(f, "NIL")
		v.hook = func() {} // value receiver: must not leak to the caller
		return
	}
	fmt.Fprint(f, "SET")
}

func main() {
	holder := struct{ m fmt.Formatter }{m: T{tag: "x"}}
	a := fmt.Sprintf("%v", holder.m)
	b := fmt.Sprintf("%v", holder.m)
	fmt.Println(a, b)
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "NIL NIL\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
