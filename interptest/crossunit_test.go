package interptest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/internal/derive"
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/stdlib/stdmod"
	"github.com/mvm-sh/mvm/vm"
)

func TestClosurePkgKeyCollision(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/check",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/check\n",
			"check.go": `package check

func panics(f func()) (b bool) {
	defer func() {
		b = recover() != nil
	}()
	f()
	return
}

func Probe() bool { return panics(func() { panic("x") }) }
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/check"
)

func panics(fn func()) (panicked bool, message string) {
	defer func() {
		r := recover()
		panicked = r != nil
		message = fmt.Sprint(r)
	}()
	fn()
	return
}

func main() {
	p, m := panics(func() { panic("boom") })
	fmt.Println(check.Probe(), p, m)
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
	if got, want := stdout.String(), "true true boom\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
	if s := stderr.String(); strings.Contains(s, "panic") {
		t.Errorf("stderr: %s", s)
	}
}

func TestCrossUnitFuncValueAddress(t *testing.T) {
	i := newAutoImportInterp(t)
	// Unit 1: an init func leaves init-call shims in m.code after the run.
	if _, err := i.Eval("unit1", `var inited int; func init() { inited = 7 }; func a() int { return inited }`); err != nil {
		t.Fatalf("unit1: %v", err)
	}
	// Unit 2: define a func, take its value, and call through it. With the
	// shims left dangling this called the wrong address and crashed.
	r, err := i.Eval("unit2", `func b(x int) int { return x*x + 1 }; fn := b; fn(6)`)
	if err != nil {
		t.Fatalf("unit2: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got != "37" {
		t.Errorf("fn(6) = %q, want 37", got)
	}
}

func TestExternalUnitFuncShadowsTargetAlias(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/buf",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/buf\n",
			"buf.go": `package buf

func newBuffer() int { return -1 }

func Pub() int { return newBuffer() }
`,
		},
	})
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.AutoImportPackages()

	// Unit 1: load X. aliasTargetTopLevel leaves a bare `newBuffer` alias.
	if _, err := i.Eval("example.com/x/buf", ""); err != nil {
		t.Fatalf("load target: %v", err)
	}
	i.PublishCompiledPackage("example.com/x/buf")

	// Unit 2 (the external-test stand-in): redeclare newBuffer with real params
	// and call it. Without shadowing this is "undefined: x".
	r, err := i.Eval("buf_test", `func newBuffer(x int) int { return x * 10 }; newBuffer(5)`)
	if err != nil {
		t.Fatalf("external unit: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got != "50" {
		t.Errorf("newBuffer(5) = %q, want 50", got)
	}
}

// An external X_test unit declaring a top-level var whose name matches an
// unexported var of the imported target must get its own slot, not reuse the
// target's leaked bare alias. Repro of `mvm test go/format` on wasm: interpreted
// go/format's `var tests = []string` collided with format_test's differently
// typed `var tests = []struct{...}`, panicking reflect.Set at init.
func TestExternalUnitVarShadowsTargetAlias(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/tv",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/tv\n",
			"tv.go": `package tv

var tests = []string{"a", "b", "c"}

func Count() int { return len(tests) }
`,
		},
	})
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.AutoImportPackages()

	if _, err := i.Eval("example.com/x/tv", ""); err != nil {
		t.Fatalf("load target: %v", err)
	}
	i.PublishCompiledPackage("example.com/x/tv")

	r, err := i.Eval("tv_test", `var tests = []struct{ name string; n int }{{"x", 7}}; tests[0].n`)
	if err != nil {
		t.Fatalf("external unit: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got != "7" {
		t.Errorf("tests[0].n = %q, want 7", got)
	}
}

func TestImportedForwardRefVarOrder(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/fwd",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/fwd\n",
			"fwd.go": `package fwd

type T struct{ name string }

var Ptr *T = &c

var c = T{name: "ibm"}

func (t *T) Name() string { return t.name }
`,
		},
	})

	// The importer declares a top-level ` + "`name`" + ` that collides with the
	// composite field key in fwd.c, reproducing the spurious-edge flip.
	src := `package main

import (
	"fmt"
	"example.com/x/fwd"
)

var name = "importer"

func main() {
	fmt.Println(fwd.Ptr.Name(), name)
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
	if got, want := stdout.String(), "ibm importer\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestDotImportedFuncCall pins the logrus hook_test crash: a bare-name call to a
// dot-imported func from a freshly imported interpreted package jumped to a
// never-filled global (the dot-import froze the func's not-yet-assigned slot).
func TestDotImportedFuncCall(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/dotfn",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/dotfn\n",
			"dotfn.go": `package dotfn

func Apply(n int, f func(int) int) int { return f(n) }
`,
		},
	})

	src := `package main

import (
	"fmt"
	. "example.com/x/dotfn"
)

func main() {
	fmt.Println(Apply(21, func(x int) int { return x * 2 }))
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
	if got, want := stdout.String(), "42\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestImportedUntypedConstFloatField pins the gonum/plot axis-title bug.
// A re-exported untyped int const folded to float64 in its owning package must
// not retype the SHARED symbol, else a later assign of it to a float64 field
// stores int bits (5e-324) not 1.0. text defines it, draw re-exports it (nulling
// Cval), box folds it in a float switch; pre-fix this corrupted draw.Right.
func TestImportedUntypedConstFloatField(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/g",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/g\n",
			"text/text.go": `package text

