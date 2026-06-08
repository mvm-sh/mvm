// Command compat generates the mvm compatibility-matrix data.
//
// It runs "mvm test <pkg>" for every bridged standard-library package (parsed
// from stdlib/gen.go) plus the curated external list (compat/external.txt),
// classifies each result into a tier (green/yellow/red/gray) with a
// tests-passing ratio, and writes three data files consumed by the
// mvm.sh/compat page: compat.json (the full latest matrix), history.jsonl (one
// appended summary line per run, for the trend chart) and badge.json (a
// shields.io endpoint badge). It also rewrites the compatibility summary block
// in README.md.
//
// This is plain Go on purpose. Run it through mvm to dogfood the interpreter:
//
//	mvm run compat/gen.go -mvm ./mvm
//
// or natively as a zero-risk fallback (the file is identical either way):
//
//	go run ./compat -mvm ./mvm
//
// Inputs are read relative to -root (default "."), outputs written to -out
// (default "compat"). See README.md and docs/usage.md for the wider design.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// schemaVersion is bumped when the compat.json shape changes incompatibly so
// the website renderer can refuse data it does not understand.
const schemaVersion = 1

// extraStdlib lists packages that are part of mvm's stdlib coverage but are not
// emitted as "-stdlib" directives in stdlib/gen.go because they are bound as
// interpreted source rather than reflect bindings (generic APIs, or log, which
// is interpreted so runtime.Caller reports the user's call site). They still
// have runnable tests, so the matrix includes them.
var extraStdlib = []string{"cmp", "iter", "maps", "slices", "log"}

// pkgRef is one package to test, tagged with its matrix category.
type pkgRef struct {
	path     string
	category string // "stdlib" or "external"
}

// Pkg is the per-package result recorded in compat.json.
type Pkg struct {
	Path         string `json:"path"`
	Category     string `json:"category"`
	Tier         string `json:"tier"` // green, yellow, red, gray
	Pass         int    `json:"pass"`
	Fail         int    `json:"fail"`
	Total        int    `json:"total"`
	DurationMs   int64  `json:"durationMs"`
	ErrorClass   string `json:"errorClass,omitempty"`   // compile, panic, timeout, tests-fail
	ErrorExcerpt string `json:"errorExcerpt,omitempty"` // first error line, truncated
}

// Summary aggregates one category's tier counts and test totals.
type Summary struct {
	Green      int `json:"green"`
	Yellow     int `json:"yellow"`
	Red        int `json:"red"`
	Gray       int `json:"gray"`
	Total      int `json:"total"`
	TestsPass  int `json:"testsPass"`
	TestsTotal int `json:"testsTotal"`
}

// Matrix is the full compat.json document.
type Matrix struct {
	Schema      int                `json:"schema"`
	GeneratedAt string             `json:"generatedAt"`
	Mvm         string             `json:"mvm"`
	Go          string             `json:"go"`
	Platform    string             `json:"platform"`
	Summary     map[string]Summary `json:"summary"`
	Packages    []Pkg              `json:"packages"`
}

// historyEntry is one compact line appended to history.jsonl per run.
type historyEntry struct {
	GeneratedAt string             `json:"generatedAt"`
	Mvm         string             `json:"mvm"`
	Summary     map[string]Summary `json:"summary"`
}

// badge is the shields.io endpoint schema written to badge.json.
type badge struct {
	SchemaVersion int    `json:"schemaVersion"`
	Label         string `json:"label"`
	Message       string `json:"message"`
	Color         string `json:"color"`
}

var (
	rePass    = regexp.MustCompile(`(?m)^\s*--- PASS: `)
	reFail    = regexp.MustCompile(`(?m)^\s*--- FAIL: `)
	rePanic   = regexp.MustCompile(`panic:|vm: panic|PanicError`)
	reUnsup   = regexp.MustCompile(`unsupported \(generic-only`)
	reUntest  = regexp.MustCompile(`untestable \(`)
	reStdlib  = regexp.MustCompile(`(?m)^//go:generate go run \.\./cmd/extract -stdlib (\S+)`)
	reErrLine = regexp.MustCompile(`(?m)^.*(?:\.go:\d+:\d+:|loading "|panic:).*$`)
	reReadme  = regexp.MustCompile(`(?s)<!-- compat:start -->.*?<!-- compat:end -->`)
)

