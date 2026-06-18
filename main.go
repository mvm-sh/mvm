// The mvm command interprets Go programs.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/mvm-sh/mvm/interp"
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
	stdlib.RegisterModuleFS(mfs)
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

func setupStats(i *interp.Interp, mfs *modfs.FS, enabled bool) func() {
	if !enabled {
		return func() {}
	}
	stderr := os.Stderr
	return sync.OnceFunc(func() {
		out := interp.FormatStats(i)
		if mfs != nil {
			ns := mfs.NetStats()
			out += fmt.Sprintf("  network:  %d requests, %s in %v\n",
				ns.Requests, humanBytes(ns.BytesFetched), ns.FetchTime)
		}
		_, _ = fmt.Fprint(stderr, out)
	})
}

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

func main() {
	watchProgressSignal() // Ctrl-C prints execution state; double-press aborts
	err := dispatch(os.Args[1:])
	// MVM_WORDDROPS telemetry: print before any os.Exit so a failing `mvm test`
	// still reports its dropped word-shapes (see ADR-022).
	if r := interp.WordShapeDropReport(); r != "" {
		fmt.Fprint(os.Stderr, r)
	}
	if err != nil {
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
  run     run a Go file or remote main package, eval an expression, or start the REPL
  test    run Go tests in a package directory
  version print the mvm version, OS, and architecture
  help    show this help

Use "mvm <command> -h" for details on a command.
`

func usage(w io.Writer) { _, _ = fmt.Fprint(w, usageText) }
