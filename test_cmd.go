package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
)

const testUsageText = `Usage: mvm test [-x] [-stat] [target] [test flags]
Runs Go tests found in *_test.go files of the given target.
Target may be a local directory (default ".") or an import path
(e.g. "github.com/google/uuid") fetched dynamically via the Go module proxy.
Test flags use the same names as "go test": -v for verbose output,
-run REGEX to select tests, -bench REGEX to run benchmarks, -count N,
-short, etc.
`

func isMvmTestFlag(a string) bool {
	switch a {
	case "-x", "--x", "-stat", "--stat", "-h", "-help", "--help":
		return true
	}
	return strings.HasPrefix(a, "-x=") || strings.HasPrefix(a, "--x=") ||
		strings.HasPrefix(a, "-stat=") || strings.HasPrefix(a, "--stat=")
}

func splitTestArgs(arg []string) (mvmFlags []string, target string, testFlags []string) {
	target = "."
	n := 0
	for n < len(arg) && isMvmTestFlag(arg[n]) {
		n++
	}
	mvmFlags = arg[:n]
	rest := arg[n:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		target = rest[0]
		rest = rest[1:]
	}
	return mvmFlags, target, rest
}

// hasShortFlag reports whether -short was set in any form (incl. -short=false).
func hasShortFlag(args []string) bool {
	for _, a := range args {
		s := strings.TrimLeft(a, "-")
		if s == "short" || s == "test.short" ||
			strings.HasPrefix(s, "short=") || strings.HasPrefix(s, "test.short=") {
			return true
		}
	}
	return false
}

func rewriteTestFlags(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		switch {
		case a == "-" || a == "--":
			out[i] = a
		case strings.HasPrefix(a, "--"):
			out[i] = "--test." + a[2:]
		case strings.HasPrefix(a, "-"):
			out[i] = "-test." + a[1:]
		default:
			out[i] = a
		}
	}
	return out
}

func testCmd(arg []string) error {
	var (
		trace traceFlag
		stat  bool
	)
	tflag := flag.NewFlagSet("test", flag.ContinueOnError)
	tflag.Usage = func() {
		_, _ = fmt.Fprint(os.Stdout, testUsageText)
		tflag.PrintDefaults()
	}
	tflag.Var(&trace, "x", "trace mode (bare -x = line; -x=op, -x=all, -x=line,op)")
	tflag.BoolVar(&stat, "stat", false, "print compile/run statistics on exit")

	mvmFlags, target, testFlags := splitTestArgs(arg)
	if err := tflag.Parse(mvmFlags); err != nil {
		if errors.Is(err, flag.ErrHelp) { // -h already printed usage
			return nil
		}
		return err
	}

	// Force -short for stress-heavy packages unless the caller chose otherwise.
	if stdlib.ForceShort(target) && !hasShortFlag(testFlags) {
		fmt.Fprintf(os.Stderr, "mvm test: %s: forcing -short (stress tests impractical under the interpreter)\n", target)
		testFlags = append([]string{"-short"}, testFlags...)
	}

	os.Args = append([]string{"mvm-test"}, rewriteTestFlags(testFlags)...)

	// Try target as a local directory first; fall back to import-path
	// resolution (modfs / stdlibfs / pkgfs) on miss.
	if absDir, aerr := filepath.Abs(target); aerr == nil {
		if entries, rerr := os.ReadDir(absDir); rerr == nil {
			i, mfs := newTestInterp(trace)
			flushStats := setupStats(i, mfs, stat)
			if err := evalLocalDir(i, absDir, entries); err != nil {
				flushStats()
				return err
			}
			return runTestsInDir(i, absDir, "", flushStats)
		}
	}

	// Import-path target (bridged stdlib, mirror-interpreted stdlib, or remote
	// module). Retry on a test-file compile error: drop the offending _test.go
	// and reload so the rest of the package still runs -- mirroring how a real
	// module's broken test file shouldn't sink the whole suite. This matters for
	// bridged packages whose external tests reference export_test.go-only symbols
	// (absent on a native bridge) AND for mirror-interpreted packages whose tests
	// hit an interpretation gap (e.g. generics). Each retry needs a fresh
	// interpreter because Eval mutates compiler/VM state. A non-test-file error
	// (failingTestFile == "") still fails hard.
	skip := map[string]bool{}
	for {
		i, mfs := newTestInterp(trace)
		flushStats := setupStats(i, mfs, stat)
		i.SetIncludeTests(true)
		i.SetTestSkipFiles(skip)
		if _, err := i.Eval(target, ""); err != nil {
			if f := failingTestFile(err, target); f != "" && !skip[f] {
				skip[f] = true
				fmt.Fprintf(os.Stderr, "mvm test: skipping %s/%s (%v)\n", target, f, err)
				continue
			}
			flushStats()
			// Error not in a droppable _test.go file. If the source still loads
			// without tests, only the tests can't run: report "no tests" instead
			// of failing hard.
			if src, ok := sourceLoadsWithoutTests(trace, target); ok {
				fmt.Fprintf(os.Stderr, "mvm test: %s: tests not loaded (%v)\n", target, err)
				return runTestDriver(src, target, func() {})
			}
			// Generic-only stub: nothing to load by design. Report it clearly
			// and exit 0 so the compat matrix marks it gray, not red.
			if stdlib.IsGenericOnly(target) {
				fmt.Fprintf(os.Stderr, "mvm test: %s: unsupported (generic-only stdlib package; all exports are generic, so there is no reflect bridge or interpreted source)\n", target)
				return nil
			}
			return fmt.Errorf("loading %q: %w", target, err)
		}
		// modfs serves the package from memory, so tests using testdata-relative
		// paths see whatever cwd mvm was launched from. Spill the subtree to a
		// temp dir and chdir there to mirror `go test`'s setup.
		if mfs != nil {
			dir, cleanup, err := materializePkgDir(mfs, target)
			if err != nil {
				flushStats()
				return err
			}
			if dir != "" {
				defer cleanup()
				return runTestsInDir(i, dir, target, flushStats)
			}
		}
		// Bridged-stdlib case: external test files came from $GOROOT/src/<target>.
		// chdir there so testdata-relative paths resolve. No copy needed since
		// stdlib tests read but do not write their testdata subtrees.
		if dir := stdlib.GorootSrcDir(target); dir != "" {
			return runTestsInDir(i, dir, target, flushStats)
		}
		return runTestDriver(i, target, flushStats)
	}
}

