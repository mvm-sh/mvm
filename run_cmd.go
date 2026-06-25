package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
		str   string    // the string to eval
		trace traceFlag // to print executed code lines
		stat  bool      // to print execution statistics afterward
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
		if errors.Is(err, flag.ErrHelp) { // -h already printed usage
			return nil
		}
		return err
	}
	args := rflag.Args()

	i := interp.NewInterpreter(golang.GoSpec)
	i.UseHostStdio()
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	applyInterpOverrides(i)
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
		var res reflect.Value
		res, err = i.Eval(str, str)
		if err == nil && i.StackLen() == 1 && res.IsValid() {
			_, _ = fmt.Fprintln(out, res)
		}
	case len(args) == 0:
		i.AutoImportPackages()
		return i.Repl(os.Stdin)
	case looksLikeImportPath(args[0]):
		target := args[0]
		i.AutoImportPackages()
		os.Args = append([]string{goparser.PackageName(target)}, args[1:]...)
		if _, err = i.Eval(target, ""); err == nil {
			if _, ok := i.Symbols["main"]; !ok {
				fmt.Fprintf(os.Stderr, "mvm run: %s has no func main; nothing to run\n", target)
			}
		}
	default:
		nfiles := 0
		for nfiles < len(args) && strings.HasSuffix(args[nfiles], ".go") {
			nfiles++
		}
		if nfiles > 1 {
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

func looksLikeImportPath(s string) bool {
	if !strings.ContainsRune(s, '/') || strings.HasSuffix(s, ".go") {
		return false
	}
	_, err := os.Stat(s)
	return err != nil // existing local path -> not an import path
}

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
