package interp

import (
	"bytes"
	"os"
	"slices"
	"testing"
	"testing/fstest"

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
	i := NewInterpreter(golang.GoSpec)
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
	i := NewInterpreter(golang.GoSpec)
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
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, os.Stdout, &stderr)
	i.SetTestSourceFS(mapFS)
	// includeTests intentionally OFF -- plain import path.
	src := `package main; import "strings"; func main() { _ = strings.Index("ab", "b") }`
	if _, err := i.Eval("main.go", src); err != nil {
		t.Fatalf("plain import path must not touch testSrcFS: %v\nstderr: %s", err, stderr.String())
	}
}
