package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/stdlib/stubs"
)

func TestSynthStringerEndToEnd(t *testing.T) {
	const src = `package main

import "fmt"

type Greeter struct {
	Name string
}

func (g Greeter) String() string { return "hello " + g.Name }

func main() {
	var s fmt.Stringer = Greeter{Name: "world"}
	fmt.Print(s.String())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("synth_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "hello world"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthPtrStringerEndToEnd is the pointer-receiver counterpart of
// TestSynthStringerEndToEnd: the reserve path synthesizes a *T rtype and wires
// PtrToThis so &T satisfies fmt.Stringer.
func TestSynthPtrStringerEndToEnd(t *testing.T) {
	const src = `package main

import "fmt"

type Counter struct {
	N int
}

func (c *Counter) String() string { return fmt.Sprintf("count=%d", c.N) }

func main() {
	c := &Counter{N: 7}
	var s fmt.Stringer = c
	fmt.Print(s.String())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("synth_ptr_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "count=7"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthKindsValueRecv exercises the synth kind catalog end-to-end:
// each named non-struct kind (primitive, slice, array, map) with a value
// receiver Stringer must satisfy fmt.Stringer and dispatch through the
// synthesized rtype.
func TestSynthKindsValueRecv(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "int",
			src: `package main
import "fmt"
type Code int
func (c Code) String() string { return fmt.Sprintf("code=%d", int(c)) }
func main() { var s fmt.Stringer = Code(7); fmt.Print(s.String()) }
`,
			want: "code=7",
		},
		{
			name: "string",
			src: `package main
import "fmt"
type Path string
func (p Path) String() string { return "path:" + string(p) }
func main() { var s fmt.Stringer = Path("x"); fmt.Print(s.String()) }
`,
			want: "path:x",
		},
		{
			name: "slice",
			src: `package main
import "fmt"
type IntList []int
func (l IntList) String() string { return fmt.Sprintf("list len=%d", len(l)) }
func main() { var s fmt.Stringer = IntList{1, 2, 3}; fmt.Print(s.String()) }
`,
			want: "list len=3",
		},
		{
			name: "array",
			src: `package main
import "fmt"
type Triple [3]int
func (t Triple) String() string { return fmt.Sprintf("triple[0]=%d", t[0]) }
func main() { var s fmt.Stringer = Triple{9, 8, 7}; fmt.Print(s.String()) }
`,
			want: "triple[0]=9",
		},
		{
			name: "map",
			src: `package main
import "fmt"
type Counts map[string]int
func (c Counts) String() string { return fmt.Sprintf("counts len=%d", len(c)) }
func main() {
	c := Counts{"a": 1, "b": 2}
	var s fmt.Stringer = c
	fmt.Print(s.String())
}
`,
			want: "counts len=2",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			i := NewInterpreter(golang.GoSpec)
			i.ImportPackageValues(stdlib.Values)
			var stdout, stderr bytes.Buffer
			i.SetIO(os.Stdin, &stdout, &stderr)
			if _, err := i.Eval(c.name+".go", c.src); err != nil {
				t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
			}
			if got := stdout.String(); got != c.want {
				t.Errorf("stdout = %q, want %q\nstderr: %s",
					got, c.want, stderr.String())
			}
		})
	}
}