func main() {
	var (
		mvmBin  = flag.String("mvm", "mvm", "path to the mvm binary used to run `mvm test`")
		root    = flag.String("root", ".", "repository root (where stdlib/gen.go and README.md live)")
		out     = flag.String("out", "compat", "output directory for the data files")
		timeout = flag.Duration("timeout", 120*time.Second, "per-package test timeout")
		workers = flag.Int("p", 4, "number of packages to test in parallel")
		only    = flag.String("only", "all", "which set to test: stdlib, external, or all")
		match   = flag.String("match", "", "only test packages whose import path matches this regexp")
		readme  = flag.Bool("readme", true, "rewrite the compat block in README.md")
	)
	flag.Parse()

	refs, err := collectPackages(*root, *only)
	if err != nil {
		fail(err)
	}
	if *match != "" {
		re, err := regexp.Compile(*match)
		if err != nil {
			fail(fmt.Errorf("bad -match: %w", err))
		}
		kept := refs[:0]
		for _, r := range refs {
			if re.MatchString(r.path) {
				kept = append(kept, r)
			}
		}
		refs = kept
	}
	if len(refs) == 0 {
		fail(fmt.Errorf("no packages selected (only=%q)", *only))
	}

	results := runAll(refs, *mvmBin, *timeout, *workers)
	sort.Slice(results, func(i, j int) bool {
		if results[i].Category != results[j].Category {
			return results[i].Category < results[j].Category
		}
		return results[i].Path < results[j].Path
	})

	mvmVer, goVer, platform := mvmVersion(*mvmBin)
	matrix := Matrix{
		Schema:      schemaVersion,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Mvm:         mvmVer,
		Go:          goVer,
		Platform:    platform,
		Summary:     summarize(results),
		Packages:    results,
	}

	if err := writeOutputs(*out, matrix); err != nil {
		fail(err)
	}
	if *readme {
		if err := updateReadme(filepath.Join(*root, "README.md"), matrix); err != nil {
			fmt.Fprintf(os.Stderr, "compat: README not updated: %v\n", err)
		}
	}
	printSummary(matrix.Summary)
}

// collectPackages builds the package list from stdlib/gen.go and
// compat/external.txt, filtered by the -only selector.
func collectPackages(root, only string) ([]pkgRef, error) {
	var refs []pkgRef
	if only == "all" || only == "stdlib" {
		std, err := stdlibPackages(root)
		if err != nil {
			return nil, err
		}
		for _, p := range std {
			refs = append(refs, pkgRef{path: p, category: "stdlib"})
		}
	}
	if only == "all" || only == "external" {
		ext, err := externalPackages(filepath.Join(root, "compat", "external.txt"))
		if err != nil {
			return nil, err
		}
		for _, p := range ext {
			refs = append(refs, pkgRef{path: p, category: "external"})
		}
	}
	return refs, nil
}

