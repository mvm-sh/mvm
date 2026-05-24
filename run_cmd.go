package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

const runUsageText = `Usage: mvm run [options] [path|import-path] [args]
Runs a local .go file, or a remote main package given by import path
(e.g. github.com/mvm-sh/mvm/cmd/mvmlint) fetched via the Go module proxy.
Arguments after the path/import-path are forwarded as the program's os.Args.
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
	i.ImportPackageConsts(stdlib.ConstValues)
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
	case looksLikeImportPath(args[0]):
		// Remote/import-path main package. Empty source routes Eval through
		// package loading (pkgfs -> stdlibfs -> remotefs); the loaded package's
		// main() is invoked automatically by interp.Eval. Forward the trailing
		// args as the program's os.Args (a host pointer bridge); os.Args[0] is
		// the short program name, matching the convention `go run` uses.
		target := args[0]
		i.AutoImportPackages()
		os.Args = append([]string{goparser.PackageName(target)}, args[1:]...)
		if _, err = i.Eval(target, ""); err == nil {
			if _, ok := i.Symbols["main"]; !ok {
				fmt.Fprintf(os.Stderr, "mvm run: %s has no func main; nothing to run\n", target)
			}
		}
	default:
		// Count the leading run of .go file arguments (go run semantics: every
		// named .go file forms the main package; the first non-.go arg starts the
		// program's os.Args).
		nfiles := 0
		for nfiles < len(args) && strings.HasSuffix(args[nfiles], ".go") {
			nfiles++
		}
		if nfiles > 1 {
			// Compile all named files as one unit so they see each other's
			// top-level symbols regardless of file or declaration order.
			sources := make([]goparser.PackageSource, 0, nfiles)
			for _, a := range args[:nfiles] {
				fpath := filepath.Clean(a)
				buf, rerr := os.ReadFile(fpath)
				if rerr != nil {
					return rerr
				}
				sources = append(sources, goparser.PackageSource{Name: fpath, Content: string(buf)})
			}
			os.Args = append([]string{filepath.Base(filepath.Clean(args[0]))}, args[nfiles:]...)
			_, err = i.EvalFiles(sources)
			break
		}
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
		os.Args = append([]string{filepath.Base(fpath)}, args[1:]...)
		_, err = i.Eval(fpath, src)
	}
	// Ensure output ends with a newline so the shell prompt is not overwritten.
	if out.written && out.last != '\n' {
		_, _ = fmt.Fprintln(os.Stdout)
	}
	return err
}

// looksLikeImportPath reports whether s should be treated as a remote/package
// import path rather than a local .go file: it contains a slash, does not end
// in ".go", and is not the path of an existing local file. Mirrors
// comp.looksLikePkgPath (unexported) with a local-file guard so a real path
// always wins over a network fetch.
func looksLikeImportPath(s string) bool {
	if !strings.ContainsRune(s, '/') || strings.HasSuffix(s, ".go") {
		return false
	}
	_, err := os.Stat(s)
	return err != nil // existing local path -> not an import path
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