const (
	Center = 0
	Right  = +1
)
`,
			"draw/draw.go": `package draw

import "example.com/x/g/text"

const (
	Center = text.Center
	Right  = text.Right
)
`,
			"box/box.go": `package box

import "example.com/x/g/draw"

type Box struct{ Pos float64 }

func classify(p float64) int {
	switch p {
	case draw.Center:
		return 0
	case draw.Right:
		return 1
	}
	return -1
}

func Classify(b Box) int { return classify(b.Pos) }
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/g/box"
	"example.com/x/g/draw"
)

func main() {
	var b box.Box
	b.Pos = draw.Right // the field assign that mis-stored pre-fix
	fmt.Printf("%v %d", b.Pos, box.Classify(b))
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
	// 1 = converted value (not 5e-324); Classify hits the Right case.
	if got, want := stdout.String(), "1 1"; got != want {
		t.Errorf("stdout: got %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

func TestImportedUnexportedConstRejected(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/sec",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/sec\n",
			"core/core.go": `package core

const secret = 99
const Public = 7
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/sec/core"
)

const X = core.secret

func main() { fmt.Println(X) }
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	// gc rejects this outright; mvm either errors or (pre-existing leniency for an
	// undefined const) registers a nil stub. Either is fine -- the guarantee is that
	// the unexported value 99 must NOT leak through.
	_, err := i.Eval("test", src)
	if err == nil && strings.Contains(stdout.String(), "99") {
		t.Fatalf("unexported core.secret leaked its value: stdout %q", stdout.String())
	}
}

func TestReExportedTypedConstKeepsNamedType(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/lvl",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/lvl\n",
			"core/core.go": `package core

type Level int8

const (
	DebugLevel Level = iota - 1
	InfoLevel
)

func (l Level) Enabled(lvl Level) bool { return lvl >= l }

type Enabler interface{ Enabled(Level) bool }
`,
			"mid/mid.go": `package mid

import "example.com/x/lvl/core"

const DebugLevel = core.DebugLevel

func Box(e core.Enabler) core.Enabler { return e }
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/lvl/core"
	"example.com/x/lvl/mid"
)

func main() {
	e := mid.Box(mid.DebugLevel) // boxing the re-export into the interface
	fmt.Printf("%T %v", mid.DebugLevel, e.Enabled(core.InfoLevel))
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
	if got, want := stdout.String(), "core.Level true"; got != want {
		t.Errorf("stdout: got %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

// TestExportedConstUnexportedTypeThroughIfaceField guards the jwt/v5 none case:
// an exported const of an UNEXPORTED named type, stored cross-package into an
// `any` struct field, must keep its named type so a type assertion back to that
// type (inside the owning package) still matches. A typed const's runtime value
// carries only the underlying rtype, so unboxing the Iface into a native eface
// must adopt the named rtype (unboxIfaceFor).
func TestExportedConstUnexportedTypeThroughIfaceField(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/none",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/none\n",
			"none.go": `package none

type magicConst string

const Allow magicConst = "allow"

func Check(key any) bool {
	_, ok := key.(magicConst)
	return ok
}
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/none"
)

type holder struct{ key any }

func main() {
	h := holder{key: none.Allow} // const into an any field of a composite literal
	fmt.Printf("%v", none.Check(h.key))
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
	if got, want := stdout.String(), "true"; got != want {
		t.Errorf("stdout: got %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}

func TestFuncNamesMatchesGoIsTest(t *testing.T) {
	i := interp.NewInterpreter(golang.GoSpec)
	src := `func Test() {}
func TestFoo() {}
func Test1() {}
func Test_helper() {}
func Testify() {}
func Testable() {}
func Benchmark() {}
func helper() {}`
	if _, err := i.Eval("u_test.go", src); err != nil {
		t.Fatalf("eval: %v", err)
	}
	// A Test-named func outside a _test.go file is not a test (fstest.TestFS).
	if _, err := i.Eval("lib.go", "func TestExportedHelper() {}"); err != nil {
		t.Fatalf("eval: %v", err)
	}

	got := i.FuncNames("Test")
	want := []string{"Test", "TestFoo", "Test1", "Test_helper"}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("FuncNames(%q) missing %q; got %v", "Test", w, got)
		}
	}
	for _, bad := range []string{"Testify", "Testable", "TestExportedHelper"} {
		if slices.Contains(got, bad) {
			t.Errorf("FuncNames(%q) must not include lower-continuation %q; got %v", "Test", bad, got)
		}
	}

	if b := i.FuncNames("Benchmark"); !slices.Contains(b, "Benchmark") {
		t.Errorf("FuncNames(%q) must include the exact-prefix name %q; got %v", "Benchmark", "Benchmark", b)
	}
}

func TestFuncTypeParamNameNoLeak(t *testing.T) {
	src := `package main

import "fmt"

type deb struct {
	callbacks []func(key string, count int)
}

func (d *deb) reset(key string) {
	// The func-type param name 'key' collides with the method param 'key'.
	callbacks := append([]func(key string, count int){}, d.callbacks...)
	for i := range callbacks {
		callbacks[i](key, i+1)
	}
}

// A func-type alias whose param name also collides with an outer param.
func run(key string, f func(key string, n int)) {
	type handler func(key string, n int)
	var h handler = f
	h(key, 7)
}

func main() {
	d := &deb{callbacks: []func(key string, count int){
		func(userID string, count int) { fmt.Println("cb1", userID, count) },
		func(userID string, count int) { fmt.Println("cb2", userID, count) },
	}}
	d.reset("samuel")
	run("john", func(userID string, n int) { fmt.Println("run", userID, n) })
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("functype_leak.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "cb1 samuel 1\ncb2 samuel 2\nrun john 7\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

// TestImportedMethodEmbedJSONFlattens pins the jwt RegisteredClaims bug.
// A struct embedding a method-bearing foreign struct as a NON-first field must
// still flatten in encoding/json (marshal and unmarshal) and promote its method.
// reflect.StructOf cannot promote a method-bearing embed off field 0, so mvm
// gave the embed a methodless layout-equivalent to keep it Anonymous; before the
// fix the embed was demoted to a named field ("Registered":{...} not flattened).
func TestImportedMethodEmbedJSONFlattens(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/claims",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/claims\n",
			"claims.go": "package claims\n\n" +
				"type Registered struct {\n" +
				"\tIssuer string `json:\"iss,omitempty\"`\n" +
				"\tExpiry int64  `json:\"exp,omitempty\"`\n" +
				"}\n\n" +
				"func (r Registered) GetIssuer() string { return r.Issuer }\n",
		},
	})

	src := `package main

import (
	"encoding/json"
	"fmt"

	"example.com/x/claims"
)

type My struct {
	Foo string ` + "`json:\"foo\"`" + `
	claims.Registered
}

func main() {
	m := My{Foo: "bar", Registered: claims.Registered{Issuer: "test", Expiry: 1516239022}}
	b, _ := json.Marshal(m)
	var out My
	json.Unmarshal(b, &out)
	fmt.Printf("%s|%s|%s", string(b), out.Issuer, out.GetIssuer())
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
	want := `{"foo":"bar","iss":"test","exp":1516239022}|test|test`
	if got := stdout.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s\nstderr: %s", got, want, stderr.String())
	}
}

// TestImportedPtrMethodEmbedJSONFlattens covers the pointer-embed variant:
// a *T embed where T has pointer-receiver methods, as a non-first field.
// reflect.StructOf reads the pointee's uncommon method count (nonzero on a synth
// rtype even when NumMethod reports 0), so methodlessLayout must rebuild the
// pointee layout-only, not trust NumMethod.
func TestImportedPtrMethodEmbedJSONFlattens(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/pclaims",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/pclaims\n",
			"pclaims.go": "package pclaims\n\n" +
				"type Registered struct {\n" +
				"\tIssuer string `json:\"iss,omitempty\"`\n" +
				"}\n\n" +
				"func (r *Registered) GetIssuer() string { return r.Issuer }\n",
		},
	})

	src := `package main

import (
	"encoding/json"
	"fmt"

	"example.com/x/pclaims"
)

type My struct {
	Foo string ` + "`json:\"foo\"`" + `
	*pclaims.Registered
}

func main() {
	m := My{Foo: "bar", Registered: &pclaims.Registered{Issuer: "test"}}
	b, _ := json.Marshal(m)
	fmt.Printf("%s|%s", string(b), m.GetIssuer())
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
	want := `{"foo":"bar","iss":"test"}|test`
	if got := stdout.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s\nstderr: %s", got, want, stderr.String())
	}
}

func TestPromotedMethodSameNameOtherPkg(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/protos",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/protos\n",
			"protos.go": `package protos

type Stringer struct{ X string }

func (s *Stringer) String() string { return s.X }

type Germ struct {
	Stringer
}
`,
		},
	})
	src := `package main

import (
	"fmt"

	"example.com/x/protos"
)

type Stringer string

func (s Stringer) String() string { return string(s) }

func main() {
	g := &protos.Germ{Stringer: protos.Stringer{X: "germ1"}}
	fmt.Println(g.String(), Stringer("ok").String())
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("main.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "germ1 ok\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRedeclareAsImport(t *testing.T) {
	run(t, []etest{
		{n: "var_vs_import", src: `import "sort"; var sort = 1; func run() int { return 0 }; run()`, err: "redeclared in this block"},
		{n: "const_vs_import", src: `import "sort"; const sort = 1; func run() int { return 0 }; run()`, err: "redeclared in this block"},
		{n: "type_vs_import", src: `import "sort"; type sort = int; func run() int { return 0 }; run()`, err: "redeclared in this block"},
		{n: "func_vs_import", src: `import "sort"; func sort() {}; func run() int { return 0 }; run()`, err: "redeclared in this block"},
		{n: "grouped_var_vs_import", src: `import "sort"; var ( a = 1; sort = 2 ); func run() int { return a }; run()`, err: "redeclared in this block"},

		// Valid Go: a local name shadows the imported package -- distinct scoped
		// key, must resolve to the local, never trip the check.
		{n: "local_shadow_ok", src: `import "sort"; func run() int { sort := 42; return sort }; run()`, res: "42"},
	})
}

func TestVarNamedLikeOwnPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "selfname"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package selfname

import "sort"

var selfname = sort.SearchInts([]int{1, 2, 3}, 2)

func Value() int { return selfname }
`
	if err := os.WriteFile(filepath.Join(dir, "selfname", "selfname.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetPkgfs(dir)
	r, err := i.Eval("t", `import "selfname"; selfname.Value()`+"\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := r.Interface(); got != 1 {
		t.Fatalf("selfname.Value() = %v, want 1", got)
	}
}

func TestForwardTypeBaseShadowsAutoImport(t *testing.T) {
	for _, name := range []string{"syntax", "bytes", "sort"} {
		t.Run(name, func(t *testing.T) {
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			intp.AutoImportPackages() // ambient-bind the short name as a Pkg symbol
			// Outer's base is declared AFTER Outer, so the base is a forward ref.
			src := "type Outer " + name + "; type " + name + " int8; func run() int { var v Outer = 2; return int(v) }; run()\n"
			r, err := intp.Eval("t", src)
			if err != nil {
				t.Fatalf("forward type base %q: unexpected error %v", name, err)
			}
			if got := r.Interface(); got != 2 {
				t.Fatalf("%s: got %v, want 2", name, got)
			}
		})
	}
}

func TestRedeclareVsAutoImport(t *testing.T) {
	for _, name := range []string{"sort", "bytes", "time"} {
		t.Run(name, func(t *testing.T) {
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			intp.AutoImportPackages() // ambient-bind sort/bytes/time as Pkg symbols
			if _, err := intp.Eval("t", "var "+name+" = 42\n"); err != nil {
				t.Fatalf("var %s = 42 with no explicit import: unexpected error %v", name, err)
			}
			r, err := intp.Eval("t2", name+"\n")
			if err != nil {
				t.Fatalf("read back %s: %v", name, err)
			}
			if got := r.Interface(); got != 42 {
				t.Fatalf("%s = %v, want 42", name, got)
			}
		})
	}
}

func TestEvalRollback_GenericReuseAfterError(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("e1", `var p atomic.Pointer[int]; var bad = undefXYZ; _ = p`); err == nil {
		t.Fatal("eval1 expected to fail on undefXYZ")
	}
	if _, err := i.Eval("e2", `var q atomic.Pointer[int]; x := 9; q.Store(&x); println(*q.Load())`); err != nil {
		t.Fatalf("eval2 reusing atomic.Pointer[int] after eval1 error: %v", err)
	}
}

func TestEvalRollback_PlainEvalAfterError(t *testing.T) {
	i := newAutoImportInterp(t)
	_, _ = i.Eval("e1", `var p atomic.Pointer[int]; var bad = undefXYZ; _ = p`)
	r, err := i.Eval("e2", `2 + 3`)
	if err != nil {
		t.Fatalf("eval2 after eval1 error: %v", err)
	}
	if got := r.Interface(); got != 5 {
		t.Fatalf("eval2 = %v, want 5", got)
	}
}

func TestEvalRollback_KeepsPriorGoodState(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("e1", `func add(a, b int) int { return a + b }`); err != nil {
		t.Fatalf("eval1: %v", err)
	}
	if _, err := i.Eval("e2", `var bad = undefXYZ`); err == nil {
		t.Fatal("eval2 expected to fail")
	}
	r, err := i.Eval("e3", `add(2, 40)`)
	if err != nil {
		t.Fatalf("eval3 using add from eval1 after eval2 error: %v", err)
	}
	if got := r.Interface(); got != 42 {
		t.Fatalf("eval3 = %v, want 42", got)
	}
}

func newExtUnitInterp(t *testing.T, files map[string]string) (*interp.Interp, *bytes.Buffer) {
	t.Helper()
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/lvl",
		version: "v1.0.0",
		files:   files,
	})
	var stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	mfs := modfs.New(modfs.Options{Proxy: url})
	if err := mfs.Inject(stdmod.ModulePath, stdmod.Version, stdlib.EmbeddedStd()); err != nil {
		t.Fatalf("inject std: %v", err)
	}
	i.SetStdlibFS(stdmod.FS(mfs))
	i.SetRemoteFS(mfs)
	i.SetIncludeTests(true)
	if _, err := i.Eval("example.com/x/lvl", ""); err != nil {
		t.Fatalf("load target: %v\nstderr: %s", err, stderr.String())
	}
	i.PublishCompiledPackage("example.com/x/lvl")
	return i, &stderr
}

const lvlSrc = `package lvl

type Level uint32

const (
	PanicLevel Level = iota
	FatalLevel
	ErrorLevel
	WarnLevel
	InfoLevel
)

func (level *Level) UnmarshalText(text []byte) error {
	*level = WarnLevel
	return nil
}

var std Level

func SetLevel(level Level) { std = level }

func GetLevel() Level { return std }
`

func TestExtUnitSynthAttachedMethodStaysInterpreted(t *testing.T) {
	i, stderr := newExtUnitInterp(t, map[string]string{
		"go.mod": "module example.com/x/lvl\n",
		"lvl.go": lvlSrc,
		"lvl_internal_test.go": `package lvl

import "testing"

func TestInternal(t *testing.T) {}
`,
		"lvl_test.go": `package lvl_test

import (
	"testing"

	log "example.com/x/lvl"
)

var _ = func() bool {
	var cmp log.Level
	if err := cmp.UnmarshalText([]byte("w")); err != nil {
		panic(err)
	}
	if cmp != log.WarnLevel {
		panic("ptr-recv write-back lost: method dispatched on a boxed copy")
	}
	return true
}()

func TestExternal(t *testing.T) {}
`,
	})
	if _, err := i.EvalFiles(i.ExternalTestSources()); err != nil {
		t.Fatalf("external unit: %v\nstderr: %s", err, stderr.String())
	}
}

func TestExtUnitDotImportInterpretedFunc(t *testing.T) {
	i, stderr := newExtUnitInterp(t, map[string]string{
		"go.mod": "module example.com/x/lvl\n",
		"lvl.go": lvlSrc,
		"lvl_internal_test.go": `package lvl

import "testing"

func TestInternal(t *testing.T) {}
`,
		"lvl_test.go": `package lvl_test

import (
	"testing"

	. "example.com/x/lvl"
)

var _ = func() bool {
	SetLevel(WarnLevel)
	if uint32(GetLevel()) != 3 {
		panic("dot-imported SetLevel/GetLevel misbound")
	}
	return true
}()

func TestExternal(t *testing.T) {}
`,
	})
	if _, err := i.EvalFiles(i.ExternalTestSources()); err != nil {
		t.Fatalf("external unit: %v\nstderr: %s", err, stderr.String())
	}
}

func TestExtUnitConstSlotRollback(t *testing.T) {
	i, stderr := newExtUnitInterp(t, map[string]string{
		"go.mod": "module example.com/x/lvl\n",
		"lvl.go": lvlSrc,
		"lvl_internal_test.go": `package lvl

import "testing"

func TestInternal(t *testing.T) {}
`,
		"a_bad_test.go": `package lvl_test

import (
	"testing"

	log "example.com/x/lvl"
)

// Extra decls so the failed attempt allocates Data slots the retry will not,
// leaving the const's recorded slot index aliased to something else.
var s1 = "bad-one"
var s2 = "bad-two"
var s3 = "bad-three"
var _ = log.WarnLevel

func TestBad(t *testing.T) {
	undefinedSymbol()
}
`,
		"z_good_test.go": `package lvl_test

import (
	"testing"

	log "example.com/x/lvl"
)

var pad1 = "pad-one"
var pad2 = "pad-two"
var pad3 = "pad-three"

var _ = func() bool {
	_ = pad1 + pad2 + pad3
	log.SetLevel(log.WarnLevel)
	if uint32(log.GetLevel()) != 3 {
		panic("const slot corrupted after failed-unit rollback")
	}
	return true
}()

func TestGood(t *testing.T) {}
`,
	})
	ext := i.ExternalTestSources()
	if len(ext) != 2 {
		t.Fatalf("want 2 external test files, got %d", len(ext))
	}
	if _, err := i.EvalFiles(ext); err == nil {
		t.Fatal("unit with a_bad_test.go should fail to compile")
	}
	// Mimic loadExternalUnit: drop the failing file and retry on the same interp.
	var good []goparser.PackageSource
	for _, s := range ext {
		if !strings.Contains(s.Name, "a_bad_test.go") {
			good = append(good, s)
		}
	}
	if _, err := i.EvalFiles(good); err != nil {
		t.Fatalf("retry without bad file: %v\nstderr: %s", err, stderr.String())
	}
}

func TestExtUnitCallerQualification(t *testing.T) {
	i, stderr := newExtUnitInterp(t, map[string]string{
		"go.mod": "module example.com/x/lvl\n",
		"lvl.go": lvlSrc,
		"lvl_internal_test.go": `package lvl

import "testing"

func TestInternal(t *testing.T) {}
`,
		"lvl_test.go": `package lvl_test

import (
	"runtime"
	"strings"
	"testing"
)

func callerName() string {
	pc, _, _, ok := runtime.Caller(0)
	if !ok {
		panic("runtime.Caller failed")
	}
	return runtime.FuncForPC(pc).Name()
}

func walkNames() string {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(0, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	names := ""
	for f, more := frames.Next(); more; f, more = frames.Next() {
		names += f.Function + "|"
	}
	return names
}

var _ = func() bool {
	if got := callerName(); !strings.HasPrefix(got, "example.com/x/lvl_test.") {
		panic("unqualified external-test caller name: " + got)
	}
	if !strings.Contains(walkNames(), ".init|") {
		panic("frames.Next more-idiom dropped the outermost interpreted frame (missing goexit sentinel): " + walkNames())
	}
	return true
}()

func TestExternal(t *testing.T) {}
`,
	})
	if _, err := i.EvalFiles(i.ExternalTestSources()); err != nil {
		t.Fatalf("external unit: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRecompileForLoopClosureFuncexit(t *testing.T) {
	src := `package main

type T struct{ name string }

func (t *T) Run(name string, f func(t *T)) bool {
	f(&T{name: name})
	return true
}

func Driver(r *T) {
	cases := []struct {
		skip bool
		want int
	}{
		{skip: false, want: 3},
		{skip: true, want: 4},
	}
	for _, tc := range cases {
		r.Run("", func(r *T) {
			if tc.skip {
				return
			}
			w := tc.want
			_ = w + tc.want
		})
	}
}

func main() {}
`
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	files := []goparser.PackageSource{{Name: "main.go", Content: src}}
	for round := 0; round < 3; round++ {
		if err := i.CompileFiles(files); err != nil {
			t.Fatalf("compile round %d: %v", round, err)
		}
	}
	// Every recompile must name the loop scope "Driver/for0..."; a drift desyncs
	// the closure's captured-variable references.
	for k := range i.Symbols {
		if strings.HasPrefix(k, "Driver/for") && !strings.HasPrefix(k, "Driver/for0") {
			t.Errorf("loop scope name drifted across recompiles: %q", k)
		}
	}
}

func TestRecompileFuncSkipJumpSkipsBody(t *testing.T) {
	src := `package main

func Helper(n int) int {
	s := 0
	for i := 0; i < n; i++ {
		s += i * 2
		s -= i
	}
	return s
}

var registered = Helper(3)

func main() { _ = registered }
`
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	files := []goparser.PackageSource{{Name: "main.go", Content: src}}
	// First compile commits its labels (only compile errors roll back); the
	// recompile must not reuse their stale addresses.
	if err := i.CompileFiles(files); err != nil {
		t.Fatalf("compile round 0: %v", err)
	}
	mark := len(i.FuncRanges)
	if err := i.CompileFiles(files); err != nil {
		t.Fatalf("compile round 1: %v", err)
	}
	for _, fr := range i.FuncRanges[mark:] {
		if fr.Start == 0 || i.Code[fr.Start-1].Op != vm.Jump {
			continue
		}
		if target := fr.Start - 1 + int(i.Code[fr.Start-1].A); target < fr.End {
			t.Errorf("func %q skip jump at %d targets %d, inside body [%d,%d): stale label offset",
				fr.Name, fr.Start-1, target, fr.Start, fr.End)
		}
	}
}

func TestRecompileClosureParam(t *testing.T) {
	src := `package main

type M struct{ v int }

func (m *M) Inc() { m.v++ }

func unknown() func(m *M) {
	return func(m *M) {
		m.Inc()
	}
}

func main() { _ = unknown() }
`
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	files := []goparser.PackageSource{{Name: "main.go", Content: src}}
	for round := 0; round < 3; round++ {
		if err := i.CompileFiles(files); err != nil {
			t.Fatalf("compile round %d: %v", round, err)
		}
	}
}

func TestExternalTestVarNameMatchesPkgType(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/rmod",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/rmod\n",
			"rmod.go": `package rmod

// rng (unexported) shares its name with the external test's local var.
type rng interface{ Int63n(n int64) int64 }

var _ rng

func Stamp() uint64 { return 1 }
`,
			"rmod_test.go": `package rmod_test

import (
	"math/rand"
	"testing"
	"time"

	"example.com/x/rmod"
)

func TestRng(t *testing.T) {
	_ = rmod.Stamp()
	var rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	var buf [8]byte
	if n, err := rng.Read(buf[:]); err != nil || n != 8 {
		t.Fatalf("rng.Read: n=%d err=%v (var rng mis-typed by package type rng)", n, err)
	}
}
`,
		},
	})

	var stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	mfs := modfs.New(modfs.Options{Proxy: url})
	if err := mfs.Inject(stdmod.ModulePath, stdmod.Version, stdlib.EmbeddedStd()); err != nil {
		t.Fatalf("inject std: %v", err)
	}
	i.SetStdlibFS(stdmod.FS(mfs))
	i.SetRemoteFS(mfs)
	i.SetIncludeTests(true)

	// Loading the target compiles its external `package X_test` sources; before
	// the fix this failed with "undefined: Read" (rng resolved to the type).
	if _, err := i.Eval("example.com/x/rmod", ""); err != nil {
		t.Fatalf("load target: %v\nstderr: %s", err, stderr.String())
	}
	i.PublishCompiledPackage("example.com/x/rmod")
	if _, err := i.EvalFiles(i.ExternalTestSources()); err != nil {
		t.Fatalf("load external tests: %v\nstderr: %s", err, stderr.String())
	}
}

func TestMethodBearingRtypeSharedWithinMachine(t *testing.T) {
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	const src = `
		type T struct {
			A string
			B []byte
		}
		func (m *T) M() string { return m.A }
	`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	sym, ok := i.Symbols["T"]
	if !ok || sym.Type == nil || sym.Type.Rtype == nil {
		t.Fatal("materialized type T not found")
	}
	t1 := sym.Type

	// A second pass's fresh, still-symbolic *Type for the same Go type: same
	// name/layout/methods, no rtype yet.
	t2v := *t1
	t2 := &t2v
	t2.Rtype = nil

	prev := vm.SetActiveMachine(i.Machine)
	defer vm.SetActiveMachine(prev)
	rt2 := derive.MaterializeRtype(t2)
	if rt2 == nil {
		t.Fatal("second-pass materialize returned nil")
	}
	if rt2 != t1.Rtype {
		t.Fatalf("method-bearing type T got a distinct rtype on the second pass: %p vs %p (cross-Eval dup)", rt2, t1.Rtype)
	}
}

func TestExtUnitScopedLocalNoCrossUnitLeak(t *testing.T) {
	files := map[string]string{
		"go.mod": "module example.com/x/lvl\n",
		"lvl.go": `package lvl

type Target struct{ URL string }

type Resolver interface {
	Close()
}

func mk() Resolver { return nil }

// New declares an interface-typed local r in its first if-block, registering
// the scoped key "New/if0/r" during unit 1.
func New() Resolver {
	if r := mk(); r != nil {
		return r
	}
	return nil
}

func F() int { return 1 }
`,
		"lvl_internal_test.go": `package lvl

import "testing"

func TestInternal(t *testing.T) {}
`,
		"dep/dep.go": `package dep

import (
	"net/url"
	"sync"

	"example.com/x/lvl"
)

var HookFromEnv = defaultHook

func defaultHook() (*url.URL, error) { return nil, nil }

type nopResolver struct{}

func (nopResolver) Close() {}

type delegatingResolver struct {
	target         lvl.Target
	proxyURL       *url.URL
	mu             sync.Mutex
	targetResolver lvl.Resolver
	proxyResolver  lvl.Resolver
}

// New reads r.proxyURL in its first if-block (scope "New/if0"); r is the
// func-level var, so resolution of r there must not fall through to a stale
// "New/if0/r" left by lvl.New in unit 1.
func New(t lvl.Target) bool {
	r := &delegatingResolver{
		target:         t,
		proxyResolver:  nopResolver{},
		targetResolver: nopResolver{},
	}
	if r.proxyURL == nil {
		return true
	}
	return false
}
`,
		"lvl_test.go": `package lvl_test

import (
	"net/url"
	"testing"

	"example.com/x/lvl"
	"example.com/x/lvl/dep"
)

func TestExternal(t *testing.T) {
	dep.HookFromEnv = func() (*url.URL, error) { return nil, nil }
	if ok := dep.New(lvl.Target{URL: "x"}); !ok {
		t.Fatalf("New: ok=%v", ok)
	}
}
`,
	}
	url, _ := startFakeProxy(t, remoteModule{path: "example.com/x/lvl", version: "v1.0.0", files: files})
	var stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	mfs := modfs.New(modfs.Options{Proxy: url})
	if err := mfs.Inject(stdmod.ModulePath, stdmod.Version, stdlib.EmbeddedStd()); err != nil {
		t.Fatalf("inject std: %v", err)
	}
	i.SetStdlibFS(stdmod.FS(mfs))
	i.SetRemoteFS(mfs)
	i.SetIncludeTests(true)
	if _, err := i.Eval("example.com/x/lvl", ""); err != nil {
		t.Fatalf("load target: %v\nstderr: %s", err, stderr.String())
	}
	i.PublishCompiledPackage("example.com/x/lvl")
	if _, err := i.EvalFiles(i.ExternalTestSources()); err != nil {
		t.Fatalf("external unit: %v\nstderr: %s", err, stderr.String())
	}
}

// A func-typed GLOBAL slot is an interface{} box like a local one, so a plain
// Addr on it pushed a *interface{} and `var StatP = &stat` failed its init
// (filepath export_test's LstatP = &lstat). AddrGlobal must retype the slot to
// the declared *func, and an external unit's write through it must reach the
// target's own calls.
func TestAddrOfGlobalFuncVar(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/lp",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/lp\n",
			// The pointer decl parses before the target var (export_test.go
			// sorts before path.go in the filepath original).
			"a_export.go": `package lp

var StatP = &stat
`,
			"z_impl.go": `package lp

var stat = func(path string) (string, error) { return "real:" + path, nil }

func Call(p string) (string, error) { return stat(p) }
`,
		},
	})
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.AutoImportPackages()
	if _, err := i.Eval("example.com/x/lp", ""); err != nil {
		t.Fatalf("load: %v", err)
	}
	i.PublishCompiledPackage("example.com/x/lp")

	r, err := i.Eval("lp_test", `
import "example.com/x/lp"

*lp.StatP = func(path string) (string, error) { return "fake:" + path, nil }
s, _ := lp.Call("x")
s`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got != "fake:x" {
		t.Errorf("got %q, want fake:x", got)
	}
}
