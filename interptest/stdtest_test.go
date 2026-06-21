package interptest

import (
	"bytes"
	"os"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// TestBridgedStdlibExternalTestLoad exercises the loader branch added for
// `mvm test <bridged-stdlib-pkg>`: when the target has a reflect bridge
// (stdlib.Values["strings"]) and no source on the normal chain, external
// `package X_test` files from the test-source FS must load and become
// callable through FuncNames("Test").
//
// Uses a synthetic strings package on an fstest.MapFS (not the real
// $GOROOT) so the test is hermetic across Go versions and host installs.
// Two test files exercise both branches:
//   - external_test.go (package strings_test) -> kept, contributes TestExternal
//   - internal_test.go (package strings)      -> dropped, would access
//     unexported bridge state that does not exist
func TestBridgedStdlibExternalTestLoad(t *testing.T) {
	mapFS := fstest.MapFS{
		"strings/external_test.go": &fstest.MapFile{Data: []byte(`package strings_test

import (
	"strings"
	"testing"
)

func TestExternal(t *testing.T) {
	if strings.Index("abc", "b") != 1 {
		t.Fatal("strings.Index broken")
	}
}
`)},
		"strings/internal_test.go": &fstest.MapFile{Data: []byte(`package strings

import "testing"

// Internal test files for bridged stdlib packages must be filtered out --
// they reference unexported names the bridge has no entries for.
func TestInternal(t *testing.T) {
	t.Fatal("must not be loaded")
}
`)},
	}

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetTestSourceFS(mapFS)
	i.SetIncludeTests(true)
	if _, err := i.Eval("strings", ""); err != nil {
		t.Fatalf("loading strings tests: %v\nstderr: %s", err, stderr.String())
	}
	names := i.FuncNames("Test")
	if !slices.Contains(names, "TestExternal") {
		t.Errorf("FuncNames missing TestExternal; got %v", names)
	}
	if slices.Contains(names, "TestInternal") {
		t.Errorf("FuncNames must not include the internal-package test, got %v", names)
	}
}

// TestBridgedStdlibInternalStubsResolve verifies the stub bridges for
// std-internal packages (internal/testenv, internal/asan, internal/race,
// ...) let an external test file that imports and uses them load instead
// of being skipped by loadBridgedTestSources' unresolvable-import filter.
func TestBridgedStdlibInternalStubsResolve(t *testing.T) {
	mapFS := fstest.MapFS{
		"strings/internaldeps_test.go": &fstest.MapFile{Data: []byte(`package strings_test

import (
	"internal/asan"
	"internal/race"
	"internal/testenv"
	"testing"
)

func TestUsesInternalStubs(t *testing.T) {
	if asan.Enabled || race.Enabled {
		t.Skip("sanitizer on")
	}
	if b := testenv.Builder(); b != "" {
		testenv.SkipFlaky(t, 12345)
	}
	testenv.SkipIfOptimizationOff(t)
}
`)},
	}

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetTestSourceFS(mapFS)
	i.SetIncludeTests(true)
	if _, err := i.Eval("strings", ""); err != nil {
		t.Fatalf("loading strings tests: %v\nstderr: %s", err, stderr.String())
	}
	if !slices.Contains(i.FuncNames("Test"), "TestUsesInternalStubs") {
		t.Errorf("test file using internal/* stubs was not loaded; stderr: %s", stderr.String())
	}
}

// TestBridgedStdlibInternalReflectliteResolves verifies the internal/reflectlite
// bridge (stdlib/internal_stubs.go) lets a test file importing it load. The
// errors package source (mvm-sh/std mirror) imports internal/reflectlite from
// wrap.go; before the bridge, `mvm test errors` failed with
// "stat internal/reflectlite: no such file or directory". reflectlite's used
// surface is a strict subset of reflect, so the bridge re-exports reflect.
func TestBridgedStdlibInternalReflectliteResolves(t *testing.T) {
	mapFS := fstest.MapFS{
		"strings/reflectlite_test.go": &fstest.MapFile{Data: []byte(`package strings_test

import (
	"internal/reflectlite"
	"testing"
)

func TestUsesReflectlite(t *testing.T) {
	rt := reflectlite.TypeOf("x")
	if rt.Kind() == reflectlite.Ptr || rt.Kind() == reflectlite.Interface {
		t.Fatal("string is neither pointer nor interface")
	}
	if !reflectlite.ValueOf("x").IsValid() {
		t.Fatal("ValueOf produced invalid value")
	}
	if reflectlite.TypeOf((*error)(nil)).Elem() == nil {
		t.Fatal("nil error element type")
	}
}
`)},
	}

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetTestSourceFS(mapFS)
	i.SetIncludeTests(true)
	if _, err := i.Eval("strings", ""); err != nil {
		t.Fatalf("loading strings tests: %v\nstderr: %s", err, stderr.String())
	}
	if !slices.Contains(i.FuncNames("Test"), "TestUsesReflectlite") {
		t.Errorf("test file importing internal/reflectlite was not loaded; stderr: %s", stderr.String())
	}
}

// TestMirrorSourcedPkgExternalTestsRun covers the loader path for
// `mvm test errors` / `mvm test cmp`: a package with source on the stdlib FS
// (the mvm-sh/std mirror) whose tests are external (package X_test).
// The mirror's src.zip strips _test.go, so LoadPackageSources, finding the source
// but no test files, serves the external X_test files from testSrcFS as a
// standalone unit (their `import "X"` resolves X via the normal chain).
func TestMirrorSourcedPkgExternalTestsRun(t *testing.T) {
	mirrorFS := fstest.MapFS{
		"mymir/mymir.go": &fstest.MapFile{Data: []byte(`package mymir

func Hello() string { return "hi" }
`)},
	}
	testFS := fstest.MapFS{
		"mymir/mymir_test.go": &fstest.MapFile{Data: []byte(`package mymir_test

import (
	"mymir"
	"testing"
)

func TestHello(t *testing.T) {
	if mymir.Hello() != "hi" {
		t.Fatal("Hello broken")
	}
}
`)},
	}

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values) // so the external test's `import "testing"` resolves
	i.SetStdlibFS(mirrorFS)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetTestSourceFS(testFS)
	i.SetIncludeTests(true)
	if _, err := i.Eval("mymir", ""); err != nil {
		t.Fatalf("loading mymir tests: %v\nstderr: %s", err, stderr.String())
	}
	if !slices.Contains(i.FuncNames("Test"), "TestHello") {
		t.Errorf("external test for mirror-sourced package was not loaded; got %v", i.FuncNames("Test"))
	}
}

