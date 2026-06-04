package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A typed const-conversion (int8(4)) passed straight to an any param used to
// box as int: it loads via immediate Push (ref collapses to int) and the int8
// lived only in Iface.Typ, which the bridge ignored. bridgeIface now rebuilds
// the numeric value from num via Iface.Typ.
func TestTypedConstArgPreservesType(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

func main() {
	fmt.Println(reflect.ValueOf(int8(4)).Type())
	fmt.Println(reflect.ValueOf(uint16(7)).Type())
	fmt.Printf("%T %T\n", int8(4), uint32(9))
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("typed_const_arg.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "int8\nuint16\nint8 uint32\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
