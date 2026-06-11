package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

func evalProgram(t *testing.T, name, src string) string {
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

// A named chan type can carry methods (go-cmp's AssignC/AssignD); chan was
// missing from runtype.SupportedKind, so the method never attached.
func TestChanRecvSynthMethod(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type C chan bool

func (c C) Equal(d chan bool) bool { return true }

func main() {
	c := C(make(chan bool))
	v := reflect.ValueOf(c)
	m, ok := v.Type().MethodByName("Equal")
	fmt.Println(v.NumMethod(), ok)
	out := v.Method(m.Index).Call([]reflect.Value{reflect.ValueOf(make(chan bool))})
	fmt.Println(out[0].Bool())
}
`
	want := "1 true\ntrue\n"
	if got := evalProgram(t, "chan_recv.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// Promoted methods are collected from the embedded type's native method table,
// which fills only at the embed's own attach; the symbol walk is map-ordered,
// so attach must order embeds before embedders (Interp.attachWithEmbeds).
// Several sibling embedders make a wrong order very likely to surface.
func TestPromotedAttachOrder(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type I interface{ M() }

type B struct{ X string }

func (b *B) Equal(y I) bool { return true }
func (b *B) M()             {}

type (
	E1 struct {
		*B
		X string
	}
	E2 struct {
		*B
		X string
	}
	E3 struct {
		*B
		X string
	}
	E4 struct {
		*B
		X string
	}
	E5 struct {
		*B
		X string
	}
)

func main() {
	for _, v := range []any{E1{}, E2{}, E3{}, E4{}, E5{}} {
		fmt.Print(reflect.TypeOf(v).NumMethod(), " ")
	}
	fmt.Println()
}
`
	want := "2 2 2 2 2 \n"
	if got := evalProgram(t, "promoted_order.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
