package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/vm"
)

// withPanicDiag prints an unrecovered test panic's full mvm diagnostic (location
// + snippet + stack) before Go's testing re-raises it raw, which would otherwise
// drop the captured location as the panic crosses back into native code.
func withPanicDiag(f func(*testing.T)) func(*testing.T) {
	return func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				if diag, ok := vm.FormatPanic(r); ok {
					fmt.Fprintln(os.Stderr, diag)
				}
				panic(r) // let testing record the failure and abort
			}
		}()
		f(t)
	}
}

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
	// The target may sit anywhere among the test flags (`go test -run X ./pkg`).
	// The first bare token that isn't a flag's separate-form value is the target.
	targetSet := false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			testFlags = append(testFlags, a)
			if name, hasValue := splitFlag(a); !hasValue && testFlagTakesValue(name) && i+1 < len(rest) {
				i++
				testFlags = append(testFlags, rest[i])
			}
			continue
		}
		if !targetSet {
			target = a
			targetSet = true
			continue
		}
		testFlags = append(testFlags, a)
	}
	return mvmFlags, target, testFlags
}

// splitFlag returns a flag token's name (sans dashes) and whether it has an attached =value.
func splitFlag(a string) (name string, hasValue bool) {
	name = strings.TrimLeft(a, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		return before, true
	}
	return name, false
}

