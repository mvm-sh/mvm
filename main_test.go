package main

import (
	"errors"
	"flag"
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
		{[]string{"github.com/google/uuid"}, []string{}, "github.com/google/uuid", nil},
		{[]string{"-stat", "-v"}, []string{"-stat"}, ".", []string{"-v"}},
		{[]string{"-x", "-stat", "./pkg", "-run", "X"}, []string{"-x", "-stat"}, "./pkg", []string{"-run", "X"}},
		// Target after a separate-form value flag (the `go test -run X ./pkg` form).
		{[]string{"-run", "X", "github.com/google/uuid"}, []string{}, "github.com/google/uuid", []string{"-run", "X"}},
		{[]string{"-v", "github.com/google/uuid"}, []string{}, "github.com/google/uuid", []string{"-v"}},
		{[]string{"-count", "3", "./pkg"}, []string{}, "./pkg", []string{"-count", "3"}},
		{[]string{"-run=X", "./pkg"}, []string{}, "./pkg", []string{"-run=X"}},
		{[]string{"-run", "X", "-v", "./pkg"}, []string{}, "./pkg", []string{"-run", "X", "-v"}},
	}
	for _, c := range cases {
		mvm, target, testArgs := splitTestArgs(c.in)
		if !reflect.DeepEqual(mvm, c.mvm) || target != c.target || !reflect.DeepEqual(testArgs, c.testArgs) {
			t.Errorf("splitTestArgs(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.in, mvm, target, testArgs, c.mvm, c.target, c.testArgs)
		}
	}
}

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

// TestNewTestInterpResetsFlags guards that each fresh test interp starts with a
// clean native flag.CommandLine. The import-path retry loop builds a new interp
// per attempt; without the reset, a package-level flag registered on one attempt
// would "flag redefined"-panic when a sibling file's panicking init forces a
// reload, wrongly dropping the file that owns the flag (e.g. modernc.org/sqlite
// all_test.go's -inner after module_volatile_test.go's init panic).
func TestNewTestInterpResetsFlags(t *testing.T) {
	saved := flag.CommandLine
	defer func() { flag.CommandLine = saved }()

	flag.CommandLine = flag.NewFlagSet("prev", flag.ContinueOnError)
	flag.Bool("inner", false, "leftover from a prior load attempt")

	newTestInterp(traceFlag{}) // must wipe flag.CommandLine

	if flag.Lookup("inner") != nil {
		t.Fatal("newTestInterp left a stale flag registered; retry would flag-redefine-panic")
	}
	// Re-registering the same name must now be safe (the next attempt's var init).
	flag.Bool("inner", false, "re-registered on the retry")
}

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

func buildMvm(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mvm")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func writeFixture(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runMvmTest(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	out, err := exec.Command(bin, append([]string{"test"}, args...)...).CombinedOutput() //nolint:gosec // bin is buildMvm's t.TempDir output
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("unexpected error: %v\n%s", err, out)
		}
		return ee.ExitCode(), string(out)
	}
	return 0, string(out)
}

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
		out, err := exec.Command(bin, append([]string{"run"}, order...)...).CombinedOutput() // bin from t.TempDir
		if err != nil {
			t.Fatalf("run %v: %v\n%s", order, err, out)
		}
		if strings.TrimSpace(string(out)) != "abc" {
			t.Errorf("run %v: got %q, want \"abc\"", order, strings.TrimSpace(string(out)))
		}
	}
}

// TestExternalTestPkgPath guards that types in a package X_test render
// X_test.<Name> (not the under-test package's name) under `mvm test <dir>`,
// in either file order.
func TestExternalTestPkgPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}
	bin := buildMvm(t)
	const fooGo = "package foo\n\ntype Internal struct{ a int }\n"
	const testGo = `package foo_test

import (
	"fmt"
	"testing"
)

type T struct{ a int }

func TestPkgPath(t *testing.T) {
	if got := fmt.Sprintf("%#v", T{5}); got != "foo_test.T{a:5}" {
		t.Errorf("got %q, want foo_test.T{a:5}", got)
	}
	var p *T
	if got := fmt.Sprintf("%T", p); got != "*foo_test.T" {
		t.Errorf("got %q, want *foo_test.T", got)
	}
}
`
	// Names make the test file sort second, then first (ReadDir sorts by name).
	for _, names := range [][2]string{{"foo.go", "z_test.go"}, {"z.go", "a_test.go"}} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, names[0]), []byte(fooGo), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, names[1]), []byte(testGo), 0o600); err != nil {
			t.Fatal(err)
		}
		exit, out := runMvmTest(t, bin, dir)
		if exit != 0 {
			t.Errorf("files %v: exit = %d, want 0:\n%s", names, exit, out)
		}
	}
}

