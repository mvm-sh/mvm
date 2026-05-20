package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/stdlib/stdmod"
)

// TestRewriteTestFlags checks the go-test -> testing.Main flag-name rewrite.
func TestRewriteTestFlags(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{nil, []string{}},
		{[]string{"-v"}, []string{"-test.v"}},
		{[]string{"-run", "TestFoo"}, []string{"-test.run", "TestFoo"}},
		{[]string{"-count=3"}, []string{"-test.count=3"}},
		{[]string{"--v"}, []string{"--test.v"}},
		{[]string{"-v", "-run", "TestFoo", "-short"}, []string{"-test.v", "-test.run", "TestFoo", "-test.short"}},
		{[]string{"-", "--"}, []string{"-", "--"}},
	}
	for _, c := range cases {
		if got := rewriteTestFlags(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("rewriteTestFlags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSplitTestArgs checks the mvm-flags / target / test-flags partition.
func TestSplitTestArgs(t *testing.T) {
	cases := []struct {
		in       []string
		mvm      []string
		target   string
		testArgs []string
	}{
		{nil, nil, ".", nil},
		{[]string{"-v"}, []string{}, ".", []string{"-v"}},
		{[]string{"./pkg", "-v", "-run", "X"}, []string{}, "./pkg", []string{"-v", "-run", "X"}},
		{[]string{"-x", "./pkg", "-v"}, []string{"-x"}, "./pkg", []string{"-v"}},
		{[]string{"-x=line", "-run", "X"}, []string{"-x=line"}, ".", []string{"-run", "X"}},
		{[]string{"github.com/google/uuid"}, []string{}, "github.com/google/uuid", []string{}},
		{[]string{"-stat", "-v"}, []string{"-stat"}, ".", []string{"-v"}},
		{[]string{"-x", "-stat", "./pkg", "-run", "X"}, []string{"-x", "-stat"}, "./pkg", []string{"-run", "X"}},
	}
	for _, c := range cases {
		mvm, target, testArgs := splitTestArgs(c.in)
		if !reflect.DeepEqual(mvm, c.mvm) || target != c.target || !reflect.DeepEqual(testArgs, c.testArgs) {
			t.Errorf("splitTestArgs(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.in, mvm, target, testArgs, c.mvm, c.target, c.testArgs)
		}
	}
}

// TestFailingTestFile checks extraction of a bridged-stdlib test file
// basename from a compile error's source position (drop-on-error retry).
func TestFailingTestFile(t *testing.T) {
	cases := []struct {
		err, target, want string
	}{
		{"strings/replace_test.go:326:32: undefined: Replacer", "strings", "replace_test.go"},
		{`loading "strings": strings/strings_test.go:25:23: cannot infer type parameter E`, "strings", "strings_test.go"},
		{"unicode/utf16/utf16_test.go:17:13: undefined: MaxRune", "unicode/utf16", "utf16_test.go"},
		// No position -> "" so the caller stops retrying.
		{"cannot infer type parameter E", "strings", ""},
		// Error in a non-test file under target must not be droppable.
		{"strings/strings.go:10:1: oops", "strings", ""},
		// Position in a different package must not match.
		{"bytes/bytes_test.go:1:1: x", "strings", ""},
	}
	for _, c := range cases {
		if got := failingTestFile(fmt.Errorf("%s", c.err), c.target); got != c.want {
			t.Errorf("failingTestFile(%q, %q) = %q, want %q", c.err, c.target, got, c.want)
		}
	}
}

// TestBuildModFS exercises the GOPROXY parsing in buildModFS. The shape
// of the resulting modfs (offline vs network-backed) is internal; this
// test only asserts construction never fails or returns nil.
func TestBuildModFS(t *testing.T) {
	cases := []string{
		"",                                    // default proxy
		"off",                                 // explicit disable -> offline
		"direct",                              // VCS-only -> offline
		"https://example.com/proxy",           // single URL
		"https://example.com/proxy,direct",    // first wins
		"off,https://example.com/proxy",       // first wins (offline)
		" https://example.com/proxy , direct", // whitespace tolerated
	}
	for _, goproxy := range cases {
		t.Setenv("GOPROXY", goproxy)
		if got := buildModFS(); got == nil {
			t.Errorf("GOPROXY=%q: buildModFS returned nil", goproxy)
		}
	}
}

// TestEmbeddedStdResolves checks that stdlib imports resolve through
// the default stdlib redirect FS, by virtue of the embedded std zip
// injected at startup. This is the path NewInterpreter installs for
// callers that don't go through wireFS (tests, embed users).
func TestEmbeddedStdResolves(t *testing.T) {
	stdlibFS := stdmod.DefaultFS()

	if _, err := fs.Stat(stdlibFS, "slices"); err != nil {
		t.Fatalf("stat slices: %v", err)
	}
	data, err := fs.ReadFile(stdlibFS, "slices/slices.go")
	if err != nil {
		t.Fatalf("read slices/slices.go: %v", err)
	}
	if len(data) == 0 {
		t.Error("slices/slices.go empty")
	}
}

func TestFilterTopLevelTests(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		args []string
		want []string
	}{
		{"no filter", []string{"TestA", "TestB"}, nil, []string{"TestA", "TestB"}},
		{"run match", []string{"TestA", "TestB"}, []string{"-test.run=TestA"}, []string{"TestA"}},
		{"run subtest segment ignored", []string{"TestA", "TestB"}, []string{"-test.run=TestA/sub"}, []string{"TestA"}},
		{"run space-separated", []string{"TestA", "TestB"}, []string{"-test.run", "TestB"}, []string{"TestB"}},
		{"run alternation", []string{"TestA", "TestB", "TestC"}, []string{"-test.run=TestA|TestC"}, []string{"TestA", "TestC"}},
		{"skip match", []string{"TestA", "TestB"}, []string{"-test.skip=TestB"}, []string{"TestA"}},
		{"run + skip", []string{"TestAlpha", "TestBeta", "TestGamma"}, []string{"-test.run=Test", "-test.skip=Beta"}, []string{"TestAlpha", "TestGamma"}},
		{"run no match", []string{"TestA"}, []string{"-test.run=NoSuch"}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Copy input -- filterTopLevelTests reuses the backing array.
			in := append([]string(nil), c.in...)
			got := filterTopLevelTests(in, c.args)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// buildMvm builds the mvm binary into a temp dir and returns its path.
// Shared by the -stat integration tests.
func buildMvm(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mvm")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// TestStatOrderingAfterTests asserts the stats block appears AFTER the test
// body's stdout. Stats fire from a t.Cleanup, so they precede the package
// PASS line but follow the test body's output; this guards that ordering.
func TestStatOrderingAfterTests(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}

	fixture := t.TempDir()
	src := `package x
import "fmt"
import "testing"
func TestSentinel(t *testing.T) { fmt.Println("SENTINEL_OUTPUT") }
`
	if err := os.WriteFile(filepath.Join(fixture, "x_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	bin := buildMvm(t)
	out, err := exec.Command(bin, "test", "-stat", fixture).CombinedOutput() //nolint:gosec // bin is a freshly-built copy of our own binary in a t.TempDir
	if err != nil {
		t.Fatalf("mvm test: %v\n%s", err, out)
	}
	s := string(out)
	sentinel := strings.Index(s, "SENTINEL_OUTPUT")
	stats := strings.Index(s, "mvm stats:")
	pass := strings.Index(s, "PASS")
	switch {
	case sentinel < 0:
		t.Fatalf("test body did not run (no SENTINEL_OUTPUT):\n%s", s)
	case stats < 0:
		t.Fatalf("no stats block:\n%s", s)
	case pass < 0:
		t.Fatalf("no PASS line:\n%s", s)
	case stats < sentinel:
		t.Errorf("stats appeared BEFORE test output; want after:\n%s", s)
	case pass < stats:
		t.Errorf("PASS appeared BEFORE stats; the counter callback should fire before testing prints PASS:\n%s", s)
	}
}

// TestStatFlushAcrossFailureModes guards the t.Cleanup-based counter design:
// stats must flush even when tests exit via t.Errorf (plain fail), t.Fatal
// (runtime.Goexit), or t.Skip (runtime.Goexit). The Goexit paths are the
// reason the wrapper registers t.Cleanup instead of a defer -- testing's
// runner always processes cleanups regardless of how the test exited, while
// native Goexit would bypass mvm's VM defer registry. Panicking tests are
// out of scope per ADR-018 (testing re-panics and crashes the process).
func TestStatFlushAcrossFailureModes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}

	bin := buildMvm(t)
	cases := []struct {
		name        string
		body        string
		wantExit    int
		wantSummary string
	}{
		{"Errorf", `t.Errorf("plain fail")`, 1, "FAIL"},
		{"Fatal", `t.Fatal("hard fail via Goexit")`, 1, "FAIL"},
		{"Skip", `t.Skip("skipped via Goexit")`, 0, "PASS"},
	}
	const tmpl = `package x
import "testing"
func TestA(t *testing.T) {}
func TestFailing(t *testing.T) { %s }
func TestZ(t *testing.T) {}
`
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fixture := t.TempDir()
			src := fmt.Sprintf(tmpl, c.body)
			if err := os.WriteFile(filepath.Join(fixture, "x_test.go"), []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "test", "-stat", fixture) //nolint:gosec // bin is buildMvm's t.TempDir output
			out, err := cmd.CombinedOutput()
			s := string(out)
			gotExit := 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					gotExit = ee.ExitCode()
				} else {
					t.Fatalf("unexpected error: %v\n%s", err, s)
				}
			}
			if gotExit != c.wantExit {
				t.Errorf("exit = %d, want %d:\n%s", gotExit, c.wantExit, s)
			}
			if !strings.Contains(s, "mvm stats:") {
				t.Errorf("stats block missing -- counter did not reach zero, suggesting a cleanup path was skipped:\n%s", s)
			}
			if !strings.Contains(s, c.wantSummary) {
				t.Errorf("expected summary %q in output:\n%s", c.wantSummary, s)
			}
		})
	}
}

// TestExampleRun guards that `mvm test` discovers and runs Example* functions
// (with an // Output: directive) and that their stdout is captured correctly.
// The example mixes fmt.Print (which mvm routes through the interpreter writer)
// with a direct os.Stdout write (the bridged global): both must land in the
// captured stream in program order, which only holds when the interpreter's
// stdout follows testing's per-example os.Stdout redirection. A package with no
// Test* funcs (examples only) must still run rather than report "no tests".
func TestExampleRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}

	cases := []struct {
		name     string
		output   string // text after `// Output:`
		wantExit int
		wantSub  string
	}{
		{"pass", "abc", 0, "PASS"},
		{"fail", "xyz", 1, "FAIL"},
	}
	const tmpl = `package x

import (
	"fmt"
	"os"
)

func ExampleMix() {
	fmt.Print("a")
	os.Stdout.Write([]byte("b"))
	fmt.Println("c")
	// Output: %s
}
`
	bin := buildMvm(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fixture := t.TempDir()
			src := fmt.Sprintf(tmpl, c.output)
			if err := os.WriteFile(filepath.Join(fixture, "x_test.go"), []byte(src), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(bin, "test", fixture).CombinedOutput() //nolint:gosec // bin is buildMvm's t.TempDir output
			s := string(out)
			gotExit := 0
			if err != nil {
				ee, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatalf("unexpected error: %v\n%s", err, s)
				}
				gotExit = ee.ExitCode()
			}
			if gotExit != c.wantExit {
				t.Errorf("exit = %d, want %d:\n%s", gotExit, c.wantExit, s)
			}
			if !strings.Contains(s, c.wantSub) {
				t.Errorf("expected %q in output:\n%s", c.wantSub, s)
			}
			if strings.Contains(s, "no tests to run") {
				t.Errorf("examples-only package reported no tests to run:\n%s", s)
			}
		})
	}
}