// TestSynthMarshalJSON exercises shape S2 end-to-end: interpreted
// MarshalJSON on a struct value type satisfies json.Marshaler via the
// synthesized rtype, with no bridge proxy.
func TestSynthMarshalJSON(t *testing.T) {
	const src = `package main

import (
	"encoding/json"
	"fmt"
)

type Pair struct{ K, V int }

func (p Pair) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("[%d,%d]", p.K, p.V)), nil
}

func main() {
	p := Pair{K: 1, V: 2}
	b, err := json.Marshal(p)
	if err != nil { fmt.Print("ERR ", err); return }
	fmt.Print(string(b))
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	before := stubs.SlotsUsedS2()
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "[1,2]"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
	if got := stubs.SlotsUsedS2(); got <= before {
		t.Errorf("SlotsUsedS2 did not advance (before=%d after=%d); "+
			"synth S2 path was not exercised", before, got)
	}
}

// TestSynthUnmarshalJSON exercises shape S3 end-to-end: interpreted
// UnmarshalJSON on a *T satisfies json.Unmarshaler via the synthesized
// *T rtype, with mutations to the receiver visible after dispatch returns.
func TestSynthUnmarshalJSON(t *testing.T) {
	const src = `package main

import (
	"encoding/json"
	"fmt"
)

type Tagged struct{ X int }

func (t *Tagged) UnmarshalJSON(data []byte) error {
	t.X = len(data)
	return nil
}

func main() {
	var v Tagged
	if err := json.Unmarshal([]byte("[1,2,3,4]"), &v); err != nil {
		fmt.Print("ERR ", err); return
	}
	fmt.Print(v.X)
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	before := stubs.SlotsUsedS3()
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "9"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
	if got := stubs.SlotsUsedS3(); got <= before {
		t.Errorf("SlotsUsedS3 did not advance (before=%d after=%d); "+
			"synth S3 path was not exercised", before, got)
	}
}

// TestSynthMultiMethod verifies that a type with BOTH a value-recv
// Stringer AND a value-recv MarshalJSON gets both installed on the synth
// rtype, so it satisfies both fmt.Stringer AND json.Marshaler natively.
// Pre-Phase-2d this was impossible: the synth1 container held one method, so
// the priority fix (S1 > S2) gave only Stringer and Marshaler fell through
// to the bridge.
func TestSynthMultiMethod(t *testing.T) {
	const src = `package main

import (
	"encoding/json"
	"fmt"
)

type T struct{ N int }

func (t T) String() string               { return fmt.Sprintf("S%d", t.N) }
func (t T) MarshalJSON() ([]byte, error) { return []byte(fmt.Sprintf("[%d]", t.N)), nil }

func main() {
	v := T{N: 7}
	var s fmt.Stringer = v
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Print("ERR ", err)
		return
	}
	fmt.Print(s.String(), " ", string(b))
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "S7 [7]"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthCompositeInterfaceReverseDecl pins the alphabetical-sort fix:
// when a type defines methods in REVERSE alphabetical order, the synth
// rtype must still satisfy a composite interface that requires both.
// Go's reflect.implements does a forward linear merge of two pre-sorted
// method arrays, so unsorted entries silently fail multi-method
// satisfaction (and the negative result is cached in the itab).
func TestSynthCompositeInterfaceReverseDecl(t *testing.T) {
	// Methods declared in REVERSE alphabetical order (String first, then
	// MarshalJSON). Pre-fix, the synth rtype's method array preserved
	// declaration order [String, MarshalJSON] and the composite
	// interface assertion returned false.
	const src = `package main

import (
	"encoding/json"
	"fmt"
)

type T struct{ N int }

func (t T) String() string               { return fmt.Sprintf("S%d", t.N) }
func (t T) MarshalJSON() ([]byte, error) { return []byte(fmt.Sprintf("[%d]", t.N)), nil }

func main() {
	v := T{N: 9}
	if _, ok := any(v).(interface {
		fmt.Stringer
		json.Marshaler
	}); !ok {
		fmt.Print("composite assertion failed")
		return
	}
	fmt.Print("ok")
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "ok"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthAttachIdempotent verifies that a single Eval consumes the
// expected number of S1 slots: one for the value type T and one for the
// synthesized *T, which carries T's value-receiver methods (Go's rule that
// method-set(*T) includes value methods, so *T satisfies fmt.Stringer too).
// The compiler aliases each Type symbol under bare and pkg-qualified keys
// (compiler.go:136), so without per-*Type dedup the walker would attach each
// rtype twice, doubling consumption to 4.
func TestSynthAttachIdempotent(t *testing.T) {
	const src = `package main

import "fmt"

type T struct{ N int }

func (t T) String() string { return fmt.Sprintf("n=%d", t.N) }

func main() {
	var s fmt.Stringer = T{N: 3}
	fmt.Print(s.String())
}
`
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	var stdout, stderr bytes.Buffer
	i.SetIO(os.Stdin, &stdout, &stderr)

	before := stubs.SlotsUsedS1()
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	after := stubs.SlotsUsedS1()
	if got, want := after-before, uint32(2); got != want {
		t.Errorf("SlotsUsedS1 delta = %d, want %d (T + *T; alias dedup broken if 4)", got, want)
	}
}

// An interpreted UnmarshalJSON returning a concrete struct error must
// propagate through the synth S3 bridge without an IsNil-on-struct panic
// (the github.com/google/uuid TestJSONUnmarshal regression).
func TestSynthUnmarshalConcreteError(t *testing.T) {
	const src = `package main

import (
	"encoding/json"
	"fmt"
)

type lenError struct{ n int }

func (e lenError) Error() string { return fmt.Sprintf("bad length %d", e.n) }

type Tagged struct{ X int }

func (t *Tagged) UnmarshalJSON(data []byte) error {
	if len(data) != 99 {
		return lenError{n: len(data)}
	}
	t.X = len(data)
	return nil
}

func main() {
	var v Tagged
	err := json.Unmarshal([]byte("[1,2,3,4]"), &v)
	if err == nil {
		fmt.Print("no error")
		return
	}
	fmt.Print("err: ", err)
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "err: bad length 9"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// A named slice type with a method, used as a struct field, must keep its named
// identity (and method set) -- not degrade to the underlying []elem. fmt must
// dispatch the named type's Format, not default slice formatting.
// (github.com/pkg/errors TestStackTraceFormat regression.)
func TestSynthNamedSliceField(t *testing.T) {
	const src = `package main

import "fmt"

type Frame int

func (f Frame) Format(s fmt.State, verb rune) { fmt.Fprintf(s, "F%d", int(f)) }

type Trace []Frame

func (t Trace) Format(s fmt.State, verb rune) {
	if s.Flag('+') {
		for _, f := range t {
			fmt.Fprint(s, "\n")
			f.Format(s, verb)
		}
		return
	}
	fmt.Fprint(s, "[bare]")
}

func main() {
	t := Trace{Frame(1), Frame(2)}
	box := struct{ Tr Trace }{t}
	fmt.Printf("%T|%+v|%v", box.Tr, box.Tr, box.Tr)
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "main.Trace|\nF1\nF2|[bare]"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthDirectIfaceFuncRecv covers a value-receiver method on a named func
// type (kindDirectIface) dispatched through a synth stub: the stub's recv is
// the receiver word itself, not its address (pflag boolfuncValue.Set).
func TestSynthDirectIfaceFuncRecv(t *testing.T) {
	const src = `package main

import "fmt"

type fv func(string) error

func (f fv) Set(s string) error { return f(s) }
func (f fv) String() string     { return "" }

type Value interface {
	String() string
	Set(string) error
}

type Flag struct{ Value Value }

func main() {
	var vals []string
	fn := func(s string) error { vals = append(vals, s); return nil }
	flags := map[string]*Flag{"f": {Value: fv(fn)}}
	if err := flags["f"].Value.Set("x"); err != nil {
		panic(err)
	}
	fmt.Print(vals)
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "[x]"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthPromotedDirectIfaceValue covers a promoted method on a VALUE of a
// direct-iface struct (struct{*bytes.Buffer} -> io.Writer): the stub recv is
// the embedded pointer word itself; formerly skipped, then mis-dereferenced.
func TestSynthPromotedDirectIfaceValue(t *testing.T) {
	const src = `package main

import (
	"bytes"
	"fmt"
	"io"
)

type W struct{ *bytes.Buffer }

type Box struct{ Out io.Writer }

func main() {
	w := W{&bytes.Buffer{}}
	b := Box{Out: w}
	fmt.Fprint(b.Out, "hi")
	fmt.Print(w.Buffer.String())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "hi"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}
