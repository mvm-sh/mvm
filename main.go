// The mvm command interprets Go programs.
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
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/stdlib/stdmod"
)

// buildModFS builds the modfs the parser uses for both stdlib redirects
// and third-party imports, applying GOPROXY semantics from the Go
// toolchain:
//
//   - unset / empty: use the default public proxy
//   - "off":         disable network fetches (offline-only modfs)
//   - any URL list:  use the first URL entry as the proxy; "direct"/"off"
//     entries fall back to offline since modfs has no direct VCS path
func buildModFS() *modfs.FS {
	p := os.Getenv("GOPROXY")
	if p == "" {
		return modfs.New(modfs.Options{})
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == ',' || r == '|' }) {
		switch strings.TrimSpace(part) {
		case "":
			continue
		case "off", "direct":
			return modfs.New(modfs.Options{Offline: true})
		default:
			return modfs.New(modfs.Options{Proxy: strings.TrimSpace(part)})
		}
	}
	return modfs.New(modfs.Options{Offline: true})
}

func wireFS(i *interp.Interp) *modfs.FS {
	mfs := buildModFS()
	if err := mfs.Inject(stdmod.ModulePath, stdmod.Version, stdlib.EmbeddedStd()); err != nil {
		panic("modfs inject embedded std: " + err.Error())
	}
	i.SetStdlibFS(stdmod.FS(mfs))
	i.SetRemoteFS(mfs)
	return mfs
}

// traceFlag is a flag.Value for -x that doubles as a bool flag (-x = line trace)
// and a string-valued flag (-x=op, -x=all, -x=line,op).
type traceFlag struct{ line, op bool }

func (t *traceFlag) IsBoolFlag() bool { return true }

func (t *traceFlag) String() string {
	switch {
	case t.line && t.op:
		return "all"
	case t.line:
		return "line"
	case t.op:
		return "op"
	}
	return ""
}

func (t *traceFlag) Set(s string) error {
	if s == "true" { // bare -x
		t.line = true
		return nil
	}
	line, op := interp.ParseTraceModes(s)
	if !line && !op {
		return fmt.Errorf("unknown trace mode %q (want line, op, all, or comma list)", s)
	}
	t.line, t.op = line, op
	return nil
}

// setupStats returns a once-guarded flush closure for the -stat summary,
// or a no-op when enabled is false.
func setupStats(i *interp.Interp, mfs *modfs.FS, enabled bool) func() {
	if !enabled {
		return func() {}
	}
	return sync.OnceFunc(func() {
		out := interp.FormatStats(i)
		if mfs != nil {
			ns := mfs.NetStats()
			out += fmt.Sprintf("  network:  %d requests, %s in %v\n",
				ns.Requests, humanBytes(ns.BytesFetched), ns.FetchTime)
		}
		_, _ = fmt.Fprint(os.Stderr, out)
	})
}

// humanBytes formats a byte count with a binary-unit suffix.
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.2f KiB", float64(n)/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.2f MiB", float64(n)/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1024*1024*1024))
	}
}

// newlineTracker wraps a writer and tracks whether the last byte written was a newline.
type newlineTracker struct {
	w       io.Writer
	written bool
	last    byte
}

func (t *newlineTracker) Write(p []byte) (int, error) {
	if len(p) > 0 {
		t.written = true
		t.last = p[len(p)-1]
	}
	return t.w.Write(p)
}

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		var ee *interp.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.Code)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func versionString() string {
	v, gv := "(devel)", ""
	if bi, ok := debug.ReadBuildInfo(); ok {
		gv = bi.GoVersion
		v = bi.Main.Version
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				v = s.Value
				break
			}
		}
	}
	return fmt.Sprintf("%.12s %s %s/%s", v, gv, runtime.GOOS, runtime.GOARCH)
}