// testFlagTakesValue reports whether a `go test` flag consumes the next arg
// (-flag value); booleans like -v/-short do not. Extend for new Go test flags.
func testFlagTakesValue(name string) bool {
	name = strings.TrimPrefix(name, "test.")
	switch name {
	case "bench", "benchtime", "blockprofile", "blockprofilerate",
		"count", "coverprofile", "covermode", "coverpkg", "cpu", "cpuprofile",
		"fuzz", "fuzztime", "fuzzminimizetime", "gocoverdir", "list",
		"memprofile", "memprofilerate", "mutexprofile", "mutexprofilefraction",
		"outputdir", "parallel", "run", "shuffle", "skip", "timeout", "trace":
		return true
	}
	return false
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
		if _, rerr := os.ReadDir(absDir); rerr == nil {
			i, mfs := newTestInterp(trace)
			i.PanicNilCompat = panicNilCompat(findGoModDir(absDir))
			flushStats := setupStats(i, mfs, stat)
			if err := evalLocalDir(i, absDir); err != nil {
				flushStats()
				return err
			}
			return runTestsInDir(i, absDir, "", flushStats)
		}
	}

	if reason := stdlib.UntestableReason(target); reason != "" {
		fmt.Fprintf(os.Stderr, "mvm test: %s: untestable (%s)\n", target, reason)
		return nil
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
	pkgDir := "" // materialized module subtree; cwd while loading AND running
	for {
		i, mfs := newTestInterp(trace)
		if mfs != nil {
			i.PanicNilCompat = panicNilCompat(findGoModFS(mfs, target))
			// Materialize and chdir BEFORE Eval: top-level var inits in
			// _test.go files may read testdata-relative paths at load time,
			// not run time, so cwd must already be the package dir.
			if pkgDir == "" {
				dir, cleanup, merr := materializePkgDir(mfs, target)
				if merr != nil {
					return merr
				}
				if dir != "" {
					defer cleanup()
					prev, werr := os.Getwd()
					if werr != nil {
						return werr
					}
					if cerr := os.Chdir(dir); cerr != nil {
						return cerr
					}
					defer func() { _ = os.Chdir(prev) }()
					pkgDir = dir
				}
			}
		}
		flushStats := setupStats(i, mfs, stat)
		i.SetIncludeTests(true)
		i.SetTestSkipFiles(skip)
		if _, err := i.Eval(target, ""); err != nil {
			if f := failingTestFile(err, target); f != "" && !skip[f] {
				skip[f] = true
				noteSkippedTestFile(target, f, err)
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
		// Also run the target's external `package X_test` tests as a second unit.
		loadExternalTests(i, target)
		// modfs serves the package from memory; cwd is already the materialized
		// subtree (set before Eval) so testdata-relative paths resolve, as with
		// `go test`'s chdir.
		if pkgDir != "" {
			return runTestDriver(i, target, flushStats)
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

func newTestInterp(trace traceFlag) (*interp.Interp, *modfs.FS) {
	i := interp.NewInterpreter(golang.GoSpec)
	// Install bridges, then layer in test-only export_test stand-ins (e.g.
	// strings.StringFind) so external stdlib tests that use them resolve. The
	// overlay is test-runner-only, so `mvm run` never sees these symbols.
	vals := make(map[string]map[string]reflect.Value, len(stdlib.Values))
	maps.Copy(vals, stdlib.Values)
	maps.Copy(vals, stdlib.TestOverlay())
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
	i.SetIO(os.Stdin, liveStdout{}, os.Stderr)
	return i, mfs
}

func sourceLoadsWithoutTests(trace traceFlag, target string) (*interp.Interp, bool) {
	src, _ := newTestInterp(trace)
	src.SetIncludeTests(false)
	if _, err := src.Eval(target, ""); err != nil {
		return nil, false
	}
	return src, true
}

func loadExternalTests(i *interp.Interp, target string) {
	ext := i.ExternalTestSources()
	if len(ext) == 0 {
		return
	}
	// Publish the target so the external unit reuses it instead of re-importing.
	i.PublishCompiledPackage(target)
	// Best-effort: recover a codegen panic so one bad file doesn't sink the run.
	i.LenientCompile = true
	defer func() { i.LenientCompile = false }()

	if loadExternalUnit(i, target, ext) {
		return
	}
	// The unit failed without naming a droppable file (a recovered panic has no
	// position); load each file alone so self-contained ones still load.
	for _, s := range ext {
		if _, err := i.EvalFiles([]goparser.PackageSource{s}); err != nil {
			noteSkippedTestFile(target, s.Name, err)
		}
	}
}

func noteSkippedTestFile(target, name string, err error) {
	if strings.Contains(name, "/") { // already a qualified external-test path
		fmt.Fprintf(os.Stderr, "mvm test: skipping %s (%v)\n", name, err)
		return
	}
	fmt.Fprintf(os.Stderr, "mvm test: skipping %s/%s (%v)\n", target, name, err)
}

func loadExternalUnit(i *interp.Interp, target string, files []goparser.PackageSource) bool {
	skip := map[string]bool{}
	for {
		var srcs []goparser.PackageSource
		for _, s := range files {
			if !skip[s.Name] {
				srcs = append(srcs, s)
			}
		}
		if len(srcs) == 0 {
			return true
		}
		_, err := i.EvalFiles(srcs)
		if err == nil {
			return true
		}
		f := externalFailingFile(err, srcs)
		if f == "" {
			return false
		}
		skip[f] = true
		noteSkippedTestFile(target, f, err)
	}
}

func externalFailingFile(err error, files []goparser.PackageSource) string {
	msg := err.Error()
	for _, s := range files {
		needle := s.Name + ":"
		for from := 0; from <= len(msg)-len(needle); {
			i := strings.Index(msg[from:], needle)
			if i < 0 {
				break
			}
			at := from + i
			if at == 0 || !isFilenameByte(msg[at-1]) {
				return s.Name
			}
			from = at + 1
		}
	}
	return ""
}

func isFilenameByte(b byte) bool {
	return b == '_' || b == '.' || b == '-' || b == '/' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// liveStdout forwards each write to the current os.Stdout rather than capturing
// it once, so writes track reassignments of os.Stdout.
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

func evalLocalDir(i *interp.Interp, absDir string) error {
	// One unit, build-tag filtered: cross-file refs resolve and constraint-excluded
	// files (//go:build ignore, !go1.18 siblings) stay out, like `go test`.
	sources, err := i.LoadLocalPackageSources(absDir)
	if err != nil {
		return err
	}
	hasTest := false
	for _, s := range sources {
		if strings.HasSuffix(s.Name, "_test.go") {
			hasTest = true
			break
		}
	}
	if !hasTest {
		return fmt.Errorf("no *_test.go files found in %s", absDir)
	}
	_, err = i.EvalFiles(sources)
	return err
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

// excludeTestMain drops TestMain(m *testing.M): the driver runs the suite
// itself, so running TestMain as an ordinary test would panic in its m.Run().
func excludeTestMain(names []string) []string {
	out := names[:0]
	for _, n := range names {
		if n == "TestMain" {
			continue
		}
		out = append(out, n)
	}
	return out
}

// subtestSkipPattern turns "TestX/Sub#03" into the testing -skip arm
// "^TestX$/^Sub#03$" (each '/'-component anchored+quoted). It fully matches the
// subtest but only partially matches the parent, so the parent still runs.
func subtestSkipPattern(path string) string {
	parts := strings.Split(path, "/")
	for i, c := range parts {
		parts[i] = "^" + regexp.QuoteMeta(c) + "$"
	}
	return strings.Join(parts, "/")
}

// mergeTestSkip OR-combines pat into the -test.skip flag within args, appending
// the flag if absent. testing reads -test.skip from os.Args, so this is how the
// subtest skiplist reaches its matcher alongside any user-supplied skip.
func mergeTestSkip(args []string, pat string) []string {
	orJoin := func(a string) string {
		if a == "" {
			return pat
		}
		return a + "|" + pat
	}
	for i, a := range args {
		switch {
		case a == "-test.skip" && i+1 < len(args):
			args[i+1] = orJoin(args[i+1])
			return args
		case strings.HasPrefix(a, "-test.skip="):
			args[i] = "-test.skip=" + orJoin(a[len("-test.skip="):])
			return args
		}
	}
	return append(args, "-test.skip="+pat)
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
	testNames := excludeTestMain(filterTopLevelTests(i.FuncNames("Test"), os.Args[1:]))
	benchNames := i.FuncNames("Benchmark")
	examples := collectExamples(i)
	if len(testNames)+len(benchNames)+len(examples) == 0 {
		fmt.Fprintln(os.Stderr, "testing: warning: no tests to run")
		return nil
	}

	// Route subtest-path Incompat entries through testing's -skip (after
	// filterTopLevelTests, so the partial-matching parent test isn't dropped).
	if subs := stdlib.SubtestSkips(pkgPath); len(subs) > 0 {
		pats := make([]string, len(subs))
		for n, s := range subs {
			pats[n] = subtestSkipPattern(s.Name)
			fmt.Fprintf(os.Stderr, "--- SKIP: %s (mvm: %s)\n", s.Name, s.Reason)
		}
		os.Args = mergeTestSkip(os.Args, strings.Join(pats, "|"))
	}

	var exitCode int
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"mvmtest": {
			"Run": reflect.ValueOf(func(tests []testing.InternalTest, benches []testing.InternalBenchmark, exs []testing.InternalExample) {
				for n := range tests {
					tests[n].F = withPanicDiag(tests[n].F)
				}
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
		// Examples have no *testing.T to self-skip, so omit a skiplisted one and
		// note it; the matrix counts only --- PASS/FAIL, so omitted != failed.
		if reason := stdlib.SkipReason(pkgPath, e.name); reason != "" {
			fmt.Fprintf(os.Stderr, "--- SKIP: %s (mvm: %s)\n", e.name, reason)
			continue
		}
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

// panicNilCompat reports whether panic(nil) should recover as nil (pre-Go 1.21
// semantics) for the module whose go.mod content is given, mirroring the
// runtime's GODEBUG panicnil default. An explicit GODEBUG setting wins.
func panicNilCompat(goMod []byte) bool {
	for kv := range strings.SplitSeq(os.Getenv("GODEBUG"), ",") {
		if v, ok := strings.CutPrefix(kv, "panicnil="); ok {
			return v == "1"
		}
	}
	for line := range strings.Lines(string(goMod)) {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "go" {
			var major, minor int
			if n, _ := fmt.Sscanf(f[1], "%d.%d", &major, &minor); n == 2 {
				return major == 1 && minor < 21
			}
		}
	}
	return false
}

// findGoModFS returns the go.mod content of the module enclosing pkgPath in
// fsys, walking up the import path; nil if none is found.
func findGoModFS(fsys fs.FS, pkgPath string) []byte {
	for p := pkgPath; p != "." && p != ""; p = filepath.Dir(p) {
		if b, err := fs.ReadFile(fsys, p+"/go.mod"); err == nil {
			return b
		}
	}
	return nil
}

// findGoModDir returns the go.mod content of the module enclosing dir,
// walking up the directory tree; nil if none is found.
func findGoModDir(dir string) []byte {
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			return b
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
}
