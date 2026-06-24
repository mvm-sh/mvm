package interptest

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/vm"
)

// Regression for `mvm test crypto/ecdh` -> "undefined: ecdh.PrivateKey".
//
// When a package is loaded as a test target, importingPkg is set to its
// path, so a bare identifier that names one of the package's own types
// resolves to that type. A struct field group like `Foo, Bar string`
// then mis-parsed: parseParamTypes processes right-to-left, and the lone
// leftmost ident `Foo` (which also names a type) made hasFirstParam
// report "type-only", so `Foo` became an unnamed embedded field of type
// Foo instead of a field NAME sharing the trailing string type. mvm then
// failed resolving the embedded type. The crypto/ecdh external test hit
// this via `map[ecdh.Curve]struct{ PrivateKey, PublicKey string; ... }`,
// where the field names PrivateKey/PublicKey collide with ecdh's own types.
func TestRemoteFieldNameMatchesLocalType(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/coll",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/coll\n",
			"coll.go": `package coll

type Foo struct{ V int }
type Bar struct{ V int }

// Field names Foo, Bar (sharing the string type) collide with the
// package's own Foo/Bar types. The var is a deferred (Phase 2) decl.
var table = map[string]struct {
	Foo, Bar string
}{
	"k": {Foo: "a", Bar: "b"},
}

func Lookup(k string) string { return table[k].Foo + table[k].Bar }
`,
		},
	})

	var stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.SetIncludeTests(true)

	// Direct-target load (mirrors test_cmd's `i.Eval(target, "")`), which sets
	// importingPkg = "example.com/x/coll". Pre-fix this failed with
	// "undefined: Foo" because the field group was mis-parsed.
	if _, err := i.Eval("example.com/x/coll", ""); err != nil {
		t.Fatalf("loading target: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "undefined") {
		t.Errorf("unexpected undefined error: %s", stderr.String())
	}
}

// An interpreted type with unexported fields round-trips through native
// encoding/gob solely via its MarshalBinary/UnmarshalBinary methods. Without
// the gobx arg proxies, the interpreted value reaches (*gob.Encoder).Encode as
// a synthetic struct whose MarshalBinary is invisible, and gob falls back to
// field reflection -> "type struct { PVector_1 int } has no exported fields".
// The proxies wrap it as encoding.BinaryMarshaler/BinaryUnmarshaler dispatching
// back into the interpreter.
func TestGobBinaryMarshaler(t *testing.T) {
	src := `package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

type Vector struct {
	x, y, z int
}

func (v Vector) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintln(&b, v.x, v.y, v.z)
	return b.Bytes(), nil
}

func (v *Vector) UnmarshalBinary(data []byte) error {
	b := bytes.NewBuffer(data)
	_, err := fmt.Fscanln(b, &v.x, &v.y, &v.z)
	return err
}

func main() {
	var network bytes.Buffer
	enc := gob.NewEncoder(&network)
	if err := enc.Encode(Vector{3, 4, 5}); err != nil {
		fmt.Println("encode error:", err)
		return
	}
	dec := gob.NewDecoder(&network)
	var v Vector
	if err := dec.Decode(&v); err != nil {
		fmt.Println("decode error:", err)
		return
	}
	fmt.Println(v)
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
	if got := stdout.String(); !strings.Contains(got, "{3 4 5}") {
		t.Errorf("gob BinaryMarshaler round-trip failed: stdout=%q stderr=%q", got, stderr.String())
	}
}

// An interpreted concrete type encoded through a registered gob interface comes
// back on decode as a native value of the type's synthetic reflect.StructOf
// rtype (no native methods). IfaceCall must re-wrap it as an mvm Iface via
// typeByRtype so the compiled value-receiver method dispatches; before the fix
// the call target resolved to nilFuncAddr and panicked with a nil deref.
func TestGobInterfaceRewrap(t *testing.T) {
	// Use a type name distinct from _samples/gob_interface.go's Point: gob's
	// registry is process-global, and each Eval's interpreted type is a distinct
	// synth rtype, so two tests registering the same name in one test binary
	// collide ("duplicate types for main.Point").
	src := `package main

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
	"math"
)

type RewrapPoint struct {
	X, Y int
}

func (p RewrapPoint) Hypotenuse() float64 {
	return math.Hypot(float64(p.X), float64(p.Y))
}

type Pythagoras interface {
	Hypotenuse() float64
}

func interfaceEncode(enc *gob.Encoder, p Pythagoras) {
	if err := enc.Encode(&p); err != nil {
		log.Fatal("encode:", err)
	}
}

