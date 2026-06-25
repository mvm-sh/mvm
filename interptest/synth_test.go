package interptest

import (
	"bytes"
	"os"
	"runtime"
	"testing"

	"github.com/mvm-sh/mvm/interp"
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
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("synth_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "hello world"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthFloatMethodIface exercises the float word-class dispatch: an interface
// whose methods carry float64 params/results and a float-field struct param/result
// (Vec) is satisfied by an interpreted concrete, and the concrete is returned as
// that interface across a reflect.Call (MakeFunc) boundary -- so its synth rtype
// must actually implement the interface (reflect.Implements), which needs the
// float methods installed. Pre-float-words, float methods dropped (classifyType
// rejected floats), so Shape's rtype lacked Area/MoveTo/At/Scale and the boxing
// panicked "not assignable". Shapes: Scale -> "f_f", MoveTo(Vec) -> "ff_",
// At() Vec -> "_ff", Area() -> "_f".
func TestSynthFloatMethodIface(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"reflect"
)

type Vec struct{ X, Y float64 }

type Shape interface {
	Area() float64
	Scale(f float64) float64
	MoveTo(v Vec)
	At() Vec
}

type Circle struct {
	c Vec
	r float64
}

func (s *Circle) Area() float64          { return 3.14159 * s.r * s.r }
func (s *Circle) Scale(f float64) float64 { s.r *= f; return s.r }
func (s *Circle) MoveTo(v Vec)           { s.c = v }
func (s *Circle) At() Vec                 { return s.c }

func makeShape() Shape { return &Circle{r: 2} }

func main() {
	// reflect.Call forces makeShape through a MakeFunc wrapper, boxing the
	// concrete *Circle into the Shape interface return -- requires *Circle's
	// synth rtype to implement Shape (all four float methods present).
	out := reflect.ValueOf(makeShape).Call(nil)
	s := out[0].Interface().(Shape)
	s.MoveTo(Vec{X: 1.5, Y: -2.5})
	r := s.Scale(3)
	at := s.At()
	fmt.Printf("area=%.2f r=%.1f at=%.1f,%.1f", s.Area(), r, at.X, at.Y)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("synth_float_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "area=113.10 r=6.0 at=1.5,-2.5"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthSubWordPointIface exercises the sub-word-packed struct word path:
// fixed.Point26_6 is struct{X,Y int32}, two int32 in one 8-byte word, so its
// register-ABI words are "ii" with leaves at offsets 0 and 4. An interface whose
// methods take one and two such points (like freetype's raster.Adder) is
// satisfied by an interpreted concrete, and the methods are invoked through
// reflect (a NATIVE-boundary call), forcing the synth stub + marshalArgs layout
// -- not interpreted dispatch. Adversarial negative/large coords pin that each
// leaf lands at its true byte offset (a uniform 8-byte stride would corrupt Y).
// Shapes: Start(Pt) -> "ii_", Add2(Pt,Pt) -> "iiii_", Sum() int64 -> "_i".
func TestSynthSubWordPointIface(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"reflect"
)

type Pt struct{ X, Y int32 }

type Path interface {
	Start(a Pt)
	Add2(b, c Pt)
	Sum() int64
}

type rec struct{ sum int64 }

func (r *rec) Start(a Pt)   { r.sum += int64(a.X)*1 + int64(a.Y)*10 }
func (r *rec) Add2(b, c Pt) { r.sum += int64(b.X)*100 + int64(b.Y)*1000 + int64(c.X)*10000 + int64(c.Y)*100000 }
func (r *rec) Sum() int64   { return r.sum }

func makePath() Path { return &rec{} }

func main() {
	p := reflect.ValueOf(makePath).Call(nil)[0].Interface().(Path)
	// reflect method calls dispatch through the synth stub (native boundary),
	// unlike interpreted p.Start(...) which never touches the stub.
	pv := reflect.ValueOf(p)
	pv.MethodByName("Start").Call([]reflect.Value{reflect.ValueOf(Pt{X: -3, Y: 5})})
	pv.MethodByName("Add2").Call([]reflect.Value{
		reflect.ValueOf(Pt{X: -7, Y: 11}),
		reflect.ValueOf(Pt{X: 13, Y: -17}),
	})
	fmt.Print(p.Sum())
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("synth_subword_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	// Start: -3*1 + 5*10 = 47. Add2: -7*100 + 11*1000 + 13*10000 - 17*100000
	// = -1559700. Total = -1559653.
	if got, want := stdout.String(), "-1559653"; got != want {
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
	i := interp.NewInterpreter(golang.GoSpec)
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
			i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
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
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "hi"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthPtrIdentValueRecvDirectIface covers a VALUE-receiver method reached
// through the *T identity of a direct-iface struct (flag's ExampleValue:
// fs.Var(&URLValue{u}, ...) with String/Set on URLValue = struct{*url.URL}).
// The stub recv is the *T pointer and must be dereferenced; reinterpreting it
// as the receiver word (correct only on T's own value identity) reconstructs a
// bogus inner pointer and faults.
func TestSynthPtrIdentValueRecvDirectIface(t *testing.T) {
	const src = `package main

import (
	"flag"
	"fmt"
	"net/url"
)

type URLValue struct {
	URL *url.URL
}

func (v URLValue) String() string {
	if v.URL != nil {
		return v.URL.String()
	}
	return ""
}

func (v URLValue) Set(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	*v.URL = *u
	return nil
}

func main() {
	u := &url.URL{}
	fs := flag.NewFlagSet("v", flag.ExitOnError)
	fs.Var(&URLValue{u}, "url", "URL to parse")
	if err := fs.Parse([]string{"-url", "https://golang.org/pkg/flag/"}); err != nil {
		panic(err)
	}
	fmt.Printf("%s %s %s", u.Scheme, u.Host, u.Path)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "https golang.org /pkg/flag/"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthComplexMethodIface exercises the complex128 word path on every arch:
// complex128 is two float64 in two FP registers ("ff"), so Set(complex128) and
// Get() complex128 resolve to the "ff_" and "_ff" stub pools. An interpreted
// concrete satisfies the interface and its methods are invoked through reflect (a
// NATIVE-boundary call), forcing the synth stub + the two-float marshaling -- this
// pins the register-ABI claim that one complex128 arg passes like two float64.
func TestSynthComplexMethodIface(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"reflect"
)

type Box interface {
	Set(v complex128)
	Get() complex128
}

type cell struct{ v complex128 }

func (c *cell) Set(v complex128) { c.v = v }
func (c *cell) Get() complex128  { return c.v }

func makeBox() Box { return &cell{} }

func main() {
	b := reflect.ValueOf(makeBox).Call(nil)[0].Interface().(Box)
	bv := reflect.ValueOf(b)
	bv.MethodByName("Set").Call([]reflect.Value{reflect.ValueOf(complex(1.5, -2.5))})
	got := bv.MethodByName("Get").Call(nil)[0].Interface().(complex128)
	fmt.Printf("%.1f %.1f", real(got), imag(got))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("synth_complex_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "1.5 -2.5"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthFloat32MethodIface exercises the single-precision word path on every
// arch: float32 is a 'g' word (its own FP-register stub, distinct from float64),
// and complex64 is two 'g' halves ("gg"). SetF/GetF/Twice resolve to "g_"/"_g"/
// "g_g"; SetC/GetC to "gg_"/"_gg". An interpreted concrete satisfies the interface
// and its methods are invoked through reflect (a NATIVE-boundary call), forcing the
// synth stub + the float32<->float64 marshaling on the register ABI.
func TestSynthFloat32MethodIface(t *testing.T) {
	const src = `package main

import (
	"fmt"
	"reflect"
)

type Box interface {
	SetF(f float32)
	GetF() float32
	Twice(f float32) float32
	SetC(c complex64)
	GetC() complex64
}

type cell struct {
	f float32
	c complex64
}

func (c *cell) SetF(f float32)          { c.f = f }
func (c *cell) GetF() float32           { return c.f }
func (c *cell) Twice(f float32) float32 { return f * 2 }
func (c *cell) SetC(v complex64)        { c.c = v }
func (c *cell) GetC() complex64         { return c.c }

func makeBox() Box { return &cell{} }

func main() {
	b := reflect.ValueOf(makeBox).Call(nil)[0].Interface().(Box)
	bv := reflect.ValueOf(b)
	bv.MethodByName("SetF").Call([]reflect.Value{reflect.ValueOf(float32(2.5))})
	f := bv.MethodByName("GetF").Call(nil)[0].Interface().(float32)
	d := bv.MethodByName("Twice").Call([]reflect.Value{reflect.ValueOf(float32(2.5))})[0].Interface().(float32)
	bv.MethodByName("SetC").Call([]reflect.Value{reflect.ValueOf(complex64(complex(3, -4)))})
	c := bv.MethodByName("GetC").Call(nil)[0].Interface().(complex64)
	fmt.Printf("%.1f %.1f %.1f %.1f", f, d, real(c), imag(c))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("synth_float32_iface_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "2.5 5.0 3.0 -4.0"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestSynthFloat32ArrayMethodIface exercises the array word path, which only the
// wasm/ABI0 classifier supports (arrays of len > 1 are stack-passed on the register
// ABI, so they drop there). SetA([2]int32) carries packed bytes in an 'i' slot and
// SumA reads them back; the float32 methods ride along (they work on every arch --
// see TestSynthFloat32MethodIface). Skipped off wasm, where the array method drops
// and the synth rtype would not implement Box.
func TestSynthFloat32ArrayMethodIface(t *testing.T) {
	if runtime.GOARCH != "wasm" {
		t.Skip("array word stubs are wasm/ABI0-only")
	}
	const src = `package main

import (
	"fmt"
	"reflect"
)

type Box interface {
	SetF(f float32)
	GetF() float32
	SetA(a [2]int32)
	SumA() int64
}

type cell struct {
	f float32
	a [2]int32
}

func (c *cell) SetF(f float32) { c.f = f }
func (c *cell) GetF() float32  { return c.f }
func (c *cell) SetA(a [2]int32) { c.a = a }
func (c *cell) SumA() int64    { return int64(c.a[0]) + int64(c.a[1]) }

func makeBox() Box { return &cell{} }

func main() {
	b := reflect.ValueOf(makeBox).Call(nil)[0].Interface().(Box)
	bv := reflect.ValueOf(b)
	bv.MethodByName("SetF").Call([]reflect.Value{reflect.ValueOf(float32(2.5))})
	bv.MethodByName("SetA").Call([]reflect.Value{reflect.ValueOf([2]int32{-7, 100})})
	f := bv.MethodByName("GetF").Call(nil)[0].Interface().(float32)
	sum := bv.MethodByName("SumA").Call(nil)[0].Interface().(int64)
	fmt.Printf("%.1f %d", f, sum)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("synth_float32_test.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "2.5 93"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// evalNoPanic runs an interpreted program that panics on its own assertion
// failure, so the check works on the wasm build too (where interpreted fmt
// output is not captured by the in-process SetIO buffer).
func evalNoPanic(t *testing.T, name, src string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval(name, src); err != nil {
		t.Fatalf("Eval: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
}

// TestSynthConcreteMethodByNameCall exercises reflect.Value.MethodByName().Call()
// on a CONCRETE interpreted type whose method is never reached through an
// interface (so it is absent from the global interface-method table). On the
// shared-PC (wasm) build the native uncommon-table entry is a trap stub, so the
// shim routes through VM dispatch; the bound func type must come from the
// method's own signature (mtype.Method.Rtype), not the iface-method table -- else
// MethodByName returns an invalid Value and the call drops silently (text/template
// method pipelines rendered empty). On non-wasm builds the native table serves it.
func TestSynthConcreteMethodByNameCall(t *testing.T) {
	evalNoPanic(t, "synth_concrete_method_test.go", `package main

import "reflect"

type Item struct{ Price int }

func (i Item) Discounted(p int) int { return i.Price - p }

func main() {
	m := reflect.ValueOf(Item{Price: 40}).MethodByName("Discounted")
	if !m.IsValid() {
		panic("MethodByName returned an invalid Value")
	}
	out := m.Call([]reflect.Value{reflect.ValueOf(7)})
	if got := out[0].Interface().(int); got != 33 {
		panic("wrong method result")
	}
}
`)
}

// TestSynthErrorTypeIdentity pins the canonical identity of the predeclared
// error interface across reflect: reflect.TypeFor[error]() (the interpreted shim
// TypeOf((*error)(nil)).Elem()) must equal the error result type of an
// interpreted func signature. A bridgePtrToIface bug synthesized a fresh
// interface rtype for the (*error)(nil) type tag even though error already
// carries its canonical native rtype, so reflect.TypeFor[error]() != Out(1) and
// text/template's goodFunc rejected every (bool, error) builtin ("second return
// value should be error; is error"). That specific regression only surfaces
// through the full `mvm run` path on the shared-PC (wasm) build (validated
// end-to-end via text/template under wazero); this test guards the invariant.
func TestSynthErrorTypeIdentity(t *testing.T) {
	evalNoPanic(t, "synth_error_identity_test.go", `package main

import "reflect"

func le(a, b int) (bool, error) { return a < b, nil }

func main() {
	tf := reflect.TypeFor[error]()
	te := reflect.TypeOf((*error)(nil)).Elem()
	out1 := reflect.TypeOf(le).Out(1)
	if out1 != tf || out1 != te {
		panic("error interface rtype identity split across the bridge boundary")
	}
}
`)
}