// newTestInterp builds a fresh interpreter configured for `mvm test`:
// stdlib bridges imported, FS chain wired, and the $GOROOT test-source FS
// installed for bridged-stdlib external tests. Returns the modfs so callers
// can materialize testdata subtrees.
func newTestInterp(trace traceFlag) (*interp.Interp, *modfs.FS) {
	i := interp.NewInterpreter(golang.GoSpec)
	// Install bridges, then layer in test-only export_test stand-ins (e.g.
	// strings.StringFind) so external stdlib tests that use them resolve. The
	// overlay is test-runner-only, so `mvm run` never sees these symbols.
	vals := make(map[string]map[string]reflect.Value, len(stdlib.Values))
	for k, v := range stdlib.Values {
		vals[k] = v
	}
	for pkg, merged := range stdlib.TestOverlay() {
		vals[pkg] = merged
	}
	i.ImportPackageValues(vals)
	i.ImportPackageConsts(stdlib.ConstValues)
	mfs := wireFS(i)
	// Test-source FS feeds `mvm test <stdlib-pkg>` external _test.go files
	// against the existing reflect bridge. Kept off the shared wireFS so
	// `mvm run`/REPL never resolve GOROOT. nil host -> "no test sources".
	i.SetTestSourceFS(stdlib.GorootTestFS())
	i.AutoImportPackages()
	if trace.line {
		i.SetTracing(true)
	}
	if trace.op {
		i.SetTraceOps(true)
	}
	// Route the interpreter's stdout through liveStdout so fmt.Print* (which
	// patchFmtBindings binds to the interpreter writer) follows testing's
	// per-example os.Stdout redirection -- otherwise Example output written via
	// fmt escapes the capture while output written via the bridged os.Stdout
	// (e.g. io.Copy(os.Stdout, ...)) does not, interleaving them wrongly.
	i.SetIO(os.Stdin, liveStdout{}, os.Stderr)
	return i, mfs
}

// sourceLoadsWithoutTests reports whether target's source compiles with tests
// excluded, returning the loaded interpreter on success.
func sourceLoadsWithoutTests(trace traceFlag, target string) (*interp.Interp, bool) {
	src, _ := newTestInterp(trace)
	src.SetIncludeTests(false)
	if _, err := src.Eval(target, ""); err != nil {
		return nil, false
	}
	return src, true
}

