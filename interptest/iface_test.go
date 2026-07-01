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

func TestMapFuncReturnsUnreflectableIface(t *testing.T) {
	src := `package main

import "fmt"

type Cfg struct{ Name string }

type Encoder interface {
	AddComplex128(k string, v complex128) // unclassifiable: no word-shape
	Encode() string
}

type jsonEncoder struct{ cfg Cfg }

func (j *jsonEncoder) AddComplex128(k string, v complex128) {}
func (j *jsonEncoder) Encode() string                       { return "json:" + j.cfg.Name }

var registry = map[string]func(Cfg) (Encoder, error){
	"json": func(c Cfg) (Encoder, error) { return &jsonEncoder{cfg: c}, nil },
}

func newEncoder(name string, c Cfg) (Encoder, error) {
	ctor, ok := registry[name]
	if !ok {
		return nil, nil
	}
	return ctor(c) // ctor is a non-addressable func value read from the map
}

func main() {
	e, err := newEncoder("json", Cfg{Name: "x"})
	fmt.Println(e.Encode(), err)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("mapfunc_iface.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "json:x <nil>\n"; got != want {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

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

// Assigning nil to a closure-captured interface (boxed as vm.Iface) must yield a
// true nil interface, not a typed-nil; the latter made gorilla/websocket's
// `if netConn != nil { netConn.Close() }` deref nil.
func TestIfaceNilAssignCapturedCell(t *testing.T) {
	src := `package main

type Closer interface{ Close() error }
type conn struct{ name string }

func (c *conn) Close() error { return nil }

func main() {
	c := &conn{"x"}
	var nc Closer = c
	closed := false
	defer func() {
		if nc != nil {
			nc.Close()
			closed = true
		}
		println("defer nc==nil:", nc == nil, "closed:", closed)
	}()
	println("before nil, nc==nil:", nc == nil)
	nc = nil
	println("after nil, nc==nil:", nc == nil)
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
		t.Fatalf("nil interface deref panicked: %s", stderr.String())
	}
	want := "before nil, nc==nil: false\nafter nil, nc==nil: true\ndefer nc==nil: true closed: false\n"
	if got := stdout.String(); got != want {
		t.Errorf("output: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

// *p = v through a *error (narrow interface) went through DerefSet's numSet, which
// reflect.Set-rejected the interface{}-boxed source; the field/slot paths bridge but
// DerefSet did not. Covers a direct error param and an interface-typed struct field.
func TestDerefSetNarrowIface(t *testing.T) {
	const src = `package main

type myErr struct{}

func (myErr) Error() string { return "boom" }

type Box struct{ err error }

func storeParam(dst *error, src error) { *dst = src }
func storeField(dst *error, b *Box)    { *dst = b.err }

func main() {
	var e error
	b := &Box{err: myErr{}}
	storeParam(&e, b.err)
	println("param:", e.Error())
	e = nil
	storeField(&e, b)
	println("field:", e.Error())
}
`
	out := evalOut(t, "derefset", src)
	if want := "param: boom\nfield: boom\n"; out != want {
		t.Errorf("output: got %q, want %q", out, want)
	}
}
