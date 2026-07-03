package interptest

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// reflect.ValueOf(f) == reflect.ValueOf(f) must hold for a top-level func, as in
// Go (one global funcval). funcWrappers memoises the bridge wrapper to preserve
// it; previously each ValueOf minted a fresh wrapper that compared unequal. This
// is the Masterminds/semver TestParseConstraint pattern.
// The middle case types as func(int) int vs cfunc: unequal, as under gc.
func TestFuncReflectIdentity(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type cfunc func(int) int

func gte(x int) int { return x + 1 }
func lt(x int) int  { return x - 1 }

var ops = map[string]cfunc{">=": gte, "<": lt}

func main() {
	var f cfunc = gte
	// Same func, two separate ValueOf calls: equal, as in native Go.
	fmt.Println(reflect.ValueOf(f) == reflect.ValueOf(f))
	// Direct ref (func(int) int) vs map lookup (cfunc): distinct rtypes, unequal.
	fmt.Println(reflect.ValueOf(gte) == reflect.ValueOf(ops[">="]))
	// Distinct funcs must stay distinct.
	fmt.Println(reflect.ValueOf(gte) == reflect.ValueOf(ops["<"]))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("func_reflect_identity.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "true\nfalse\nfalse\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// reflect.TypeOf((*I)(nil)).Elem() must expose an interpreted interface's method
// set so reflect.Implements distinguishes conforming from non-conforming types.
// Without the synth-iface retype the interface erased to interface{}, so every
// type "implemented" it -- testify assert.Implements/NotImplements (TestImplements).
func TestReflectImplementsInterpretedIface(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type I interface{ TestMethod() }

type Conform struct{}

func (*Conform) TestMethod() {}

type NonConform struct{}

func main() {
	it := reflect.TypeOf((*I)(nil)).Elem()
	fmt.Println(it.NumMethod())
	fmt.Println(reflect.TypeOf(new(Conform)).Implements(it))
	fmt.Println(reflect.TypeOf(new(NonConform)).Implements(it))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("reflect_implements.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "1\ntrue\nfalse\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// The reflect.Value func-identity cache is keyed by {code address, rtype} on the
// persistent Machine; these guard the cross-Eval (REPL) invariants keying relies
// on: code addresses are monotonic across Evals, and a failed Eval's rollback
// never reuses an address that already cached a wrapper.

// A redefined func in a later Eval must not collapse onto the old func's wrapper.
func TestCrossEval_RedefineKeepsIdentity(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("e0", `import "reflect"`); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := i.Eval("e1", `type cf func() int
var f cf = func() int { return 1 }
var rf = reflect.ValueOf(f)`); err != nil {
		t.Fatalf("e1: %v", err)
	}
	r, err := i.Eval("e2", `var g cf = func() int { return 2 }
reflect.ValueOf(g) == rf`)
	if err != nil {
		t.Fatalf("e2: %v", err)
	}
	if got := r.Interface(); got != false {
		t.Errorf("reflect.ValueOf(g) == rf: got %v, want false (distinct funcs collided)", got)
	}
	r2, err := i.Eval("e3", `rf.Call(nil)[0].Int()`)
	if err != nil {
		t.Fatalf("e3: %v", err)
	}
	if got := r2.Interface(); got != int64(1) {
		t.Errorf("rf.Call -> %v, want 1 (cached wrapper points at wrong func)", got)
	}
}

// A failed Eval between caching and reuse must not corrupt the cached wrapper.
func TestCrossEval_RollbackThenReuse(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("e0", `import "reflect"`); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := i.Eval("e1", `type cf func() int
var f cf = func() int { return 11 }
var rf = reflect.ValueOf(f)`); err != nil {
		t.Fatalf("e1: %v", err)
	}
	if _, err := i.Eval("e2", `var bad = undefXYZ`); err == nil {
		t.Fatal("e2 expected to fail")
	}
	r, err := i.Eval("e3", `rf.Call(nil)[0].Int()`)
	if err != nil {
		t.Fatalf("e3: %v", err)
	}
	if got := r.Interface(); got != int64(11) {
		t.Errorf("rf.Call after rollback -> %v, want 11", got)
	}
}

// A cached func re-bridged and invoked in a later Eval, after globals grew and a
// referenced global was mutated, must read the new value (not a stale snapshot).
func TestCrossEval_GlobalsVisibleAfterGrowth(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("e0", `import "reflect"`); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := i.Eval("e1", `var base = 100
type cf func() int
var f cf = func() int { return base }
var rf = reflect.ValueOf(f)`); err != nil {
		t.Fatalf("e1: %v", err)
	}
	r, err := i.Eval("e2", `var a, b, c, d, e2v, ff, gg, hh int = 1, 2, 3, 4, 5, 6, 7, 8
_ = a + b + c + d + e2v + ff + gg + hh
base = 999
reflect.ValueOf(f).Call(nil)[0].Int()`)
	if err != nil {
		t.Fatalf("e2: %v", err)
	}
	if got := r.Interface(); got != int64(999) {
		t.Errorf("re-bridged f after growth -> %v, want 999 (stale globals)", got)
	}
}

// reflect.Type.MethodByName must find an interpreted method that is invisible to
// Go's native synth rtype: one beyond the 16-method synth cap AND of an
// unsupported synth shape (multiple func returns plus a slice), and its
// Method.Func must be callable with a zero receiver. This is exactly how
// protobuf's makeStructInfo derives a legacy message's oneof wrappers via
// reflect.PtrTo(t).MethodByName("XXX_OneofFuncs").Func.Call([zero-recv]); without
// the reflectTypeShim the method is reported missing and fieldInfoForOneof
// nil-derefs. The 20 A* getters push XXX_Wrappers past the cap; its shape (a
// func return plus []any) keeps it out of the native table regardless of order.
func TestReflectTypeMethodByNameBeyondCap(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type W struct {
	b bool ` + "`protobuf:\"varint,1,opt\"`" + `
}

type T struct{ x int }

func (t *T) A0() int  { return 0 }
func (t *T) A1() int  { return 1 }
func (t *T) A2() int  { return 2 }
func (t *T) A3() int  { return 3 }
func (t *T) A4() int  { return 4 }
func (t *T) A5() int  { return 5 }
func (t *T) A6() int  { return 6 }
func (t *T) A7() int  { return 7 }
func (t *T) A8() int  { return 8 }
func (t *T) A9() int  { return 9 }
func (t *T) A10() int { return 10 }
func (t *T) A11() int { return 11 }
func (t *T) A12() int { return 12 }
func (t *T) A13() int { return 13 }
func (t *T) A14() int { return 14 }
func (t *T) A15() int { return 15 }
func (t *T) A16() int { return 16 }
func (t *T) A17() int { return 17 }
func (t *T) A18() int { return 18 }
func (t *T) A19() int { return 19 }

// Multiple-func-plus-slice return: unsupported synth shape, like protobuf's
// legacy XXX_OneofFuncs. Never gets a native synth stub.
func (t *T) XXX_Wrappers() (func() int, []any) {
	return func() int { return 42 }, []any{(*W)(nil)}
}

func main() {
	rt := reflect.TypeOf(&T{})
	m, ok := rt.MethodByName("XXX_Wrappers")
	fmt.Println("found:", ok, "numIn:", m.Type.NumIn(), "numOut:", m.Type.NumOut())
	rets := m.Func.Call([]reflect.Value{reflect.Zero(m.Type.In(0))})
	vs, isAny := rets[1].Interface().([]any)
	fmt.Println("isAny:", isAny, "len:", len(vs))
	wt := reflect.TypeOf(vs[0]).Elem()
	fmt.Println("wrapper:", wt.Name(), "tag:", wt.Field(0).Tag.Get("protobuf"))

	// Method-set rules: a pointer-receiver method is hidden on the value type.
	_, valOK := reflect.TypeOf(T{}).MethodByName("XXX_Wrappers")
	fmt.Println("valueExposes:", valOK)
}
`
	want := "found: true numIn: 1 numOut: 2\nisAny: true len: 1\nwrapper: W tag: varint,1,opt\nvalueExposes: false\n"
	if got := evalOut(t, "reflect_type_methodbyname.go", src); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}

// reflect.SliceOf/MapOf/ArrayOf/ChanOf called from interpreted code over a synth
// (interpreted) element built a distinct rtype from the one mvm's materialize
// derives for the same composite literal: native reflect uses its own cache, mvm
// uses runtype's. So reflect.SliceOf(reflect.TypeOf((*X)(nil))) != reflect.TypeOf([]*X{}),
// which broke protobuf's extension converters (build.go reflect.SliceOf(goType) vs
// the user's []*X literal). interceptReflectCtor reroutes these to runtype.Derive*.
//
// PointerTo is NOT intercepted: rtype's PtrToThis back-pointer makes native
// reflect.PointerTo converge with the materialized *T on its own, so the pointer
// cases below must stay equal without interception.
func TestReflectDeriveDupConverges(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type X struct{ A int }

func (x *X) M() string { return "m" } // method-bearing -> synth rtype

type E int32

func (e E) String() string { return "e" }

func main() {
	// Slice of pointer-to-message: the protobuf extension case.
	litSlice := reflect.TypeOf([]*X{})
	ctorSlice := reflect.SliceOf(reflect.TypeOf((*X)(nil)))
	fmt.Println("slice:", litSlice == ctorSlice)

	// Slice whose element is itself a synth slice ([]E, E method-bearing): forces
	// the synth builder on BOTH the composite-literal (MkSlice) and reflect.SliceOf
	// sides, which diverge unless both route through runtype.Derive.
	litSynth := reflect.TypeOf([][]E{})
	ctorSynth := reflect.SliceOf(reflect.TypeOf([]E{}))
	fmt.Println("synthSlice:", litSynth == ctorSynth)

	// Map with synth key and elem.
	litMap := reflect.TypeOf(map[E]*X{})
	ctorMap := reflect.MapOf(reflect.TypeOf(E(0)), reflect.TypeOf((*X)(nil)))
	fmt.Println("map:", litMap == ctorMap)

	// Array of synth elem.
	litArr := reflect.TypeOf([3]*X{})
	ctorArr := reflect.ArrayOf(3, reflect.TypeOf((*X)(nil)))
	fmt.Println("array:", litArr == ctorArr)

	// Chan of synth elem.
	litChan := reflect.TypeOf(make(chan E))
	ctorChan := reflect.ChanOf(reflect.BothDir, reflect.TypeOf(E(0)))
	fmt.Println("chan:", litChan == ctorChan)

	// Pointer derivation already converges natively via PtrToThis; the value the
	// converter built (reflect.PtrTo) must equal the one reflect.New/NewAt yields.
	s := []E{}
	litPtr := reflect.TypeOf(&s)
	ctorPtr := reflect.PtrTo(reflect.TypeOf(s))
	newPtr := reflect.New(reflect.TypeOf(s)).Type()
	fmt.Println("ptr:", litPtr == ctorPtr && litPtr == newPtr)
}
`
	want := "slice: true\nsynthSlice: true\nmap: true\narray: true\nchan: true\nptr: true\n"
	if got := evalOut(t, "reflect_derive_dup.go", src); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}

// errors.AsType[*fs.PathError] instantiates the [E error] shim with a pointer
// to a bridged type. The constraint check runs before the type arg is
// materialized, so *fs.PathError had a nil Rtype (its bridged base sits on
// ElemType) and was wrongly rejected as not satisfying error. The expression
// path (TestExpr) materializes earlier and never hit this; only the full
// package/file path did, which is why this uses a complete program.
// argImplementsIface now materializes the arg before the reflect checks.
func TestAsTypeBridgedPtrConstraint(t *testing.T) {
	src := `package main

import (
	"errors"
	"fmt"
	"io/fs"
)

func main() {
	var err error = &fs.PathError{Op: "open", Path: "x", Err: fmt.Errorf("boom")}
	pe, ok := errors.AsType[*fs.PathError](err)
	fmt.Println(ok, pe.Path)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("astype.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "true x\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A native typed-nil pointer (reflect.Zero(mt).Interface()) asserted to an
// interpreted interface collapsed to a nil interface, so m == nil was true and a
// valid nil-receiver method call panicked. Was protobuf proto.Marshal via
// getMessageInfo.
func TestTypeAssertNativeNilPtrToInterface(t *testing.T) {
	const decl = `
		import "reflect"
		type I interface{ M() string }
		type T struct{ X int }
		func (t *T) M() string { if t == nil { return "nil-recv" }; return "non-nil" }
		func nilOf() I {
			m, ok := reflect.Zero(reflect.TypeOf((*T)(nil))).Interface().(I)
			if !ok { panic("assert failed") }
			return m
		}
	`
	run(t, []etest{
		{n: "nil_receiver_method", src: decl + `nilOf().M()`, res: "nil-recv"},
		{n: "not_equal_nil", src: decl + `nilOf() == nil`, res: "false"},
	})
}

// Setting an untyped string const into a named-string field of a native struct
// (reflect.StructField.Tag is reflect.StructTag) used to panic: reflect.Set
// rejects string as not assignable to reflect.StructTag. setFuncField now
// converts when the value differs from the field type only by name.
func TestNamedStringFieldSet(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

func main() {
	typ := reflect.StructOf([]reflect.StructField{
		{Name: "Height", Type: reflect.TypeOf(float64(0)), Tag: ` + "`json:\"height\"`" + `},
	})
	fmt.Println(typ.Field(0).Tag.Get("json"))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("named_string_field.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "height\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// An untyped const assigned through an index, map key, or pointer deref must
// adopt the element/key/pointee type (gonum/mat Permutation: `Data[i] = 1`
// into []float64 stored raw int bits, printing 5e-324). IndexAssign and
// DerefAssign now coerce operands like plain Assign does.
func TestIndexAssignConstConvert(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"slice", `f := func() float64 { d := make([]float64, 2); d[0] = 1; return d[0] }; f()`, "1"},
		{"array", `f := func() float64 { var a [2]float64; a[1] = 1; return a[1] }; f()`, "1"},
		{"computed index", `f := func() float64 { d := make([]float64, 4); i, v, s := 1, 0, 2; d[i*s+v] = 1; return d[2] }; f()`, "1"},
		{"map value", `f := func() float64 { m := map[string]float64{}; m["k"] = 1; return m["k"] }; f()`, "1"},
		{"map key", `f := func() string { m := map[float64]string{}; m[1] = "a"; return m[1.0] }; f()`, "a"},
		{"deref", `f := func() float64 { p := new(float64); *p = 1; return *p }; f()`, "1"},
		{"complex elem", `f := func() complex128 { d := make([]complex128, 1); d[0] = 1; return d[0] }; f()`, "(1+0i)"},
		{"typed var elem", `f := func() float64 { d := make([]float64, 1); n := 2; d[0] = float64(n); return d[0] }; f()`, "2"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			r, err := i.Eval(c.n, c.src)
			if err != nil {
				t.Fatalf("eval %q: %v", c.src, err)
			}
			if got := fmt.Sprintf("%v", r); got != c.res {
				t.Errorf("got %q, want %q", got, c.res)
			}
		})
	}
}

// Generic instance rtype must report Go's Foo[int], not mvm's Foo#int: xml truncates the element name at '['.
// Regression for encoding/xml TestMarshal/47 on wasm.
func TestReflectGenericInstanceName(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type Box[T any] struct{ X T }
type Pair[A, B any] struct{ X A; Y B }

func main() {
	fmt.Println(reflect.TypeOf(Box[int]{}).Name())
	fmt.Println(reflect.TypeOf(Pair[int, string]{}).Name())
}
`
	if got, want := evalOut(t, "generic_name.go", src), "Box[int]\nPair[int,string]\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A field promoted through an unexported embed stays reflect-reachable only if the embed reports Anonymous=true.
// StructOf refuses anon+PkgPath, so mvm sets the bit post-build.
// Regression for encoding/xml TestMarshal/64.
func TestReflectUnexportedEmbedAnonymous(t *testing.T) {
	src := `package main

import (
	"encoding/json"
	"fmt"
	"reflect"
)

type embedD struct {
	fieldD string
	FieldE string
}

type Outer struct {
	FieldA string
	embedD
}

func main() {
	f := reflect.TypeOf(Outer{}).Field(1)
	fmt.Println(f.Name, f.Anonymous, f.IsExported())
	b, _ := json.Marshal(Outer{FieldA: "a", embedD: embedD{FieldE: "e"}})
	fmt.Println(string(b))
}
`
	want := "embedD true false\n" + `{"FieldA":"a","FieldE":"e"}` + "\n"
	if got := evalOut(t, "unexported_embed.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// SetIterValue from a map whose elem is a synth iface must store mvm eface
// form: the native write packed {synth iface rtype, pointer into the bucket},
// so a later delete (or growth) nulled the read-out value. Broke gonum
// graph/simple TestDirected/RemoveNodes via gonum's safe map iterator.
func TestSynthMapIterValueDetached(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type Node interface{ ID() int64 }

type node int64

func (n node) ID() int64 { return int64(n) }

func main() {
	m := map[int64]Node{1: node(1), 2: node(2)}
	var it reflect.MapIter
	it.Reset(reflect.ValueOf(m))
	var curr Node
	val := reflect.ValueOf(&curr).Elem()
	it.Next()
	val.SetIterValue(&it)
	got := curr.ID()
	delete(m, 3-got) // other key
	fmt.Println(curr.ID() == got)
	delete(m, got) // own key: bucket slot cleared
	fmt.Println(curr.ID() == got)
}
`
	if got, want := evalOut(t, "synth_mapiter_value.go", src), "true\ntrue\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// Interpreted named types must surface PkgPath().
// Regression for encoding/gob TestRegistrationNaming (gob.Register keys on it).
func TestReflectNamedTypePkgPath(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type S struct{ X int }

type MyInt int

type M struct{ Y int }

func (m M) Get() int { return m.Y }

func main() {
	for _, v := range []any{S{}, MyInt(0), M{}} {
		rt := reflect.TypeOf(v)
		fmt.Println(rt.Name(), rt.PkgPath(), rt.String())
	}
}
`
	want := "S main main.S\nMyInt main main.MyInt\nM main main.M\n"
	if got := evalOut(t, "named_pkgpath.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
