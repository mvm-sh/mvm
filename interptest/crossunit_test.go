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
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/stdlib/stdmod"
	"github.com/mvm-sh/mvm/vm"
)

// Two packages each declare a same-named func containing a closure: the
// closures share the anon name "#panics.func1", and a bare symbol-table key
// made the second parse reuse the first package's closure Symbol, leaking its
// FreeVars (gonum/mat's panics vs blas/testblas's panics: "undefined:
// panics/b"). Closure symbols are now keyed per-package (anonFuncKey).
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

// TestCrossUnitFuncValueAddress guards code/address alignment across successive
// top-level Evals. Each Eval leaves its init/main call shims in the VM code;
// those shims are not in the compiler's Code, so a later Eval's code must be
// trimmed back into alignment before being pushed. Otherwise a function defined
// in the later unit and called by VALUE (its stored compiler-code offset) lands
// at the wrong VM address -- exactly how `mvm test` ran external-package
// examples (referenced as testing.InternalExample.F) after the internal unit's
// init shims shifted m.code. See interp.evalCompiled / vm.Machine.TrimCode.
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

// Exact prefix ("Test", as grpc/codes declares) and digit/"_" continuations
// match; lower-case continuations ("Testify") do not. Mirrors cmd/go's isTest.
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
	if _, err := i.Eval("u", src); err != nil {
		t.Fatalf("eval: %v", err)
	}

	got := i.FuncNames("Test")
	want := []string{"Test", "TestFoo", "Test1", "Test_helper"}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("FuncNames(%q) missing %q; got %v", "Test", w, got)
		}
	}
	for _, bad := range []string{"Testify", "Testable"} {
		if slices.Contains(got, bad) {
			t.Errorf("FuncNames(%q) must not include lower-continuation %q; got %v", "Test", bad, got)
		}
	}

	if b := i.FuncNames("Benchmark"); !slices.Contains(b, "Benchmark") {
		t.Errorf("FuncNames(%q) must include the exact-prefix name %q; got %v", "Benchmark", "Benchmark", b)
	}
}

// Regression for `mvm test github.com/samber/lo` -> ExampleNewDebounceBy panic
// `index out of range [-1]`.
//
// A func TYPE's parameter names are documentation only, but parseTypeExpr's func
// case registered them as locals in the enclosing scope (it shares that code with
// real func-literal signatures). When a func-type param name collided with an
// outer param -- here `[]func(key string, count int){}` inside a method
// `reset(key string)` -- the leaked `key` rebound the method's `key` to a wrong
// frame slot (the receiver shifts param indices, so the slots no longer coincide).
// A later use of `key` then loaded garbage (a func value), corrupting the call.
// Fixed by registering func-signature params only for a genuine declaration/
// literal (parseFunc sets regFuncSig); a func TYPE leaves it unset.
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

// Regression for go-cmp TestDiff/Project2: a promoted method resolved through
// an embedded field probed the BARE "<recvName>.<method>" symbol key, so a
// same-named unit-local type hijacked an imported receiver (cmp_test.Stringer's
// String, body `string(s)`, ran with a testprotos.Stringer struct receiver ->
// reflect.Value.Convert panic). promotedMethod/MethodByName now verify the bare
// key's Type symbol is the receiver's own type before trusting the bare probe.
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

// A failed Eval must not corrupt a later Eval that reuses a generic the failed
// one had begun to instantiate (the failed unit left half-registered instance
// state on the shared, reused Compiler). Regression for the cross-eval leak.
func TestEvalRollback_GenericReuseAfterError(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("e1", `var p atomic.Pointer[int]; var bad = undefXYZ; _ = p`); err == nil {
		t.Fatal("eval1 expected to fail on undefXYZ")
	}
	if _, err := i.Eval("e2", `var q atomic.Pointer[int]; x := 9; q.Store(&x); println(*q.Load())`); err != nil {
		t.Fatalf("eval2 reusing atomic.Pointer[int] after eval1 error: %v", err)
	}
}

// A failed Eval must leave a clean slate: a later unrelated Eval still works.
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

// Rollback must not discard state from PRIOR successful Evals (REPL semantics:
// good lines accumulate, only the failed line is undone).
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

// newExtUnitInterp loads module files via a fake proxy and evaluates the
// target package (tests included), returning the interp ready for the
// external `package X_test` unit. Mirrors `mvm test <import-path>`.
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

