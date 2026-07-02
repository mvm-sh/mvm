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

// The reverse of TestSynthIoEOFCanon: an interpreted reader's io.EOF must canonicalize to host io.EOF when a native sink (bytes.Buffer.ReadFrom via io.Copy) consumes it.
// Without the eface unwrap in mapInterpSentinel it leaked out as an error (io.Pipe + io.Copy(os.Stdout, r) returned EOF on wasm).
// TestSynth* runs on the wasm CI.
func TestSynthInterpEOFToNativeReadFrom(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"io"
	"nat"
)

type onceReader struct{ done bool }

func (r *onceReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, []byte("hello, sink")), nil
}

func main() {
	n, err := io.Copy(nat.Sink(), &onceReader{})
	fmt.Printf("n=%d err=%v got=%q\n", n, err, nat.Got())
}
`
	var stdout, stderr bytes.Buffer
	var sink bytes.Buffer // native *bytes.Buffer has ReadFrom, so io.Copy uses the native ReadFrom path
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SkipBridges("io") // force-interpret io so the interp-EOF -> native-sink split exists on native
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"nat": {
			"Sink": reflect.ValueOf(func() io.Writer { return &sink }),
			"Got":  reflect.ValueOf(func() string { return sink.String() }),
		},
	})
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("interp_eof_sink.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "n=11 err=<nil> got=\"hello, sink\"\n"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// An interpreted oserror sentinel (io/fs.ErrNotExist == oserror.ErrNotExist)
// must reconcile with the host value in both directions: bridgeArgs maps a bare
// sentinel arg to native (a nested one is left alone), and canonNativeReturns
// maps a host sentinel returned by a native call back to the interpreted copy.
// TestSynth* runs on the wasm CI.
func TestSynthOserrorSentinelArg(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"io/fs"
	"nat"
)

func main() {
	fmt.Println(nat.EqNotExist(fs.ErrNotExist), nat.EqExist(fs.ErrExist), nat.EqNotExist(fs.ErrExist))
	fmt.Println(nat.RetNotExist() == fs.ErrNotExist)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SkipBridges("io", "io/fs", "errors") // force-interpret so the sentinel split exists on native
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"nat": {
			"EqNotExist":  reflect.ValueOf(func(err error) bool { return err == os.ErrNotExist }),
			"EqExist":     reflect.ValueOf(func(err error) bool { return err == os.ErrExist }),
			"RetNotExist": reflect.ValueOf(func() error { return os.ErrNotExist }),
		},
	})
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("oserr_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "true true false\ntrue\n"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// RegisterNativeIdentity: native *fs.PathError (os.Open) and the interpreted io/fs
// mirror's PathError share one rtype, so errors.As / TypeOf reconcile.
// Fails "false/false" without it.
// TestSynth* runs on the wasm CI.
func TestSynthNativeIdentityPathError(t *testing.T) {
	const src = `package main

import (
	"errors"
	"fmt"
	"io/fs"
	"nat"
	"reflect"
)

func main() {
	err := nat.OpenMissing()
	var pe *fs.PathError
	ok := errors.As(err, &pe)
	fmt.Println(ok, ok && pe != nil)
	fmt.Println(reflect.TypeOf(err) == reflect.TypeOf(&fs.PathError{}))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SkipBridges("io", "io/fs", "errors") // force-interpret so the split exists on native
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"nat": {
			"OpenMissing": reflect.ValueOf(func() error {
				_, err := os.Open("/no/such/file/mvm-test")
				return err
			}),
		},
	})
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("pe_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "true true\ntrue\n"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// Interpreted errors.As into an anon-iface target interface{Timeout()bool}.
// reflectliteValueOf retyping gives a precise targetType (no over-match of a plain
// error, line 1); the Set hook keeps the matched native *fs.PathError eface-form so
// it writes/reads back (line 2).
// TestSynth* runs on the wasm CI.
func TestSynthAnonIfaceAsTarget(t *testing.T) {
	const src = `package main

import (
	"errors"
	"fmt"
	"nat"
)

type wrap struct{ err error }

func (w wrap) Error() string { return "wrap: " + w.err.Error() }
func (w wrap) Unwrap() error { return w.err }

func main() {
	var timeout interface{ Timeout() bool }
	fmt.Println(errors.As(errors.New("plain"), &timeout)) // no Timeout: must be false

	timeout = nil
	ok := errors.As(wrap{nat.OpenMissing()}, &timeout) // *fs.PathError has Timeout
	fmt.Println(ok, ok && timeout != nil)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SkipBridges("io", "io/fs", "errors") // force-interpret so the split exists on native
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"nat": {
			"OpenMissing": reflect.ValueOf(func() error {
				_, err := os.Open("/no/such/file/mvm-test")
				return err
			}),
		},
	})
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("anon_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "false\ntrue true\n"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// Interpreted fmt.pp/fmt.ss handed to a native Formatter/Scanner (big.Int, big.Rat).
// On wasm this needs the State/ScanState shims; natively, Token's ip_piipp pool.
// TestSynth* runs on the wasm CI.
func TestSynthFmtStateScanState(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"math/big"
)

func main() {
	fmt.Printf("%+d %#x %6.2s\n", big.NewInt(42), big.NewInt(255), big.NewInt(7))
	var r big.Rat
	n, err := fmt.Sscan("3/4", &r)
	fmt.Println(n, err, r.String())
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SkipBridges("fmt")
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("fmtstate_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "+42 0xff     07\n1 <nil> 3/4\n"
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