// stdlibPackages returns the bridged stdlib import paths, parsed from the
// -stdlib directives in stdlib/gen.go plus the interpreted-source extras.
func stdlibPackages(root string) ([]string, error) {
	buf, err := os.ReadFile(filepath.Join(root, "stdlib", "gen.go"))
	if err != nil {
		return nil, fmt.Errorf("reading stdlib/gen.go: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, m := range reStdlib.FindAllStringSubmatch(string(buf), -1) {
		add(m[1])
	}
	for _, p := range extraStdlib {
		add(p)
	}
	sort.Strings(out)
	return out, nil
}

// externalPackages reads compat/external.txt: one import path per line, with
// "#" comments and blank lines ignored. A missing file yields no packages.
func externalPackages(path string) ([]string, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var out []string
	for line := range strings.SplitSeq(string(buf), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// runAll tests every package through a fixed worker pool. Each package runs as
// an isolated subprocess, so a panic, os.Exit or hang in one cannot take the
// generator down. Results are written into a preallocated slice indexed by job,
// so no locking is needed beyond the progress counter.
func runAll(refs []pkgRef, mvmBin string, timeout time.Duration, workers int) []Pkg {
	if workers < 1 {
		workers = 1
	}
	results := make([]Pkg, len(refs))
	jobs := make(chan int)
	var done atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = runOne(refs[i], mvmBin, timeout)
				n := done.Add(1)
				r := results[i]
				fmt.Fprintf(os.Stderr, "[%3d/%d] %-7s %-40s %-6s %d/%d (%dms) %s\n",
					n, len(refs), r.Category, r.Path, r.Tier, r.Pass, r.Total, r.DurationMs, r.ErrorClass)
			}
		}()
	}
	for i := range refs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

// runOne runs "mvm test <pkg> -v" with a timeout and classifies the result.
func runOne(ref pkgRef, mvmBin string, timeout time.Duration) Pkg {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	//nolint:gosec // G204: package paths from our curated lists, not untrusted input
	cmd := exec.CommandContext(ctx, mvmBin, "test", ref.path, "-v")
	cmd.Env = os.Environ()
	output, _ := cmd.CombinedOutput()
	dur := time.Since(start)

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	timedOut := ctx.Err() == context.DeadlineExceeded

	r := classify(exitCode, timedOut, string(output))
	r.Path = ref.path
	r.Category = ref.category
	r.DurationMs = dur.Milliseconds()
	return r
}

// classify turns one "mvm test" run into a tier and ratio. It is pure (no I/O)
// so it can be unit-tested against canned output. pass/fail are counted from
// the per-test "--- PASS:/--- FAIL:" lines emitted with -v (subtests included,
// which only sharpens the ratio).
func classify(exitCode int, timedOut bool, out string) Pkg {
	pass := len(rePass.FindAllStringIndex(out, -1))
	fail := len(reFail.FindAllStringIndex(out, -1))
	total := pass + fail
	r := Pkg{Pass: pass, Fail: fail, Total: total}

	switch {
	case timedOut:
		r.Tier, r.ErrorClass = "red", "timeout"
	case total > 0 && fail == 0:
		r.Tier = "green"
	case total > 0 && pass > 0:
		r.Tier = "yellow"
	case total > 0: // pass == 0
		r.Tier, r.ErrorClass = "red", "tests-fail"
	case reUnsup.MatchString(out):
		// Generic-only stub package: unsupported by design, not a failure.
		r.Tier = "gray"
	case reUntest.MatchString(out):
		// Wholesale-untestable package (stdlib.Untestable): skipped, not failed.
		r.Tier = "gray"
	case exitCode == 0 && strings.Contains(out, "no tests to run"):
		r.Tier = "gray"
	case rePanic.MatchString(out):
		r.Tier, r.ErrorClass = "red", "panic"
	default:
		r.Tier, r.ErrorClass = "red", "compile"
	}
	if r.Tier == "red" && r.ErrorClass != "tests-fail" {
		r.ErrorExcerpt = firstErrorLine(out)
	}
	return r
}

// firstErrorLine returns the first line that looks like a compiler/loader error
// or a panic, truncated, for a compact excerpt in the matrix.
func firstErrorLine(out string) string {
	line := reErrLine.FindString(out)
	if line == "" {
		// Fall back to the first non-empty line.
		for l := range strings.SplitSeq(out, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				line = l
				break
			}
		}
	}
	line = strings.TrimSpace(line)
	const maxLen = 200
	if len(line) > maxLen {
		line = line[:maxLen] + "..."
	}
	return line
}

// summarize aggregates results into per-category tier counts and test totals.
func summarize(pkgs []Pkg) map[string]Summary {
	m := map[string]Summary{}
	for _, p := range pkgs {
		s := m[p.Category]
		s.Total++
		s.TestsPass += p.Pass
		s.TestsTotal += p.Total
		switch p.Tier {
		case "green":
			s.Green++
		case "yellow":
			s.Yellow++
		case "red":
			s.Red++
		case "gray":
			s.Gray++
		}
		m[p.Category] = s
	}
	return m
}