// Regression for sirupsen/logrus TestLevelMarshalText. Once unit 1 (the
// package + its internal tests) triggers AttachSynthMethods, the materialized
// rtype carries UnmarshalText as a reflect-visible method. MethodByName's
// native-method probe must not mistake that synth-attached method for a
// stdlib bridge: the native dispatch path mutates a boxed copy of the
// receiver, so `cmp.UnmarshalText(b)` in the external unit silently lost the
// ptr-recv write-back.
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

// Regression for logrus internal/testutils: dot-importing an interpreted
// package that was published with PublishCompiledPackage. A published func's
// value is its code address (an int), so the dot-import must bind the
// pkg-qualified symbol (real kind/type), not synthesize a Value symbol of
// type int ("reflect: IsVariadic of non-func type int" at the call).
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

// Regression for the logrus SetLevel crash ("value of type *mtype.Type cannot
// be converted to uint32"). A failed external-unit compile allocated a Data
// slot for the imported const and recorded it on the SHARED symbol; the
// rollback truncated Data but could not undo the in-place Index mutation, so
// the retry's GetGlobal read whatever the retry compiled into that slot.
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

// Regression for logrus's ReportCaller tests: external test sources must be
// registered under Go's external-test pseudo package path
// ("<importPath>_test/<file>") so virtualized runtime.Caller frames qualify
// as "<importPath>_test.Func"; and the virtualized Callers stack must end
// with a goexit sentinel so the `for f, more := frames.Next(); more;` idiom
// still examines the outermost interpreted frame.
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

// Regression for recompiling a unit on the reused Compiler (the `mvm test`
// drop-retry loop). Two recompile bugs: a stale func "_end" Label symbol skipped
// the funcexit stack restore (leaking body locals so the `r.Run(...)` call
// resolved to a body local: "call of non-func"), and the per-scope label counter
// drifted (for0 -> for1) so a captured loop var resolved "undefined".
// Compiles thrice without running (a re-emitted func label would hang the VM).
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

// Regression for recompiling a unit on the reused Compiler (the `mvm test`
// drop-retry loop): a func-body skip jump (at Start-1) resolved against a stale
// _end Label from a prior committed compile, landing inside the re-emitted body.
// Top-level fall-through then ran the body inline on top of the init-shim call,
// so a package var-init (proto bench_test.go's flag.Bool) ran twice.
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

// Regression for recompiling a closure with its own parameters (proto's
// `func unknown() func(m proto.Message)` helpers). The recompile took the
// registerParamsFromSym path over the closure's empty InNames, registering no
// params, so the body's `m` resolved "undefined: m".
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

// Regression for oklog/ulid TestMonotonicSafe. In an external `package X_test`
// unit, a `var name = expr` whose name also denotes an (unexported) type in the
// package under test -- ulid's `type rng` vs the test's `var rng = rand.New(..)`
// -- must declare a variable with its type inferred from expr, not be read as an
// unnamed var of that type. Before the fix, `rng` got the interface type (nil
// value), so `ulid.Monotonic(rng, 0)` received a nil reader and the 100-goroutine
// loop nil-deref'd inside bufio.
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

// mvm test's drop-retry loop recompiles a test unit several times on one interp,
// minting a fresh *Type for each declared method-bearing type each pass. Those
// passes must converge on ONE rtype within a Machine: a value captured in one
// pass (e.g. reflect.TypeOf((*T)(nil)) stored in a native MessageInfo
// .GoReflectType) otherwise no longer == a value built in a later pass, which
// crashed proto's noenforceutf8 MessageOf "type mismatch: got *T, want *T".
//
// This mirrors the second pass: a fresh *Type for the same Go type (same name,
// layout, method set) is materialized under the same Machine and must adopt the
// first pass's reserved rtype rather than reserving a distinct one.
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
	rt2 := vm.MaterializeRtype(t2)
	if rt2 == nil {
		t.Fatal("second-pass materialize returned nil")
	}
	if rt2 != t1.Rtype {
		t.Fatalf("method-bearing type T got a distinct rtype on the second pass: %p vs %p (cross-Eval dup)", rt2, t1.Rtype)
	}
}

// Regression for grpc/internal/transport: a func-local variable's scoped symbol
// key ("New/if0/r") carries no package qualifier, so it leaked across compile
// units.
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