func dispatch(args []string) error {
	if len(args) == 0 {
		return runCmd(nil)
	}
	switch args[0] {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return nil
	case "run":
		return runCmd(args[1:])
	case "test":
		return testCmd(args[1:])
	case "version", "-v", "--version":
		fmt.Println(versionString())
		return nil
	}
	return runCmd(args)
}

const usageText = `Usage: mvm <command> [arguments]

Commands:
  run     run a Go source file, evaluate an expression, or start the REPL
  test    run Go tests in a package directory
  version print the mvm version, OS, and architecture
  help    show this help

Use "mvm <command> -h" for details on a command.
`

func usage(w io.Writer) { _, _ = fmt.Fprint(w, usageText) }

const runUsageText = `Usage: mvm run [options] [path] [args]
Options:
`

func runCmd(arg []string) error {
	var (
		str   string
		trace traceFlag
		stat  bool
	)
	rflag := flag.NewFlagSet("run", flag.ContinueOnError)
	rflag.Usage = func() {
		_, _ = fmt.Fprint(os.Stdout, runUsageText)
		rflag.PrintDefaults()
	}
	rflag.StringVar(&str, "e", "", "string to eval")
	rflag.Var(&trace, "x", "trace mode (bare -x = line; -x=op, -x=all, -x=line,op)")
	rflag.BoolVar(&stat, "stat", false, "print compile/run statistics on exit")
	if err := rflag.Parse(arg); err != nil {
		if err == flag.ErrHelp { // -h already printed usage
			return nil
		}
		return err
	}
	args := rflag.Args()

	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	mfs := wireFS(i)
	if trace.line {
		i.SetTracing(true)
	}
	if trace.op {
		i.SetTraceOps(true)
	}

	out := &newlineTracker{w: os.Stdout}
	i.SetIO(os.Stdin, out, os.Stderr)
	defer setupStats(i, mfs, stat)()

	var err error
	switch {
	case str != "":
		i.AutoImportPackages()
		_, err = i.Eval(str, str)
	case len(args) == 0:
		i.AutoImportPackages()
		return i.Repl(os.Stdin)
	default:
		fpath := filepath.Clean(args[0])
		var buf []byte
		buf, err = os.ReadFile(fpath)
		if err != nil {
			return err
		}
		src := string(buf)
		if strings.HasPrefix(src, "#!") {
			if nl := strings.IndexByte(src, '\n'); nl >= 0 {
				src = src[nl:]
			} else {
				src = ""
			}
		}
		_, err = i.Eval(fpath, src)
	}
	// Ensure output ends with a newline so the shell prompt is not overwritten.
	if out.written && out.last != '\n' {
		_, _ = fmt.Fprintln(os.Stdout)
	}
	return err
}

