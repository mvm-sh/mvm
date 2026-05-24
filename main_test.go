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

// writeFixture writes src as x_test.go in a fresh temp dir and returns the dir.
func writeFixture(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runMvmTest runs `bin test <args...>` and returns the exit code and combined
// output. A failure that is not a normal non-zero exit fails the test.
func runMvmTest(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	out, err := exec.Command(bin, append([]string{"test"}, args...)...).CombinedOutput() //nolint:gosec // bin is buildMvm's t.TempDir output
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("unexpected error: %v\n%s", err, out)
		}
		return ee.ExitCode(), string(out)
	}
	return 0, string(out)
}

// TestStatOrderingAfterTests asserts the stats block appears AFTER the package
// PASS/FAIL line (and therefore after the test body's stdout). Stats flush once
// testing.MainStart(...).Run() returns, so they follow everything testing
// prints; this guards that ordering.
func TestStatOrderingAfterTests(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}

	fixture := writeFixture(t, `package x
import "fmt"
import "testing"
func TestSentinel(t *testing.T) { fmt.Println("SENTINEL_OUTPUT") }
`)
	code, s := runMvmTest(t, buildMvm(t), "-stat", fixture)
	if code != 0 {
		t.Fatalf("mvm test exited %d:\n%s", code, s)
	}
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
	case stats < pass:
		t.Errorf("stats appeared BEFORE PASS; want stats after the package PASS/FAIL line:\n%s", s)
	}
}

// TestStatFlushAcrossFailureModes guards that stats flush regardless of how a
// test exits: t.Errorf (plain fail), t.Fatal (runtime.Goexit), or t.Skip
// (runtime.Goexit). All three flow through testing.MainStart(...).Run(), which
// returns normally so the post-run flush always fires. Panicking tests are out
// of scope per ADR-018 (testing re-panics and crashes the process before Run
// returns).
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
			fixture := writeFixture(t, fmt.Sprintf(tmpl, c.body))
			gotExit, s := runMvmTest(t, bin, "-stat", fixture)
			if gotExit != c.wantExit {
				t.Errorf("exit = %d, want %d:\n%s", gotExit, c.wantExit, s)
			}
			if !strings.Contains(s, "mvm stats:") {
				t.Errorf("stats block missing -- the post-run flush did not fire for this exit path:\n%s", s)
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
			fixture := writeFixture(t, fmt.Sprintf(tmpl, c.output))
			gotExit, s := runMvmTest(t, bin, fixture)
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

// TestRunMultiFile guards `mvm run f1.go f2.go ...`: the named files are compiled
// as one main package, so a top-level symbol declared in one file resolves from
// another regardless of the order the files are listed (issue #19).
func TestRunMultiFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}
	bin := buildMvm(t)
	dir := t.TempDir()
	aGo := filepath.Join(dir, "a.go")
	bGo := filepath.Join(dir, "b.go")
	if err := os.WriteFile(aGo, []byte("package main\nimport \"fmt\"\nfunc main() { fmt.Println(Global) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bGo, []byte("package main\nvar Global = \"abc\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Both orderings must resolve the cross-file reference and print "abc".
	for _, order := range [][]string{{aGo, bGo}, {bGo, aGo}} {
		out, err := exec.Command(bin, append([]string{"run"}, order...)...).CombinedOutput() //nolint:gosec // bin from t.TempDir
		if err != nil {
			t.Fatalf("run %v: %v\n%s", order, err, out)
		}
		if strings.TrimSpace(string(out)) != "abc" {
			t.Errorf("run %v: got %q, want \"abc\"", order, strings.TrimSpace(string(out)))
		}
	}
}

// TestBenchmarkRun guards that `mvm test -bench` discovers and runs Benchmark*
// functions through the MainStart driver. Benchmarks run only when -bench is
// given; a benchmarks-only package must still drive (not short-circuit on the
// "no tests" guard), and a failing benchmark must surface as FAIL/exit 1.
func TestBenchmarkRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}

	bin := buildMvm(t)
	const benchSrc = `package x
import "testing"
func BenchmarkAdd(b *testing.B) {
	x := 0
	for i := 0; i < b.N; i++ {
		x += i
	}
	_ = x
}
`
	cases := []struct {
		name     string
		src      string
		args     []string
		wantExit int
		wantSub  string
		notSub   string
	}{
		{"with -bench runs", benchSrc, []string{"-bench", ".", "-benchtime", "1x"}, 0, "BenchmarkAdd", ""},
		{"without -bench skips", benchSrc, nil, 0, "PASS", "BenchmarkAdd"},
		{"failing bench fails", `package x
import "testing"
func BenchmarkBoom(b *testing.B) { b.Fatal("boom") }
`, []string{"-bench", ".", "-benchtime", "1x"}, 1, "FAIL", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fixture := writeFixture(t, c.src)
			gotExit, s := runMvmTest(t, bin, append([]string{fixture}, c.args...)...)
			if gotExit != c.wantExit {
				t.Errorf("exit = %d, want %d:\n%s", gotExit, c.wantExit, s)
			}
			if c.wantSub != "" && !strings.Contains(s, c.wantSub) {
				t.Errorf("expected %q in output:\n%s", c.wantSub, s)
			}
			if c.notSub != "" && strings.Contains(s, c.notSub) {
				t.Errorf("did not expect %q in output:\n%s", c.notSub, s)
			}
		})
	}
}
