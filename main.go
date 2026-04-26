// The mvm command interprets programs.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/jsonx"
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
		_, err = i.Eval("m:"+str, str)
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
		_, err = i.Eval("f:"+fpath, string(buf))
	}
	// Ensure output ends with a newline so the shell prompt is not overwritten.
	if out.written && out.last != '\n' {
		_, _ = fmt.Fprintln(os.Stdout)
	}
	return err
}

var (
	testFuncRE  = regexp.MustCompile(`(?m)^func\s+(Test[A-Z][A-Za-z0-9_]*)\s*\(\s*\w+\s+\*testing\.T\s*\)`)
	pkgClauseRE = regexp.MustCompile(`(?m)^package\s+\w+\s*$`)
)

func testCmd(arg []string) error {
	tflag := flag.NewFlagSet("test", flag.ContinueOnError)
	tflag.Usage = func() {
		fmt.Println("Usage: mvm test [dir] [testing-flags]")
		fmt.Println("Runs Go tests found in *_test.go files of the given package directory (default \".\").")
		fmt.Println("Flags after [dir] are forwarded to testing.Main; use the -test. prefix (e.g. -test.v, -test.run REGEX).")
	}
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
	var pkgSources, testSources []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		buf, err := os.ReadFile(filepath.Join(absDir, e.Name()))
		if err != nil {
			return err
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			testSources = append(testSources, string(buf))
		} else {
			pkgSources = append(pkgSources, string(buf))
		}
	}
	if len(testSources) == 0 {
		return fmt.Errorf("no *_test.go files found in %s", absDir)
	}

	seen := map[string]bool{}
	var testNames []string
	for _, s := range testSources {
		for _, m := range testFuncRE.FindAllStringSubmatch(s, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				testNames = append(testNames, m[1])
			}
		}
	}
	if len(testNames) == 0 {
		fmt.Fprintln(os.Stderr, "testing: warning: no tests to run")
		return nil
	}

	var b strings.Builder
	b.WriteString("package main\n\nimport \"testing\"\n\n")
	for _, s := range pkgSources {
		b.WriteString(pkgClauseRE.ReplaceAllString(s, ""))
		b.WriteString("\n")
	}
	for _, s := range testSources {
		b.WriteString(pkgClauseRE.ReplaceAllString(s, ""))
		b.WriteString("\n")
	}
	b.WriteString("func main() {\n")
	b.WriteString("\ttesting.Main(\n")
	b.WriteString("\t\tfunc(pat, str string) (bool, error) { return true, nil },\n")
	b.WriteString("\t\t[]testing.InternalTest{\n")
	for _, name := range testNames {
		fmt.Fprintf(&b, "\t\t\t{Name: %q, F: %s},\n", name, name)
	}
	b.WriteString("\t\t},\n")
	b.WriteString("\t\tnil, nil,\n")
	b.WriteString("\t)\n")
	b.WriteString("}\n")

	os.Args = append([]string{"mvm-test"}, pass...)

	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, os.Stdout, os.Stderr)

	_, err = i.Eval("m:_testmain", b.String())
	return err
}