// liveStdout forwards each write to the current os.Stdout rather than capturing
// it once, so writes track reassignments of os.Stdout (testing redirects it
// around every example to capture output).
type liveStdout struct{}

func (liveStdout) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

// failingTestFile extracts the basename of the bridged-stdlib external test
// file named by a compile error's source position (e.g.
// "strings/replace_test.go:326:32: undefined: Replacer" -> "replace_test.go").
// Only files directly under target ending in _test.go qualify. Returns ""
// when the error carries no such position, so the caller stops retrying
// rather than looping.
func failingTestFile(err error, target string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(target) + `/([^/:\s]+_test\.go):\d+`)
	if m := re.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return ""
}

// runTestsInDir runs the test driver with cwd set to dir, restoring cwd on
// return. Cwd matters because `go test` chdirs to the package source dir, and
// any test using testdata-relative paths depends on that.
// pkgPath identifies the bridged-stdlib import path so the driver can apply
// the stdlib.Incompat skiplist; pass "" for local-dir runs.
func runTestsInDir(i *interp.Interp, dir, pkgPath string, flushStats func()) error {
	prev, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(prev) }()
	return runTestDriver(i, pkgPath, flushStats)
}

// materializePkgDir copies the import-path subtree of fsys into a fresh
// temp directory so test code can resolve testdata-relative paths from cwd.
// On stat-miss returns ("", nil, nil) -- the caller has nothing to chdir to
// and falls through (e.g. targets outside the modfs reach).
func materializePkgDir(fsys fs.FS, importPath string) (string, func(), error) {
	fi, err := fs.Stat(fsys, importPath)
	if err != nil {
		return "", nil, nil //nolint:nilerr
	}
	if !fi.IsDir() {
		return "", nil, fmt.Errorf("%s: not a package directory", importPath)
	}
	tmp, err := os.MkdirTemp("", "mvm-test-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	tmpRoot, _ := filepath.Abs(tmp)
	walkErr := fs.WalkDir(fsys, importPath, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(importPath, p)
		if rerr != nil {
			return rerr
		}
		dst := filepath.Join(tmpRoot, rel)
		// Defense in depth: refuse zip entries whose joined path escapes
		// tmpRoot (a malformed module proxy could serve "../" entries).
		if !strings.HasPrefix(dst, tmpRoot+string(filepath.Separator)) && dst != tmpRoot {
			return fmt.Errorf("refusing entry outside temp dir: %s", p)
		}
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700) // dst validated above
		}
		src, oerr := fsys.Open(p)
		if oerr != nil {
			return oerr
		}
		defer func() { _ = src.Close() }()
		out, cerr := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // dst validated above
		if cerr != nil {
			return cerr
		}
		if _, err := io.Copy(out, src); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
	if walkErr != nil {
		cleanup()
		return "", nil, walkErr
	}
	return tmp, cleanup, nil
}

func evalLocalDir(i *interp.Interp, absDir string, entries []os.DirEntry) error {
	var paths []string
	hasTest := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		paths = append(paths, filepath.Join(absDir, e.Name()))
		if strings.HasSuffix(e.Name(), "_test.go") {
			hasTest = true
		}
	}
	if !hasTest {
		return fmt.Errorf("no *_test.go files found in %s", absDir)
	}
	for _, p := range paths {
		buf, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if _, err := i.Eval(p, string(buf)); err != nil {
			return err
		}
	}
	return nil
}

