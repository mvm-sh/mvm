package interptest

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A synth rtype's method table must hold more than the old 16-method cap, so a
// reflect-driven test suite (grpctest.RunSubTests / testify) enumerating every
// Test* method via reflect.Type.Method does not silently lose methods past 16.
func TestSynthMethodCapAboveSixteen(t *testing.T) {
	const src = `package main

import "reflect"

type S struct{}

func (S) M00() {}
func (S) M01() {}
func (S) M02() {}
func (S) M03() {}
func (S) M04() {}
func (S) M05() {}
func (S) M06() {}
func (S) M07() {}
func (S) M08() {}
func (S) M09() {}
func (S) M10() {}
func (S) M11() {}
func (S) M12() {}
func (S) M13() {}
func (S) M14() {}
func (S) M15() {}
func (S) M16() {}
func (S) M17() {}
func (S) M18() {}
func (S) M19() {}

func main() {
	println(reflect.TypeOf(S{}).NumMethod())
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "20\n"; got != want {
		t.Errorf("NumMethod: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

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
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "NIL NIL\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
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
	if got := evalOut(t, "chan_recv.go", src); got != want {
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
	if got := evalOut(t, "promoted_order.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// Method.Func calls on indirect value receivers (string, named scalar,
// multi-word struct) misread the by-value receiver through the one-word synth
// stubs (go-cmp's callTTBFunc); the Call shim converts them to bound-method
// calls, while direct-iface receivers (e.g. via the pointer type) stay native.
func TestMethodFuncValueReceiver(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type S string

func (s S) Equal(o S) bool { return s == o }

type N int

func (n N) Double() int { return int(n) * 2 }

type P struct{ a, b int }

func (p P) Sum() int { return p.a + p.b }

func main() {
	m, _ := reflect.TypeOf(S("a")).MethodByName("Equal")
	out := m.Func.Call([]reflect.Value{reflect.ValueOf(S("foo")), reflect.ValueOf(S("foo"))})
	ne := m.Func.Call([]reflect.Value{reflect.ValueOf(S("foo")), reflect.ValueOf(S("bar"))})
	fmt.Println(out[0].Bool(), ne[0].Bool())

	m2, _ := reflect.TypeOf(N(21)).MethodByName("Double")
	out2 := m2.Func.Call([]reflect.Value{reflect.ValueOf(N(21))})
	fmt.Println(out2[0].Int())

	s := S("foo")
	mp, _ := reflect.TypeOf(&s).MethodByName("Equal")
	out3 := mp.Func.Call([]reflect.Value{reflect.ValueOf(&s), reflect.ValueOf(S("foo"))})
	fmt.Println(out3[0].Bool())

	m4, _ := reflect.TypeOf(P{}).MethodByName("Sum")
	out4 := m4.Func.Call([]reflect.Value{reflect.ValueOf(P{a: 3, b: 4})})
	fmt.Println(out4[0].Int())
}
`
	want := "true false\n42\ntrue\n7\n"
	if got := evalOut(t, "method_func_value_recv.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