func interfaceDecode(dec *gob.Decoder) Pythagoras {
	var p Pythagoras
	if err := dec.Decode(&p); err != nil {
		log.Fatal("decode:", err)
	}
	return p
}

func main() {
	var network bytes.Buffer
	gob.Register(RewrapPoint{})
	enc := gob.NewEncoder(&network)
	for i := 1; i <= 3; i++ {
		interfaceEncode(enc, RewrapPoint{3 * i, 4 * i})
	}
	dec := gob.NewDecoder(&network)
	for i := 1; i <= 3; i++ {
		result := interfaceDecode(dec)
		fmt.Println(result.Hypotenuse())
	}
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
	if got, want := stdout.String(), "5\n10\n15\n"; got != want {
		t.Errorf("gob interface round-trip: got %q want %q (stderr=%q)", got, want, stderr.String())
	}
}

// capWriter records the first cap bytes written and discards the rest, so a
// flood of dumps does not balloon memory. Mutex-guarded for use under -race.
type capWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	cap int
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() < w.cap {
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *capWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestRequestStateDump verifies that a state-dump request raised from another
// goroutine (as a signal handler would) prints the current position and mvm
// call stack of the running interpreter, mid-run, without stopping it.
func TestRequestStateDump(t *testing.T) {
	intp := interp.NewInterpreter(golang.GoSpec)
	sink := &capWriter{cap: 8192}
	intp.SetIO(nil, sink, sink)

	if _, err := intp.Eval("setup",
		`func spin(n int) int { s := 0; for i := 0; i < n; i++ { s += i % 7 }; return s }`); err != nil {
		t.Fatal(err)
	}

	// Arm the flag a bounded number of times from another goroutine for
	// cross-goroutine coverage under -race (each re-arm yields, so the dump
	// does not fire on every loop back-edge). Arm once here too so at least
	// one dump is guaranteed regardless of scheduling.
	stop := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			select {
			case <-stop:
				return
			default:
			}
			vm.RequestStateDump()
			runtime.Gosched()
		}
	}()
	vm.RequestStateDump()

	if _, err := intp.Eval("run", "spin(200000)"); err != nil {
		close(stop)
		t.Fatal(err)
	}
	close(stop)

	out := sink.String()
	for _, want := range []string{"mvm execution state", "mvm stack:", "spin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dump missing %q; got:\n%s", want, out)
		}
	}
}

// TestStatsAccumulate checks that Stats counters advance across Eval calls
// and that FormatStats renders the expected header and labels.
func TestStatsAccumulate(t *testing.T) {
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.AutoImportPackages()

	if _, err := i.Eval("first", `1 + 2`); err != nil {
		t.Fatalf("first Eval: %v", err)
	}
	compile1, run1 := i.Stats.CompileTime, i.Stats.RunTime
	if compile1 <= 0 || run1 <= 0 {
		t.Fatalf("after first Eval: CompileTime=%v RunTime=%v, both want >0", compile1, run1)
	}

	if _, err := i.Eval("second", `3 + 4`); err != nil {
		t.Fatalf("second Eval: %v", err)
	}
	if i.Stats.CompileTime <= compile1 || i.Stats.RunTime <= run1 {
		t.Errorf("counters did not advance: compile %v->%v, run %v->%v",
			compile1, i.Stats.CompileTime, run1, i.Stats.RunTime)
	}

	out := interp.FormatStats(i)
	for _, want := range []string{"mvm stats:", "packages:", "sources:", "lines)", "bytes)", "code:", "data:", "compile:", "execute:"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatStats output missing %q:\n%s", want, out)
		}
	}
}

// ExampleInterp_Eval shows how to embed the interpreter, expose custom Go
// functions to interpreted code via a synthetic package, and read back the
// value of an evaluated expression.
func ExampleInterp_Eval() {
	i := interp.NewInterpreter(golang.GoSpec)

	// Bring in the standard library bindings (fmt, strings, ...).
	i.ImportPackageValues(stdlib.Values)

	// Expose a custom package "host" with a function and a constant.
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"host": {
			"Greet":  reflect.ValueOf(func(s string) string { return "hello, " + s + "!" }),
			"Answer": reflect.ValueOf(42),
		},
	})

	// Register every loaded package under its short name so the snippet does
	// not need explicit import statements.
	i.AutoImportPackages()

	res, err := i.Eval("m:expr", `fmt.Sprintf("%s answer=%d", host.Greet("world"), host.Answer)`)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(res.Interface())
	// Output:
	// hello, world! answer=42
}

