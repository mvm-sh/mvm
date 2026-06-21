package interptest

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// An interface embedding a forward-declared interface (cross-file: band.go's
// Banded embeds matrix.go's Matrix in gonum/mat) copied an empty placeholder
// method set, so calls through the embedder picked a random same-named
// concrete method ("mismatched types complex128 and float64"). The decl now
// defers until the embedded interface is parsed.
func TestIfaceEmbedForwardRef(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/mat",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/mat\n",
			// band.go sorts before matrix.go: Banded parses while Matrix is
			// still a placeholder.
			"band.go": `package mat

type Banded interface {
	Matrix
	Bandwidth() int
}

type Band struct{}

func (Band) At(i, j int) float64 { return float64(i + j) }
func (Band) Bandwidth() int      { return 1 }
`,
			"matrix.go": `package mat

type Matrix interface {
	At(i, j int) float64
}

func AtSum(b Banded) float64 { return b.At(1, 2) + b.At(2, 3) }
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/mat"
)

func main() {
	fmt.Println(mat.AtSum(mat.Band{}))
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "8\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A single-method interface whose method returns a FORWARD-declared interface:
// `x := eds.Get(0); x.Name()` inferred x's type from the reflect-materialized
// method signature, whose result was erased to bare interface{} whenever Item
// had not yet materialized to a named reflect interface. Materialization order
// is map-dependent, so "undefined: Name" appeared on most (not all) compiles.
// resolveIfaceMethodSym now uses the interpreted symbolic Sig, which keeps
// Item's method set regardless of materialization order.
//
// No concrete implementation of Name exists, so findConcreteFuncSym cannot
// rescue the call -- the result type must carry the method set itself.
func TestIfaceForwardResultMethodSet(t *testing.T) {
	src := `package main

type List interface {
	Get(i int) Item
}

type Item interface {
	Name() string
}

func use(eds List) string {
	if eds == nil {
		return ""
	}
	x := eds.Get(0)
	return x.Name()
}

func main() {
	_ = use
}
`
	// Forward-ref materialization order is map-dependent; run repeatedly with
	// fresh interpreters so a regression (undefined: Name) surfaces reliably.
	for k := range 16 {
		var stdout, stderr bytes.Buffer
		i := interp.NewInterpreter(golang.GoSpec)
		i.ImportPackageValues(stdlib.Values)
		i.SetIO(os.Stdin, &stdout, &stderr)
		if _, err := i.Eval("test", src); err != nil {
			t.Fatalf("iter %d Eval: %v", k, err)
		}
	}
}

// Storing an interface-typed local into a map whose element type is a narrower
// bridged interface (here image.Image) used to panic in MapSet:
// "reflect.Value.SetMapIndex: value of type interface {} is not assignable to
// type image.Image". Interface locals are boxed as interface{} (or an mvm
// Iface), neither directly assignable to the map's image.Image slot. The fix
// in wrapForFunc bridges/unwraps to the concrete element first. Repro of the
// `mvm test image` TestDecode failure (golden[name] = g).
func TestIfaceMapStoreBridgedInterface(t *testing.T) {
	src := `package main

import (
	"fmt"
	"image"
)

func main() {
	m := make(map[string]image.Image)
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var g image.Image = img
	m["a"] = g
	fmt.Println(m["a"].Bounds())
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "panic") {
		t.Fatalf("got panic: %s", stderr.String())
	}
	if got, want := stdout.String(), "(0,0)-(2,2)\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// An interface method returning an interface, dispatched on a value stored in
// a native slice element (make([]Iface, n) + IndexSet): the call used to go
// through the synth-attached stub, whose result marshaling dropped the
// interpreted concrete (it does not implement the synth iface rtype) to nil
// (gonum/mat HOGSVD: d.T() returned nil, then x.Dims() nil-deref'd). IfaceCall
// now dispatches synth-rtype receivers through the interpreter directly.
func TestIfaceResultThroughNativeSliceElem(t *testing.T) {
	src := `package main

import "fmt"

type Matrix interface {
	Dims() (int, int)
	T() Matrix
}

type Dense struct{ r, c int }

func (d *Dense) Dims() (int, int) { return d.r, d.c }
func (d *Dense) T() Matrix        { return Transpose{d} }

type Transpose struct{ M Matrix }

func (t Transpose) Dims() (int, int) { r, c := t.M.Dims(); return c, r }
func (t Transpose) T() Matrix        { return t.M }

func main() {
	data := make([]Matrix, 2)
	for i := range data {
		data[i] = &Dense{3, 2}
	}
	for _, d := range data {
		r, c := d.T().Dims()
		fmt.Println(r, c)
	}
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "2 3\n2 3\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// An interface method whose param mentions the interface itself (goldmark
// ast.Node.SortChildren(func(n1, n2 Node) int)) materializes that param to any
// while the interface's own synth rtype is mid-build, but to the precise synth
// iface on the concrete side. mtype.Identical compared materialized Rtypes by
// pointer and returned false, so no concrete type satisfied the interface.
// Identical now falls through to structural identity on rtype inequality.
func TestIfaceSelfRefMethodSig(t *testing.T) {
	src := `package main

import "fmt"

type Node interface {
	Kind() int
	SortChildren(comparator func(n1, n2 Node) int)
}

type Base struct{ n int }

func (b *Base) Kind() int                                     { return 1 }
func (b *Base) SortChildren(comparator func(n1, n2 Node) int) {}

func main() {
	var x any = &Base{}
	_, ok := x.(Node)
	fmt.Println("is Node:", ok)
}
`
	want := "is Node: true\n"
	if got := evalOut(t, "selfsig.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A value-receiver method dispatched through an embedded interface field got
// the unboxed iface value as its receiver cell; the value is unaddressable,
// so the first field write panicked "reflect.Value.Set using unaddressable
// value" (zerolog ConsoleWriter.Write: w.PartsOrder = ...). The cell is now
// detached to an addressable copy, which also matches Go's copy semantics.
func TestIfaceValueRecvFieldWrite(t *testing.T) {
	src := `package main

import (
	"bytes"
	"fmt"
	"io"
)

type W struct {
	Out   io.Writer
	Parts []string
}

func (w W) Write(p []byte) (int, error) {
	if w.Parts == nil {
		w.Parts = []string{"a", "b"}
	}
	return len(w.Parts), nil
}

type LevelWriter interface {
	io.Writer
	WriteLevel(level int, p []byte) (int, error)
}

type Adapter struct {
	io.Writer
}

func (lw Adapter) WriteLevel(_ int, p []byte) (int, error) {
	return lw.Write(p)
}

type Logger struct {
	w LevelWriter
}

func main() {
	var buf bytes.Buffer
	l := Logger{w: Adapter{W{Out: &buf}}}
	n, _ := l.w.WriteLevel(1, []byte("x"))
	fmt.Println(n)
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "2\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// Minimal repro for the pflag IPNet dispatch bug: a value-receiver method
// defined in a sub-package on a named type whose underlying is a
// stdlib-bridged struct, called via interface dispatch on *T.
//
// Before the fix in vm.go IfaceCall, the method body received `ipnet`
// as `*net.IPNet` (the stdlib type, with extra leading pointer) instead
// of `ipNetValue` (the value receiver expects). `net.IPNet(ipnet)` then
// panicked with "value of type *net.IPNet cannot be converted to type
// net.IPNet". The fix derefs ifc.Val when ResolveMethodType walked from
// *T to T and the method has a value receiver (PtrRecv=false).
func TestRemoteIPNetIfaceDirect(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/pflag",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/pflag\n",
			"pflag.go": `package pflag

import "net"

type ipNetValue net.IPNet

func (ipnet ipNetValue) String() string {
	n := net.IPNet(ipnet)
	return n.String()
}

type Stringer interface { String() string }

func Use(s Stringer) string { return s.String() }

func Make(v net.IPNet) Stringer {
	p := new(net.IPNet)
	*p = v
	return (*ipNetValue)(p)
}
`,
		},
	})

	src := `package main

import (
	"net"
	"example.com/x/pflag"
)

func main() {
	s := pflag.Make(net.IPNet{})
	println("got:", pflag.Use(s))
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "panic") {
		t.Errorf("got panic: %s", stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "<nil>") {
		t.Errorf("stdout: got %q", got)
	}
}

// Same fix, exercised through a pflag-shaped call chain (multi-method
// interface, several layers of method calls before the iface dispatch
// reaches the value-receiver method body).
func TestRemoteIPNetIfaceDispatch(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/pflag",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/pflag\n",
			"pflag.go": `package pflag

import "net"

type ipNetValue net.IPNet

func (ipnet ipNetValue) String() string {
	n := net.IPNet(ipnet)
	return n.String()
}

func (ipnet *ipNetValue) Set(value string) error { return nil }
func (*ipNetValue) Type() string                 { return "ipNet" }

func NewIPNetValue(val net.IPNet, p *net.IPNet) *ipNetValue {
	*p = val
	return (*ipNetValue)(p)
}

type Value interface {
	String() string
	Set(string) error
	Type() string
}

type FlagSet struct{}

func (f *FlagSet) VarPF(value Value, name string) string {
	return value.String()
}

func (f *FlagSet) VarP(value Value, name string) string { return f.VarPF(value, name) }

func (f *FlagSet) IPNetVarP(p *net.IPNet, name string, value net.IPNet) string {
	return f.VarP(NewIPNetValue(value, p), name)
}

func (f *FlagSet) IPNet(name string, value net.IPNet) string {
	p := new(net.IPNet)
	return f.IPNetVarP(p, name, value)
}
`,
		},
	})

	src := `package main

import (
	"net"
	"example.com/x/pflag"
)

func main() {
	fs := &pflag.FlagSet{}
	s := fs.IPNet("IPNet", net.IPNet{})
	println("got:", s)
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	got := stdout.String()
	if strings.Contains(stderr.String(), "panic") {
		t.Errorf("got panic: %s", stderr.String())
	}
	if !strings.Contains(got, "<nil>") {
		t.Errorf("stdout: got %q", got)
	}
}

// mvm registers pointer-receiver methods on the value type T with
// PtrRecv=true. The native-bridge layer (vm.wrapIface / wrapIfaceMulti)
// must NOT expose those methods on T's method set: in Go semantics they
// only belong to *T. Otherwise, passing a T value to native fmt would
// build a Stringer bridge with the int Value as receiver, and the
// pointer-receiver body would panic at the first `*recv` deref with
// "reflect: call of reflect.Value.Elem on int Value". Reproducer:
// pflag's TestPrintDefaults via `type customValue int` with a
// pointer-receiver String() that calls fmt.Sprintf("%v", *cv).
func TestNativeBridgeSkipsPtrRecvOnValue(t *testing.T) {
	src := `package main

import "fmt"

type customValue int

func (cv *customValue) String() string { return fmt.Sprintf("%v", *cv) }

func main() {
	cv2 := customValue(10)
	fmt.Println(&cv2)
	fmt.Println(cv2)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "PANIC=") {
		t.Errorf("got PANIC marker in output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "10\n10\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// new(io.Reader) emitted PtrNew against the type's zero-VALUE slot, which
// NewValue collapses to interface{} for interface/func kinds (heterogeneous
// var storage). The pointer thus reflected as *interface{} with no methods,
// so reflect.TypeOf(new(io.Reader)).Elem().Implements(...) was always true
// (TestImplements/TestAssignableTo). new now builds the pointer from the
// precise type descriptor, keeping the declared element type and method set.
func TestNewInterfaceRtypeKeepsMethods(t *testing.T) {
	src := `package main

import (
	"fmt"
	"io"
	"reflect"
)

func main() {
	e := reflect.TypeOf(new(io.Reader)).Elem()
	fmt.Println(e.String(), e.NumMethod())
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("newiface.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "io.Reader 1\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

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
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "5\n7\n9\n2.5\n"; got != want {
		t.Errorf("output: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

func TestStructEmbedIfaceNativeBoundary(t *testing.T) {
	const src = `func() int {
	rec := httptest.NewRecorder()
	var w http.ResponseWriter = rec
	type onlyCloseNotifier interface{ http.ResponseWriter }
	s := struct{ onlyCloseNotifier }{w.(onlyCloseNotifier)}
	http.Error(s, "boom", http.StatusInternalServerError)
	return rec.Code
}()`
	i := newAutoImportInterp(t)
	r, err := i.Eval("test", src)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got != "500" {
		t.Errorf("rec.Code = %q, want 500", got)
	}
}

// make(NamedSlice, n) must keep its named identity at runtime. Before the fix
// MkSlice rebuilt the value as the unnamed underlying []T, so boxing the make
// result straight into an interface (here a struct field round-tripped through
// a channel) dropped NamedSlice's method set and method dispatch nil-dereffed.
// Mirrors grpc internal/transport TestReadMessageHeaderMultipleBuffers, where a
// mem.SliceBuffer flows through recvBuffer's recvMsg channel and then dispatches
// the unexported mem.Buffer.read method inside package mem.
func TestMakeNamedSliceIfaceIdentity(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/mem",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/mem\n",
			"mem.go": `package mem

type Buffer interface {
	Len() int
	read(buf []byte) (int, Buffer)
}

type SliceBuffer []byte

func (s SliceBuffer) Len() int { return len(s) }

func (s SliceBuffer) read(buf []byte) (int, Buffer) {
	n := copy(buf, s)
	if n == len(s) {
		return n, nil
	}
	return n, s[n:]
}

func ReadUnsafe(dst []byte, buf Buffer) (int, Buffer) {
	return buf.read(dst)
}
`,
		},
	})

	src := `package main

import "example.com/x/mem"

type msg struct {
	buf mem.Buffer
	err error
}

func main() {
	ch := make(chan msg, 1)
	ch <- msg{buf: make(mem.SliceBuffer, 3)}
	m := <-ch
	dst := make([]byte, 2)
	n, rest := mem.ReadUnsafe(dst, m.buf)
	println("n=", n, "rest!=nil:", rest != nil)
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got := stderr.String(); strings.Contains(got, "panic") {
		t.Fatalf("dispatch panicked: %s", got)
	}
	if got, want := stdout.String(), "n= 2 rest!=nil: true\n"; got != want {
		t.Errorf("output: got %q, want %q", got, want)
	}
}