const testUsageText = `Usage: mvm test [-x] [-stat] [target] [test flags]
Runs Go tests found in *_test.go files of the given target.
Target may be a local directory (default ".") or an import path
(e.g. "github.com/google/uuid") fetched dynamically via the Go module proxy.
Test flags use the same names as "go test": -v for verbose output,
-run REGEX to select tests, -count N, -short, etc.
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
		if err == flag.ErrHelp { // -h already printed usage
			return nil
		}
		return err
	}

	os.Args = append([]string{"mvm-test"}, rewriteTestFlags(testFlags)...)

	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
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
	i.SetIO(os.Stdin, os.Stdout, os.Stderr)
	// flushStats covers the load-error / no-tests-found paths via defer;
	// the driver itself fires flushStats from a per-test counter once the
	// last test body returns (it can't run from defer because testing.Main
	// ends in a host os.Exit). flushStats is sync.OnceFunc so the defer
	// becomes a no-op when the counter already fired.
	flushStats := setupStats(i, mfs, stat)
	defer flushStats()

	// Try target as a local directory first; fall back to import-path
	// resolution (modfs / stdlibfs / pkgfs) on miss.
	if absDir, aerr := filepath.Abs(target); aerr == nil {
		if entries, rerr := os.ReadDir(absDir); rerr == nil {
			if err := evalLocalDir(i, absDir, entries); err != nil {
				return err
			}
			return runTestsInDir(i, absDir, flushStats)
		}
	}
	i.SetIncludeTests(true)
	if _, err := i.Eval(target, ""); err != nil {
		return fmt.Errorf("loading %q: %w", target, err)
	}
	// modfs serves the package from memory, so tests using testdata-relative
	// paths see whatever cwd mvm was launched from. Spill the subtree to a
	// temp dir and chdir there to mirror `go test`'s setup.
	if mfs != nil {
		dir, cleanup, err := materializePkgDir(mfs, target)
		if err != nil {
			return err
		}
		if dir != "" {
			defer cleanup()
			return runTestsInDir(i, dir, flushStats)
		}
	}
	// Bridged-stdlib case: external test files came from $GOROOT/src/<target>.
	// chdir there so testdata-relative paths resolve. No copy needed since
	// stdlib tests read but do not write their testdata subtrees.
	if dir := stdlib.GorootSrcDir(target); dir != "" {
		return runTestsInDir(i, dir, flushStats)
	}
	return runTestDriver(i, flushStats)
}

// runTestsInDir runs the test driver with cwd set to dir, restoring cwd on
// return. Cwd matters because `go test` chdirs to the package source dir, and
// any test using testdata-relative paths depends on that.
func runTestsInDir(i *interp.Interp, dir string, onAllDone func()) error {
	prev, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(prev) }()
	return runTestDriver(i, onAllDone)
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
			return os.MkdirAll(dst, 0o700) //nolint:gosec // dst validated above
		}
		src, oerr := fsys.Open(p)
		if oerr != nil {
			return oerr
		}
		defer func() { _ = src.Close() }()
		out, cerr := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // dst validated above
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
// package's Test* funcs through native testing.Main. Each test is wrapped
// via a host-side mvmtest.WrapTest that registers t.Cleanup(oneDone); when
// the counter hits zero, onAllDone flushes -stat output before testing's
// native os.Exit terminates the host. See ADR-018 for the design rationale.
func runTestDriver(i *interp.Interp, onAllDone func()) error {
	testNames := filterTopLevelTests(i.FuncNames("Test"), os.Args[1:])
	if len(testNames) == 0 {
		fmt.Fprintln(os.Stderr, "testing: warning: no tests to run")
		return nil
	}

	var remaining atomic.Int32
	remaining.Store(int32(len(testNames))) //nolint:gosec // bounded by Test* count
	oneDone := func() {
		if remaining.Add(-1) == 0 {
			onAllDone()
		}
	}
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"mvmtest": {
			"WrapTest": reflect.ValueOf(func(f func(*testing.T)) func(*testing.T) {
				return func(t *testing.T) {
					t.Cleanup(oneDone)
					f(t)
				}
			}),
		},
	})
	i.AutoImportPackages()

	var driver strings.Builder
	// Pass regexp.MatchString directly rather than wrapping it in an interpreted
	// closure: native testing.Main calls the matcher via reflect for each test
	// (and per slash-separated sub-name) when -run/-skip is set, so wrapping it
	// in `func(pat, name string) (bool, error) { return regexp.MatchString(...) }`
	// makes every match a re-entrant mvm Machine spin-up that copies the host
	// data segment. On large packages (e.g. golang.org/x/text/language) that
	// snowballed into minutes-long hangs and gigabytes of allocations under
	// `mvm test -run=X`. Passing the native func value avoids the bridge.
	driver.WriteString("testing.Main(regexp.MatchString, []testing.InternalTest{")
	for _, name := range testNames {
		fmt.Fprintf(&driver, "{Name: %q, F: mvmtest.WrapTest(%s)},", name, name)
	}
	driver.WriteString("}, nil, nil)")
	_, err := i.Eval("_testmain", driver.String())
	return err
}