// External (package X_test) tests shipped in-tree by the mirror (no testSrcFS)
// must still load as a standalone unit, not get dropped as "not package X".
func TestMirrorSourcedPkgInTreeExternalTestsRun(t *testing.T) {
	mirrorFS := fstest.MapFS{
		"mymir/mymir.go": &fstest.MapFile{Data: []byte(`package mymir

func Hello() string { return "hi" }
`)},
		"mymir/mymir_test.go": &fstest.MapFile{Data: []byte(`package mymir_test

import (
	"mymir"
	"testing"
)

func TestHello(t *testing.T) {
	if mymir.Hello() != "hi" {
		t.Fatal("Hello broken")
	}
}
`)},
	}

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values) // so the external test's `import "testing"` resolves
	i.SetStdlibFS(mirrorFS)
	i.SetIO(os.Stdin, &stdout, &stderr)
	// Note: no SetTestSourceFS -- the tests come from the mirror FS itself.
	i.SetIncludeTests(true)
	if _, err := i.Eval("mymir", ""); err != nil {
		t.Fatalf("loading mymir tests: %v\nstderr: %s", err, stderr.String())
	}
	// An external-only test package stashes its tests for a second unit; load it
	// the way `mvm test` does (see test_cmd.go loadExternalTests).
	ext := i.ExternalTestSources()
	if len(ext) == 0 {
		t.Fatalf("external test sources not stashed for external-only package")
	}
	i.PublishCompiledPackage("mymir")
	if _, err := i.EvalFiles(ext); err != nil {
		t.Fatalf("loading external test unit: %v\nstderr: %s", err, stderr.String())
	}
	if !slices.Contains(i.FuncNames("Test"), "TestHello") {
		t.Errorf("in-tree external test for mirror-sourced package was not loaded; got %v", i.FuncNames("Test"))
	}
}

// A package shipping both internal (package X) and external (package X_test)
// tests in-tree keeps the internal suite in-unit and drops the external one.
func TestMirrorSourcedPkgPrefersInternalTests(t *testing.T) {
	mirrorFS := fstest.MapFS{
		"mymir/mymir.go": &fstest.MapFile{Data: []byte(`package mymir

func Hello() string { return "hi" }
`)},
		"mymir/internal_test.go": &fstest.MapFile{Data: []byte(`package mymir

import "testing"

func TestInternal(t *testing.T) {
	if Hello() != "hi" {
		t.Fatal("Hello broken")
	}
}
`)},
		"mymir/external_test.go": &fstest.MapFile{Data: []byte(`package mymir_test

import (
	"mymir"
	"testing"
)

func TestExternal(t *testing.T) {
	if mymir.Hello() != "hi" {
		t.Fatal("Hello broken")
	}
}
`)},
	}

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetStdlibFS(mirrorFS)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetIncludeTests(true)
	if _, err := i.Eval("mymir", ""); err != nil {
		t.Fatalf("loading mymir tests: %v\nstderr: %s", err, stderr.String())
	}
	names := i.FuncNames("Test")
	if !slices.Contains(names, "TestInternal") {
		t.Errorf("internal test should be kept in-unit; got %v", names)
	}
	if slices.Contains(names, "TestExternal") {
		t.Errorf("external test should be dropped when an internal suite is present; got %v", names)
	}
}