func TestSubtestPanicDiag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}
	bin := buildMvm(t)
	const src = `package x

import "testing"

func deref(p *int) int { return *p }

func TestBoom(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		var p *int
		t.Log(deref(p))
	})
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	exit, out := runMvmTest(t, bin, dir)
	if exit == 0 {
		t.Fatalf("expected non-zero exit, got 0:\n%s", out)
	}
	if !strings.Contains(out, "x_test.go:") {
		t.Errorf("subtest panic diagnostic missing source location:\n%s", out)
	}
}

func TestRunEvalEcho(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}
	bin := buildMvm(t)
	cases := []struct {
		name, expr, want string
	}{
		{"arith", "1+2", "3\n"},
		{"builtin len", `len("abc")`, "3\n"},
		{"multi-return call suppressed", `fmt.Println("hi")`, "hi\n"}, // no trailing <nil>
		{"void call suppressed", "f := func(){}; f()", ""},
		{"define suppressed", "x:=5", ""},
		{"assign suppressed", "x:=5; x=x+3", ""},
		{"multi-define suppressed", "a,b:=1,2", ""},
		{"declaration suppressed", "var x int", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := exec.Command(bin, "run", "-e", c.expr).CombinedOutput() //nolint:gosec // bin from t.TempDir
			if err != nil {
				t.Fatalf("run -e %q: %v\n%s", c.expr, err, out)
			}
			if string(out) != c.want {
				t.Errorf("run -e %q = %q, want %q", c.expr, out, c.want)
			}
		})
	}
}

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

// TestMain in the package under test must drive the suite (fixture setup runs,
// its os.Exit(m.Run()) code becomes the run's exit code), mirroring go test's
// generated _testmain. Regression: doublestar's fixtures were never created.
func TestTestMainDriver(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short")
	}

	const exitSrc = `package x
import (
	"os"
	"testing"
)
func TestFixture(t *testing.T) {
	if _, err := os.Stat("made-by-testmain"); err != nil {
		t.Fatal("TestMain setup missing")
	}
	BODY
}
func TestMain(m *testing.M) {
	f, err := os.Create("made-by-testmain")
	if err != nil {
		os.Exit(2)
	}
	f.Close()
	os.Exit(m.Run())
}
`
	const returnSrc = `package x
import (
	"os"
	"testing"
)
func TestFixture(t *testing.T) {
	if _, err := os.Stat("made-by-testmain"); err != nil {
		t.Fatal("TestMain setup missing")
	}
}
func TestMain(m *testing.M) {
	f, _ := os.Create("made-by-testmain")
	f.Close()
	m.Run() // exit code read back from m, as in go test's _testmain
}
`
	bin := buildMvm(t)
	cases := []struct {
		name     string
		src      string
		wantExit int
		wantSub  string
	}{
		{"setup and exit code", strings.ReplaceAll(exitSrc, "BODY", ""), 0, "PASS"},
		{"failing suite exits 1", strings.ReplaceAll(exitSrc, "BODY", `t.Error("boom")`), 1, "FAIL"},
		{"return without os.Exit", returnSrc, 0, "PASS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fixture := writeFixture(t, c.src)
			gotExit, s := runMvmTest(t, bin, fixture)
			if gotExit != c.wantExit {
				t.Errorf("exit = %d, want %d:\n%s", gotExit, c.wantExit, s)
			}
			if !strings.Contains(s, c.wantSub) {
				t.Errorf("expected %q in output:\n%s", c.wantSub, s)
			}
			if strings.Contains(s, "TestMain") && strings.Contains(s, "--- ") {
				t.Errorf("TestMain ran as an ordinary test:\n%s", s)
			}
		})
	}
}
