// The mvm command interprets Go programs.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// newRemoteFS builds the parser's remote-FS fallback from the GOPROXY
// environment variable, mirroring the Go toolchain's semantics:
//
//   - unset / empty: use the default public proxy
//   - "off":         disable network imports (returns nil)
//   - any URL list:  use the first URL entry as the proxy; "direct"/"off"
//     entries disable since modfs has no direct VCS path
func newRemoteFS() fs.FS {
	p := os.Getenv("GOPROXY")
	if p == "" {
		return modfs.New(modfs.Options{})
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == ',' || r == '|' }) {
		switch strings.TrimSpace(part) {
		case "":
			continue
		case "off", "direct":
			return nil
		default:
			return modfs.New(modfs.Options{Proxy: strings.TrimSpace(part)})
		}
	}
	return nil
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

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: mvm <command> [arguments]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Commands:")
	_, _ = fmt.Fprintln(w, "  run     run a Go source file, evaluate an expression, or start the REPL")
	_, _ = fmt.Fprintln(w, "  test    run Go tests in a package directory")
	_, _ = fmt.Fprintln(w, "  version print the mvm version, OS, and architecture")
	_, _ = fmt.Fprintln(w, "  help    show this help")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, `Use "mvm <command> -h" for details on a command.`)
}

func runCmd(arg []string) error {
	var str string
	rflag := flag.NewFlagSet("run", flag.ContinueOnError)
	rflag.Usage = func() {
		fmt.Println("Usage: mvm run [options] [path] [args]")
		fmt.Println("Options:")
		rflag.PrintDefaults()
	}
	rflag.StringVar(&str, "e", "", "string to eval")
	if err := rflag.Parse(arg); err != nil {
		return err
	}
	args := rflag.Args()

	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetRemoteFS(newRemoteFS())

	out := &newlineTracker{w: os.Stdout}
	i.SetIO(os.Stdin, out, os.Stderr)

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

const testUsageText = `Usage: mvm test [target] [testing-flags]
Runs Go tests found in *_test.go files of the given target. Target may be a
local directory (default ".") or an import path (e.g. "github.com/google/uuid")
fetched dynamically via the Go module proxy.
Flags after [target] are forwarded to testing.Main; use the -test. prefix
(e.g. -test.v, -test.run REGEX).
`

// testCmd runs the tests of a Go package. The target is either a local
// directory (existing files Eval'd individually, current behavior) or an
// import path resolved through the FS chain incl. modfs (loaded as one
// package via dir-mode ParseAll so cross-file refs resolve).
func testCmd(arg []string) error {
	tflag := flag.NewFlagSet("test", flag.ContinueOnError)
	tflag.Usage = func() { _, _ = fmt.Fprint(os.Stdout, testUsageText) }
	if err := tflag.Parse(arg); err != nil {
		return err
	}
	args := tflag.Args()

	target := "."
	var pass []string
	if len(args) > 0 {
		target = args[0]
		pass = args[1:]
	}

	os.Args = append([]string{"mvm-test"}, pass...)

	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetRemoteFS(newRemoteFS())
	i.AutoImportPackages()
	i.SetIO(os.Stdin, os.Stdout, os.Stderr)

	// Try target as a local directory first; fall back to import-path
	// resolution (modfs / stdlibfs / pkgfs) on miss.
	if absDir, aerr := filepath.Abs(target); aerr == nil {
		if entries, rerr := os.ReadDir(absDir); rerr == nil {
			if err := evalLocalDir(i, absDir, entries); err != nil {
				return err
			}
			return runTestDriver(i)
		}
	}
	i.SetIncludeTests(true)
	if _, err := i.Eval(target, ""); err != nil {
		return fmt.Errorf("loading %q: %w", target, err)
	}
	return runTestDriver(i)
}

// evalLocalDir Evals each .go file in a local directory in turn.
// Mirrors mvm test's pre-dynamic-import behavior.
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

// runTestDriver synthesizes a testing.Main call over all Test* funcs
// registered in the interpreter's symbol table and runs it.
func runTestDriver(i *interp.Interp) error {
	testNames := i.FuncNames("Test")
	if len(testNames) == 0 {
		fmt.Fprintln(os.Stderr, "testing: warning: no tests to run")
		return nil
	}

	var driver strings.Builder
	driver.WriteString("testing.Main(func(p, s string) (bool, error) { return true, nil }, []testing.InternalTest{")
	for _, name := range testNames {
		fmt.Fprintf(&driver, "{Name: %q, F: %s},", name, name)
	}
	driver.WriteString("}, nil, nil)")
	_, err := i.Eval("_testmain", driver.String())
	return err
}