// TestBridgedStdlibSkipFiles verifies SetTestSkipFiles excludes named test
// files from a bridged-stdlib load. This backs `mvm test`'s drop-on-compile-
// error retry: a file that can't compile against the bridge (e.g. one using
// export_test.go-only symbols) is recorded and skipped on the next attempt.
func TestBridgedStdlibSkipFiles(t *testing.T) {
	mapFS := fstest.MapFS{
		"strings/good_test.go": &fstest.MapFile{Data: []byte(`package strings_test

import "testing"

func TestGood(t *testing.T) {}
`)},
		"strings/bad_test.go": &fstest.MapFile{Data: []byte(`package strings_test

import "testing"

func TestBad(t *testing.T) {}
`)},
	}

	var stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, os.Stdout, &stderr)
	i.SetTestSourceFS(mapFS)
	i.SetIncludeTests(true)
	i.SetTestSkipFiles(map[string]bool{"bad_test.go": true})
	if _, err := i.Eval("strings", ""); err != nil {
		t.Fatalf("loading strings tests: %v\nstderr: %s", err, stderr.String())
	}
	names := i.FuncNames("Test")
	if !slices.Contains(names, "TestGood") {
		t.Errorf("TestGood should load; got %v", names)
	}
	if slices.Contains(names, "TestBad") {
		t.Errorf("TestBad is in the skip set and must not load; got %v", names)
	}
}

// TestBridgedStdlibTestOverlayResolves verifies stdlib.TestOverlay() supplies a
// bridged package's export_test.go-only symbols (math's ExpGo / Exp2Go /
// HypotGo / SqrtGo / TrigReduce / ReduceThreshold) so an external test file
// dot-importing the package and using them loads instead of failing
// "undefined: ExpGo". Merges Values + TestOverlay the way newTestInterp does.
func TestBridgedStdlibTestOverlayResolves(t *testing.T) {
	mapFS := fstest.MapFS{
		"math/overlay_test.go": &fstest.MapFile{Data: []byte(`package math_test

import (
	. "math"
	"testing"
)

func TestOverlaySymbols(t *testing.T) {
	_, _ = ExpGo, Exp2Go
	_, _ = HypotGo, SqrtGo
	_ = ReduceThreshold
	if j, _ := TrigReduce(Pi); j > 8 {
		t.Fatal("TrigReduce out of range")
	}
}
`)},
	}

	vals := make(map[string]map[string]reflect.Value, len(stdlib.Values))
	for k, v := range stdlib.Values {
		vals[k] = v
	}
	for pkg, merged := range stdlib.TestOverlay() {
		vals[pkg] = merged
	}

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(vals)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetTestSourceFS(mapFS)
	i.SetIncludeTests(true)
	if _, err := i.Eval("math", ""); err != nil {
		t.Fatalf("loading math tests with overlay: %v\nstderr: %s", err, stderr.String())
	}
	if !slices.Contains(i.FuncNames("Test"), "TestOverlaySymbols") {
		t.Errorf("overlay test not loaded; got %v", i.FuncNames("Test"))
	}
}

// TestBridgedStdlibTestSourceFSNotConsultedForImports guards the design
// promise that the test-source FS is invisible to ordinary `import "X"`
// resolution. If it were chained alongside stdlibfs/remotefs, an import
// of a bridged package would start loading interpreted source on top of
// the reflect bridge and double-define every exported symbol.
//
// The fixture wires a test-source FS that would, if consulted for plain
// imports, supply a strings/extra.go contributing a new exported symbol.
// We then eval a tiny program that just uses the native bridge symbol;
// if extra.go were loaded the program would still compile (extra symbol
// gets ignored), but more importantly the load path must not even try to
// resolve the import through testSrcFS.
func TestBridgedStdlibTestSourceFSNotConsultedForImports(t *testing.T) {
	mapFS := fstest.MapFS{
		"strings/extra.go": &fstest.MapFile{Data: []byte(`package strings

// If this file were ever read during plain import resolution, the
// duplicate symbol vs. the bridge would crash the loader.
func MvmShouldNotSeeMe() {}
`)},
	}
	var stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, os.Stdout, &stderr)
	i.SetTestSourceFS(mapFS)
	// includeTests intentionally OFF -- plain import path.
	src := `package main; import "strings"; func main() { _ = strings.Index("ab", "b") }`
	if _, err := i.Eval("main.go", src); err != nil {
		t.Fatalf("plain import path must not touch testSrcFS: %v\nstderr: %s", err, stderr.String())
	}
}