// filterTopLevelTests applies the user's -test.run / -test.skip patterns to
// the top-level Test* name list, matching what testing's own matcher would
// admit (only the first path segment is consulted -- subtest filtering still
// happens inside testing). args is os.Args[1:] (post-rewriteTestFlags).
func filterTopLevelTests(names, args []string) []string {
	runRE, skipRE := compileTestFilters(args)
	if runRE == nil && skipRE == nil {
		return names
	}
	out := names[:0]
	for _, name := range names {
		if runRE != nil && !runRE.MatchString(name) {
			continue
		}
		if skipRE != nil && skipRE.MatchString(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// compileTestFilters extracts -test.run / -test.skip patterns from args and
// compiles their first path segment to a *regexp.Regexp. A nil result means
// "no filter for that side". Compile errors yield nil (we mirror testing's
// behavior of failing later in its own verify step).
func compileTestFilters(args []string) (run, skip *regexp.Regexp) {
	var runPat, skipPat string
	for i, a := range args {
		switch {
		case a == "-test.run" && i+1 < len(args):
			runPat = args[i+1]
		case strings.HasPrefix(a, "-test.run="):
			runPat = a[len("-test.run="):]
		case a == "-test.skip" && i+1 < len(args):
			skipPat = args[i+1]
		case strings.HasPrefix(a, "-test.skip="):
			skipPat = a[len("-test.skip="):]
		}
	}
	compile := func(pat string) *regexp.Regexp {
		if i := strings.Index(pat, "/"); i >= 0 {
			pat = pat[:i]
		}
		if pat == "" {
			return nil
		}
		re, _ := regexp.Compile(pat)
		return re
	}
	return compile(runPat), compile(skipPat)
}

// runTestDriver synthesizes the _testmain Eval that drives the loaded test
// package's Test*/Benchmark*/Example* funcs through native
// testing.MainStart(...).Run(). Run executes the suite, prints the
// package-level PASS/FAIL line, and returns the exit code -- unlike
// testing.Main, which ends in os.Exit and never returns -- so flushStats can
// emit the -stat summary *after* PASS/FAIL. statDeps supplies the unexported
// testDeps argument MainStart requires; the matcher it exposes
// (regexp.MatchString, for -run/-bench/-skip) stays fully native, avoiding the
// re-entrant mvm bridge that an interpreted matcher would impose on every test
// name. Benchmarks run only when -bench is given; testing filters them by that
// flag, so the full Benchmark* list is passed unfiltered. See ADR-019.
func runTestDriver(i *interp.Interp, pkgPath string, flushStats func()) error {
	testNames := filterTopLevelTests(i.FuncNames("Test"), os.Args[1:])
	benchNames := i.FuncNames("Benchmark")
	examples := collectExamples(i)
	if len(testNames)+len(benchNames)+len(examples) == 0 {
		fmt.Fprintln(os.Stderr, "testing: warning: no tests to run")
		return nil
	}

	var exitCode int
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"mvmtest": {
			"Run": reflect.ValueOf(func(tests []testing.InternalTest, benches []testing.InternalBenchmark, exs []testing.InternalExample) {
				exitCode = testing.MainStart(statDeps{}, tests, benches, nil, exs).Run()
			}),
			// SkipFn builds a *native* t.Skip(reason) closure, so the skip path
			// stays entirely outside the interpreter (an interpreted shim
			// crashes inside makeCallFunc when invoked with the bridged
			// *testing.T arg). See stdlib.Incompat usage below.
			"SkipFn": reflect.ValueOf(func(reason string) func(*testing.T) {
				return func(t *testing.T) { t.Skip(reason) }
			}),
		},
	})
	i.AutoImportPackages()

	var driver strings.Builder
	driver.WriteString("mvmtest.Run([]testing.InternalTest{")
	for _, name := range testNames {
		if reason := stdlib.SkipReason(pkgPath, name); reason != "" {
			// Architectural-limit skiplist: route through mvmtest.SkipFn so
			// the run shows --- SKIP instead of --- FAIL, keeping
			// compat/gen.go's pass/fail ratio honest.
			fmt.Fprintf(&driver, "{Name: %q, F: mvmtest.SkipFn(%q)},", name, "mvm: "+reason)
			continue
		}
		fmt.Fprintf(&driver, "{Name: %q, F: %s},", name, name)
	}
	driver.WriteString("}, []testing.InternalBenchmark{")
	for _, name := range benchNames {
		fmt.Fprintf(&driver, "{Name: %q, F: %s},", name, name)
	}
	driver.WriteString("}, []testing.InternalExample{")
	for _, e := range examples {
		fmt.Fprintf(&driver, "{Name: %q, F: %s, Output: %q, Unordered: %t},",
			e.name, e.name, e.output, e.unordered)
	}
	driver.WriteString("})")
	// Continue the suite on an unrecovered goroutine panic (log it, keep running
	// the other tests) instead of aborting the whole run; fail the run afterward.
	i.SetGoroutineFaultContinue(true)
	_, err := i.Eval("_testmain", driver.String())
	flushStats()
	if err != nil {
		return err
	}
	if exitCode == 0 && i.GoroutineFault() != nil {
		exitCode = 2 // a goroutine panicked during the suite (already logged)
	}
	if exitCode != 0 {
		return &interp.ExitError{Code: exitCode}
	}
	return nil
}
