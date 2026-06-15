package interp

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/stdlib/stdmod"
	"github.com/mvm-sh/mvm/vm"
)

// newExtUnitInterp loads module files via a fake proxy and evaluates the
// target package (tests included), returning the interp ready for the
// external `package X_test` unit. Mirrors `mvm test <import-path>`.
func newExtUnitInterp(t *testing.T, files map[string]string) (*Interp, *bytes.Buffer) {
	t.Helper()
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/lvl",
		version: "v1.0.0",
		files:   files,
	})
	var stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
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
	i := NewInterpreter(golang.GoSpec)
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
	i := NewInterpreter(golang.GoSpec)
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
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	files := []goparser.PackageSource{{Name: "main.go", Content: src}}
	for round := 0; round < 3; round++ {
		if err := i.CompileFiles(files); err != nil {
			t.Fatalf("compile round %d: %v", round, err)
		}
	}
}
