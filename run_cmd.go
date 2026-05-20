package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
)

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