// runProg evaluates src and returns its stdout, failing on any eval error.
// new(reflect.Value) reflected as **reflect.Value: the shim pre-seeds the
// "reflect.Value" symbol, and the compiler's Period handler filled its Data
// slot with the raw (*reflect.Value)(nil) native value instead of the zero
// reflect.Value, so PtrNew built a pointer to a pointer. (go-spew
// TestInvalidReflectValue.)
func TestNewReflectValuePointerType(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

func main() {
	v := new(reflect.Value)
	fmt.Printf("%T\n", v)
}
`
	if got, want := evalOut(t, "rv.go", src), "*reflect.Value\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A map literal/assignment of an untyped constant value into a named-string
// element type left the value as a plain string, which SetMapIndex rejected.
// MapSet now adopts the named element type like the key path. (go-spew
// TestFormatter map[pstringer]pstringer{"one": "1"}.)
func TestMapNamedStringElemConvert(t *testing.T) {
	src := `package main

import "fmt"

type pstringer string

func main() {
	m := map[string]pstringer{"a": "x"}
	m["b"] = "y"
	fmt.Println(m["a"], m["b"])
}
`
	if got, want := evalOut(t, "mapelem.go", src), "x y\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// complex() of a typed float32 constant must yield complex64, not complex128:
// the deref helper widened any typed numeric const to float64. (go-spew
// complex64 dump/format tests.)
func TestComplexTypedFloat32Const(t *testing.T) {
	src := `package main

import "fmt"

func main() {
	a := complex(float32(6), -2)
	b := complex(float64(6), -2)
	fmt.Printf("%T %T\n", a, b)
}
`
	if got, want := evalOut(t, "cx.go", src), "complex64 complex128\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestFloat32ConstRounds checks that a constant converted to float32 is rounded
// to float32 precision before further folding, matching Go.
// Regression for structpb's TestToStruct: it computes the want value as
// float64(float32(123.456)), which mvm folded as 123.456 (full precision)
// because constConvert kept the untyped constant unrounded for float32.
func TestFloat32ConstRounds(t *testing.T) {
	src := `package main

import "fmt"

func main() {
	const c = 123.456
	fmt.Println(float64(float32(c)))
	fmt.Println(float64(float32(123.456)))
	fmt.Println(complex128(complex64(complex(0.1, 0.2))))
}
`
	want := "123.45600128173828\n123.45600128173828\n(0.10000000149011612+0.20000000298023224i)\n"
	if got := evalOut(t, "f32.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestTypedConstPlusUntypedKeepsType pins the go.uber.org/zap addStack bug.
// A typed named-type constant plus an untyped one (`Level(int8) + 1`) must keep
// the named type, so boxing the result into a Level-satisfying interface
// dispatches the method. Pre-fix the fold picked the wider numeric rank and,
// both being rank 0, returned the untyped int, dropping the Level name.
func TestTypedConstPlusUntypedKeepsType(t *testing.T) {
	src := `package main

import "fmt"

type Level int8

const FatalLevel Level = 5

func (l Level) Enabled(x Level) bool { return x >= l }

type Enabler interface{ Enabled(Level) bool }

func main() {
	var e Enabler = FatalLevel + 1
	fmt.Printf("%T %v", e, e.Enabled(2))
}
`
	if got, want := evalOut(t, "typedconst.go", src), "main.Level false"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestMethodExprInterfaceArg checks that a method expression stored in a variable
// and then called dispatches through the interpreter, even when a parameter is an
// interface satisfied by an interpreted concrete.
// Regression for structpb: protojson calls encoder.marshalStruct (a method
// expression of type func(encoder, protoreflect.Message) error) via a variable.
// The old path used reflect.Call, which rejected the synthetic concrete as the
// synthetic interface param; the bypass binds the receiver and runs interpreted.
func TestMethodExprInterfaceArg(t *testing.T) {
	src := `package main

import "fmt"

type Stringer interface{ String() string }

type Named struct{ name string }

func (n Named) String() string { return n.name }

type printer struct{ prefix string }

func (p printer) write(s Stringer) string { return p.prefix + s.String() }

type Counter struct{ n int }

func (c Counter) Add(x int) int { c.n += x; return c.n } // value recv copy

func main() {
	w := printer.write // method expr with interface param
	fmt.Println(w(printer{"> "}, Named{"hello"}))

	c := Counter{n: 10}
	add := Counter.Add
	fmt.Println(add(c, 5), c.n) // receiver copy must leave c.n at 10
}
`
	want := "> hello\n15 10\n"
	if got := evalOut(t, "methexpr.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestIssue9MultiReturnTupleAssignCrossPkg is the regression test for
// github.com/mvm-sh/mvm/issues/9. parseImportLine used to register `_` as a
// Kind=symbol.Pkg entry for `import _ "path"`, polluting the symbol table; a
// later `tag, _ = f(tag)` would resolve the blank LHS to that Pkg symbol
// instead of Kind==Unset, miss the blank-shortcut at comp/compiler.go's
// lang.Assign n>1 loop, and fall into the FieldRefSet default branch -- which
// then wrote the bool return into the struct slot, panicking with
// "reflect.Set: value of type bool is not assignable to type struct {P<n> int}".
// Fixed in goparser/decl.go by skipping SymSet when the alias name is "_".
func TestIssue9MultiReturnTupleAssignCrossPkg(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner\n",
				"inner.go": `package inner

type Tag struct{ X int }
`,
			},
		},
		remoteModule{
			path:    "example.com/x/outer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/outer\n",
				"outer.go": `package outer

import "example.com/x/inner"

func f(t inner.Tag) (inner.Tag, bool) { return t, true }

func init() {
	var tag inner.Tag
	tag, _ = f(tag)
	_ = tag
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", `import _ "example.com/x/outer"`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
}

// The zero value of a map or slice variable is nil. A local `var m map[...]int`
// used to be a non-nil empty container (Fnew always made one), so `m == nil` was
// false in a function body while correct under -e. A composite literal or make
// must still be non-nil, and writing to a nil map is a recoverable panic.
func TestNilZeroValueMapSlice(t *testing.T) {
	src := `package main

import "fmt"

type MyMap map[string]int
type S struct {
	M map[string]int
	L []int
}

func main() {
	var m map[string]int
	var s []int
	fmt.Println(m == nil, s == nil) // true true

	// Composite and make are non-nil, even when empty.
	fmt.Println(map[string]int{} == nil, []int{} == nil) // false false
	fmt.Println(make(map[string]int) == nil, make([]int, 0) == nil) // false false

	// Named header types and struct fields default to nil.
	var mm MyMap
	var st S
	fmt.Println(mm == nil, st.M == nil, st.L == nil) // true true true

	// Append to a nil slice works; the result is non-nil.
	var a []int
	a = append(a, 1, 2)
	fmt.Println(a, a == nil) // [1 2] false

	// Writing to a nil map is a recoverable runtime panic.
	func() {
		defer func() { fmt.Println("recovered:", recover() != nil) }() // true
		var nm map[string]int
		nm["x"] = 1
	}()
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("nil_zero.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "true true\n" +
		"false false\n" +
		"false false\n" +
		"true true true\n" +
		"[1 2] false\n" +
		"recovered: true\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}

// Two composite literals of the same map/slice type in one function must each
// produce their own non-nil container; the non-nil patch once matched a single
// canonical slot, leaving the other literal nil. Reduced from go-difflib chainB.
func TestNilZeroValueDuplicateLiterals(t *testing.T) {
	src := `package main

import "fmt"

func main() {
	// Two empty map[K]struct{} literals: writing to the first must not panic.
	a := map[string]struct{}{}
	a["x"] = struct{}{}
	b := map[string]struct{}{}
	b["y"] = struct{}{}
	fmt.Println(len(a), len(b)) // 1 1

	// Same for slices: both must be non-nil and independently appendable.
	p := []int{}
	p = append(p, 1)
	q := []int{}
	q = append(q, 2, 3)
	fmt.Println(len(p), len(q), p == nil, q == nil) // 1 2 false false
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("dup_literals.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "1 1\n" +
		"1 2 false false\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}

// The ccgo (modernc.org/libc) C-function-pointer idiom: a func boxed in
// interface{} has its data word read out as a uintptr (__ccgo_fp), then that
// uintptr is reinterpreted as a *funcval and called. For this to dispatch, the
// eface materialized from a boxed interpreted func must hold the func's native
// MakeFunc wrapper (a real *funcval registered in funcFields), not a bare code
// address -- otherwise the reconstructed call jumps into garbage (SIGBUS).
// Regression for the modernc.org/sqlite init crash.
func TestCcgoFuncPointerIdiom(t *testing.T) {
	src := `package main

import (
	"fmt"
	"unsafe"
)

func cmp(tls *int, a uintptr) int32 { return int32(a) + 1 }

func ccgoFp(f interface{}) uintptr {
	type iface [2]uintptr
	return (*iface)(unsafe.Pointer(&f))[1]
}

func main() {
	var tls int
	p := ccgoFp(cmp)
	r := (*(*func(*int, uintptr) int32)(unsafe.Pointer(&struct{ uintptr }{p})))(&tls, 41)
	fmt.Println(r)
}
`
	if got, want := evalOut(t, "ccgo_fp.go", src), "42\n"; got != want {
		t.Errorf("output: got %q, want %q", got, want)
	}
}

// A local numeric var whose address is taken in a switch case that does NOT
// execute must still read its assigned value in the case that does. The compiler
// marks the slot "addressed" for the whole function (so reads emit GetLocalSync),
// but the AddrLocal that promotes the slot to addressable ref storage is
// control-flow dependent. When it never runs, num stays authoritative and ref is
// a stale type-zero; GetLocalSync must not clobber num from ref in that case.
// Regression for the modernc.org/sqlite Xsqlite3_config varargs nil-deref:
// `ap = va` then a sibling case's `&ap` made the executed case read ap as 0.
func TestAddrInUnreachedCaseKeepsValue(t *testing.T) {
	src := `package main

import "fmt"

func sink(p *uintptr) uintptr { return *p }

func config(op int32, va uintptr) uintptr {
	var ap uintptr
	ap = va
	switch op {
	case 1:
		_ = sink(&ap)
	case 2:
		return ap // plain read; must see va, not 0
	}
	return 0
}

func main() {
	fmt.Println(config(2, 0xdead))
}
`
	if got, want := evalOut(t, "addr_case.go", src), "57005\n"; got != want {
		t.Errorf("output: got %q, want %q", got, want)
	}
}

// A &slot taken in an earlier switch case marks the slot addressed, so a later
// &slot reads via GetLocalSync. That later &slot must still become AddrLocal
// (aliasing the slot) so a native callee's write through the first pointer is
// visible to the second read. Models libc varargs: VaInt32(&ap) advances the
// cursor in place, then VaUintptr(&ap) must read the advanced position.
// Was modernc.org/sqlite Xsqlite3_db_config nil-deref (pRes re-read slot 0).
func TestAddrSyncSlotAliasesAcrossCases(t *testing.T) {
	src := `package main

import (
	"fmt"
	"unsafe"
)

func vaInt32(app *uintptr) int32 {
	ap := *app
	v := int32(*(*int64)(unsafe.Pointer(ap)))
	*app = ap + 8
	return v
}

func vaUintptr(app *uintptr) uintptr {
	ap := *app
	v := *(*uintptr)(unsafe.Pointer(ap))
	*app = ap + 8
	return v
}

func config(op int32, va uintptr) (onoff int32, pRes uintptr) {
	var ap uintptr
	ap = va
	switch op {
	case 100:
		pRes = vaUintptr(&ap) // marks ap addressed first
	default:
		onoff = vaInt32(&ap)  // advances cursor through &ap
		pRes = vaUintptr(&ap) // must read the advanced slot, not re-read slot 0
	}
	return
}

func main() {
	var buf [2]uintptr
	buf[0] = 1
	buf[1] = 0
	bp := uintptr(unsafe.Pointer(&buf[0]))
	onoff, pRes := config(1, bp)
	fmt.Println(onoff, pRes)
}
`
	if got, want := evalOut(t, "addr_sync_cases.go", src), "1 0\n"; got != want {
		t.Errorf("output: got %q, want %q", got, want)
	}
}

// A value-receiver method that self-assigns the receiver inside control flow
// (switch + if), as modernc.org/libc Int128.Float64 does (`n = n.Neg()`), must
// write the receiver, not a global Data slot. A compile-state-dependent stack
// misalignment let the lhs reach the global-assign path as a Kind=Value with the
// unset zero index (0), clobbering whichever global owned slot 0 -- surfacing as
// a flaky `reflect.Set [N]float64 not assignable to mathutil.Int128` at init.
// Globals here must stay intact and the method must compute correctly.
func TestValueRecvSelfAssignNoGlobalClobber(t *testing.T) {
	src := `package main

import "fmt"

type I128 struct{ Lo, Hi int64 }

func (n I128) Neg() I128 { return I128{-n.Lo, -n.Hi} }

func (n I128) Mag() int64 {
	switch n.Hi {
	case 0:
		return n.Lo
	case -1:
		return -n.Lo
	}
	if n.Hi < 0 {
		n = n.Neg()
		return n.Hi
	}
	return n.Hi
}

var sentinel = [3]float64{1.5, 2.5, 3.5}

func main() {
	fmt.Println(I128{Lo: 5, Hi: -7}.Mag())
	fmt.Println(sentinel)
}
`
	if got, want := evalOut(t, "valrecv_selfassign.go", src), "7\n[1.5 2.5 3.5]\n"; got != want {
		t.Errorf("output: got %q, want %q", got, want)
	}
}