// mvmVersion shells out to "mvm version" and splits its
// "<rev> <goversion> <os>/<arch>" line. Missing fields degrade to "unknown".
func mvmVersion(mvmBin string) (rev, goVer, platform string) {
	rev, goVer, platform = "unknown", "unknown", "unknown"
	out, err := exec.Command(mvmBin, "version").Output()
	if err != nil {
		return rev, goVer, platform
	}
	f := strings.Fields(string(out))
	if len(f) > 0 {
		rev = f[0]
	}
	if len(f) > 1 {
		goVer = f[1]
	}
	if len(f) > 2 {
		platform = f[2]
	}
	return rev, goVer, platform
}

// writeOutputs writes compat.json, appends history.jsonl and writes badge.json.
func writeOutputs(dir string, m Matrix) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "compat.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}

	hist, err := json.Marshal(historyEntry{GeneratedAt: m.GeneratedAt, Mvm: m.Mvm, Summary: m.Summary})
	if err != nil {
		return err
	}
	if err := appendLine(filepath.Join(dir, "history.jsonl"), hist); err != nil {
		return err
	}

	b, err := json.MarshalIndent(makeBadge(m.Summary), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "badge.json"), append(b, '\n'), 0o600)
}

// appendLine appends one JSON line to path, creating it if missing.
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Close()
}

// makeBadge builds the shields.io endpoint badge from the green/total ratios.
func makeBadge(sum map[string]Summary) badge {
	std, ext := sum["stdlib"], sum["external"]
	msg := fmt.Sprintf("stdlib %d/%d, ext %d/%d", std.Green, std.Total, ext.Green, ext.Total)
	greens := std.Green + ext.Green
	totals := std.Total + ext.Total
	return badge{SchemaVersion: 1, Label: "go compat", Message: msg, Color: ratioColor(greens, totals)}
}

// ratioColor maps a green/total ratio to a shields color name.
func ratioColor(green, total int) string {
	if total == 0 {
		return "lightgrey"
	}
	switch r := float64(green) / float64(total); {
	case r >= 0.8:
		return "brightgreen"
	case r >= 0.6:
		return "green"
	case r >= 0.4:
		return "yellow"
	case r >= 0.2:
		return "orange"
	default:
		return "red"
	}
}

// updateReadme rewrites the text between the compat:start / compat:end markers
// in README.md. A missing marker pair is reported but not fatal.
func updateReadme(path string, m Matrix) error {
	buf, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !reReadme.Match(buf) {
		return errors.New("markers <!-- compat:start --> / <!-- compat:end --> not found")
	}
	std, ext := m.Summary["stdlib"], m.Summary["external"]
	date := m.GeneratedAt
	if len(date) >= 10 {
		date = date[:10]
	}
	block := fmt.Sprintf("<!-- compat:start -->\n"+
		"Stdlib: %d/%d packages fully pass; external: %d/%d fully pass (as of %s).\n"+
		"See the full matrix at https://mvm.sh/compat.\n"+
		"<!-- compat:end -->",
		std.Green, std.Total, ext.Green, ext.Total, date)
	updated := reReadme.ReplaceAllLiteralString(string(buf), block)
	//nolint:gosec // G703: path is the operator-supplied -root/README.md, not untrusted input
	return os.WriteFile(path, []byte(updated), 0o600)
}

// printSummary writes a short human summary of the run to stderr.
func printSummary(sum map[string]Summary) {
	for _, cat := range []string{"stdlib", "external"} {
		s, ok := sum[cat]
		if !ok {
			continue
		}
		fmt.Fprintf(os.Stderr, "%-8s green=%d yellow=%d red=%d gray=%d total=%d tests=%d/%d\n",
			cat, s.Green, s.Yellow, s.Red, s.Gray, s.Total, s.TestsPass, s.TestsTotal)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "compat:", err)
	os.Exit(1)
}
