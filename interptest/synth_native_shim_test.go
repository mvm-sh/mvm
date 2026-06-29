package interptest

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A synth (interpreted) value implementing error/Stringer must render correctly
// when it crosses into a NATIVE variadic ...any sink that formats it. On the
// shared-PC (wasm) build the synth method PC is the -1 unreachable sentinel, so
// vm wraps it in a native forwarding shim (wrapSynthIfaceForNative); native
// dispatches the real method via stub pools. The native sink here (a bridged
// fmt.Sprintf) stands in for testing.Logf, the suite-level trigger. Both the
// direct-arg (bridgeArgs) and spread-slice (unwrapVariadicIface) paths are
// exercised. Runs on the wasm CI (TestSynth* prefix).
func TestSynthErrorThroughNativeVariadic(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"render"
)

type myErr struct{ s string }

func (e *myErr) Error() string { return e.s }

type myStr struct{ v string }

func (s myStr) String() string { return "S:" + s.v }

func main() {
	var e error = &myErr{"boom"}
	s := myStr{"hi"}
	fmt.Print(render.Sprintf("[%v|%v]", e, s)) // direct args
	args := []any{e, s}
	fmt.Print(render.Sprintf("[%v|%v]", args...)) // spread slice
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"render": {"Sprintf": reflect.ValueOf(fmt.Sprintf)},
	})
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("shim_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "[boom|S:hi][boom|S:hi]"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}
