package interptest

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
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

// A native reader's io.EOF must satisfy interpreted io's `err == io.EOF`.
// SkipBridges forces interpreted io so the split exists on native too.
// TestSynth* runs on the wasm CI.
func TestSynthIoEOFCanon(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"io"
	"nat"
)

func main() {
	buf := make([]byte, 64)
	var got []byte
	r := nat.Reader()
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			fmt.Printf("got=%q eofMatch=%v\n", string(got), err == io.EOF)
			break
		}
	}
	all, err := io.ReadAll(nat.Reader())
	fmt.Printf("readall=%q err=%v\n", string(all), err)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SkipBridges("io") // force-interpret io so the sentinel split exists on native too
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"nat": {"Reader": reflect.ValueOf(func() io.Reader { return strings.NewReader("hello, eof") })},
	})
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("eof_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "got=\"hello, eof\" eofMatch=true\nreadall=\"hello, eof\" err=<nil>\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", stdout.String(), want, stderr.String())
	}
}

// An interpreted io.Reader read by a native sink: on wasm its Read PC is the -1
// sentinel, so without the reader shim native io.ReadAll would trap.
// Asserts the buffer round-trips and the interpreted io.EOF terminates the copy.
// TestSynth* runs on the wasm CI.
func TestSynthIoReaderThroughNativeSink(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"io"
	"nat"
)

type myReader struct {
	data []byte
	pos  int
}

func (r *myReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func main() {
	s, err := nat.ReadAll(&myReader{data: []byte("hello, world")})
	fmt.Printf("got=%q err=%v\n", s, err)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"nat": {"ReadAll": reflect.ValueOf(func(r io.Reader) (string, error) {
			b, err := io.ReadAll(r)
			return string(b), err
		})},
	})
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("reader_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "got=\"hello, world\" err=<nil>\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", stdout.String(), want, stderr.String())
	}
}
