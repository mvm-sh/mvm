// The mvm command interprets Go programs.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/stdlib/stdmod"
	"github.com/mvm-sh/mvm/stdlib/stubs"
	"github.com/mvm-sh/mvm/vm"
)

// poolStatsReport formats each stub pool's slot high-water against its
// capacity (MVM_POOLSTATS), highest utilization first.
func poolStatsReport() string {
	stats := stubs.HighWater()
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Used > stats[j].Used
	})
	var b strings.Builder
	b.WriteString("stub pool high-water (MVM_POOLSTATS): name used/cap\n")
	for _, s := range stats {
		if s.Used == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %-12s %d/%d\n", s.Name, s.Used, s.Cap)
	}
	return b.String()
}

// buildModFS builds the modfs the parser uses for both stdlib redirects
// and third-party imports, applying GOPROXY semantics from the Go
// toolchain:
//
//   - unset / empty: use the default public proxy
//   - "off":         disable network fetches (offline-only modfs)
//   - any URL list:  use the first URL entry as the proxy; "direct"/"off"
//     entries fall back to offline since modfs has no direct VCS path
//
// All modes may persist fetched zips to a local cache.
func buildModFS() *modfs.FS {
	opts := modfs.Options{}
	opts.CacheDir, opts.ReadDirs = cacheOptions()
	p := os.Getenv("GOPROXY")
	if p == "" {
		return modfs.New(opts)
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == ',' || r == '|' }) {
		switch strings.TrimSpace(part) {
		case "":
			continue
		case "off", "direct":
			opts.Offline = true
			return modfs.New(opts)
		default:
			opts.Proxy = strings.TrimSpace(part)
			return modfs.New(opts)
		}
	}
	opts.Offline = true
	return modfs.New(opts)
}

func cacheOptions() (cacheDir string, readDirs []string) {
	switch v := os.Getenv("MVMCACHE"); v {
	case "off":
		return "", nil
	case "":
		if ucd, err := os.UserCacheDir(); err == nil {
			cacheDir = filepath.Join(ucd, "mvm", "download")
		}
	default:
		cacheDir = v
	}
	if gmc := goModCacheDownload(); gmc != "" && gmc != cacheDir {
		readDirs = []string{gmc}
	}
	return cacheDir, readDirs
}

func goModCacheDownload() string {
	base := os.Getenv("GOMODCACHE")
	if base == "" {
		gp := os.Getenv("GOPATH")
		if gp == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return ""
			}
			gp = filepath.Join(home, "go")
		}
		if i := strings.IndexByte(gp, os.PathListSeparator); i >= 0 {
			gp = gp[:i] // GOPATH may be a list; first entry wins
		}
		base = filepath.Join(gp, "pkg", "mod")
	}
	dir := filepath.Join(base, "cache", "download")
	//nolint:gosec // G703: dir derives from the user's own GOMODCACHE/GOPATH, not untrusted input
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// applyInterpOverrides drops the bridges named in MVM_INTERP (comma/space list)
// so those packages interpret from the mirror, for validating interpretation on
// a native build.
func applyInterpOverrides(i *interp.Interp) {
	if os.Getenv("MVM_NATIVE_TABLE") == "off" {
		vm.SetNativeMethodTables(false) // kill switch for ADR-023 native method tables
	}
	v := os.Getenv("MVM_INTERP")
	if v == "" {
		return
	}
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			i.SkipBridges(p)
		}
	}
}

func wireFS(i *interp.Interp) *modfs.FS {
	mfs := buildModFS()
	if err := mfs.Inject(stdmod.ModulePath, stdmod.Version, stdlib.EmbeddedStd()); err != nil {
		panic("modfs inject embedded std: " + err.Error())
	}
	// On wasm net/http is interpreted and pulls golang.org/x/net + x/text from
	// source; there is no module cache or proxy, so inject the embedded subset.
	// Native resolves the full modules from the cache/proxy, so injecting a
	// trimmed copy there would shadow their other packages -- skip it.
	if runtime.GOARCH == "wasm" {
		for _, v := range stdlib.EmbeddedVendor() {
			if err := mfs.Inject(v.Path, v.Version, v.Zip); err != nil {
				panic("modfs inject " + v.Path + ": " + err.Error())
			}
		}
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
			cs := mfs.CacheStats()
			via := ""
			if cs.ReadThroughHits > 0 {
				via = fmt.Sprintf(" (%d via go cache)", cs.ReadThroughHits)
			}
			out += fmt.Sprintf("  cache:    %d hits%s, %s served, %d stored\n",
				cs.ZipHits, via, humanBytes(cs.ZipBytes), cs.ZipWrites)
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
	// MVM_POOLSTATS telemetry: dump per-pool slot high-water vs capacity, for
	// right-sizing the stub pools (each slot is one generated function).
	if os.Getenv("MVM_POOLSTATS") != "" {
		fmt.Fprint(os.Stderr, poolStatsReport())
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
