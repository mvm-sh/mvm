// The mvm command interprets Go programs.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

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
	log.SetFlags(log.Lshortfile)
	if err := dispatch(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
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
	}
	return runCmd(args)
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "Usage: mvm <command> [arguments]")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Commands:")
	_, _ = fmt.Fprintln(w, "  run    run a Go source file, evaluate an expression, or start the REPL")
	_, _ = fmt.Fprintln(w, "  test   run Go tests in a package directory")
	_, _ = fmt.Fprintln(w, "  help   show this help")
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

const testUsageText = `Usage: mvm test [dir] [testing-flags]
Runs Go tests found in *_test.go files of the given package directory (default ".").
Flags after [dir] are forwarded to testing.Main; use the -test. prefix (e.g. -test.v, -test.run REGEX).
`

func testCmd(arg []string) error {
	tflag := flag.NewFlagSet("test", flag.ContinueOnError)
	tflag.Usage = func() { _, _ = fmt.Fprint(os.Stdout, testUsageText) }
	if err := tflag.Parse(arg); err != nil {
		return err
	}
	args := tflag.Args()

	dir := "."
	var pass []string
	if len(args) > 0 {
		dir = args[0]
		pass = args[1:]
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}
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

	os.Args = append([]string{"mvm-test"}, pass...)

	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.AutoImportPackages()
	i.SetIO(os.Stdin, os.Stdout, os.Stderr)

	for _, p := range paths {
		buf, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if _, err := i.Eval(p, string(buf)); err != nil {
			return err
		}
	}

	testNames := i.FuncNames("Test")
	if len(testNames) == 0 {
		fmt.Fprintln(os.Stderr, "testing: warning: no tests to run")
		return nil
	}
	sort.Strings(testNames)

	var driver strings.Builder
	driver.WriteString("testing.Main(func(p, s string) (bool, error) { return true, nil }, []testing.InternalTest{")
	for _, name := range testNames {
		fmt.Fprintf(&driver, "{Name: %q, F: %s},", name, name)
	}
	driver.WriteString("}, nil, nil)")
	_, err = i.Eval("_testmain", driver.String())
	return err
}
