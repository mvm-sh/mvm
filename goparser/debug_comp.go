package goparser

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

// DebugComp enables MVM_DEBUG_COMP compile-phase tracing: one line per package
// as it finishes parsing, plus an execution-start marker (emitted by interp).
var DebugComp = os.Getenv("MVM_DEBUG_COMP") != ""

// progStart approximates process start; package-var init runs before main.
var progStart = time.Now()

// Elapsed returns time since program start, for MVM_DEBUG_COMP stamps.
func Elapsed() time.Duration { return time.Since(progStart) }

// traceCompPkg prints a per-package compile-phase trace line: the package's own
// line count and the cumulative parse totals so far. Code/data stats do not yet
// exist (Phase 2 code-gen runs once for the whole unit); interp prints those at
// execution start.
func (p *Parser) traceCompPkg(label string, ownLines int) {
	cumSrcs, cumLines := 0, 0
	for i := range p.Sources {
		if path.Base(p.Sources[i].Name) == "<shim>" {
			continue
		}
		cumSrcs++
		cumLines += p.Sources[i].Lines()
	}
	fmt.Fprintf(os.Stderr, "[comp] %-44s %6d lines  +%-12s  cum: %d srcs / %d lines / %d syms\n",
		label, ownLines, Elapsed().Round(time.Microsecond).String(), cumSrcs, cumLines, len(p.Symbols))
}

// ownLines sums the lines of sources belonging to package name (target file or
// "name/<file>" imported sources), excluding generic-template shims.
func (p *Parser) ownLines(name string) int {
	prefix := name + "/"
	n := 0
	for i := range p.Sources {
		s := &p.Sources[i]
		if path.Base(s.Name) == "<shim>" {
			continue
		}
		if s.Name == name || strings.HasPrefix(s.Name, prefix) {
			n += s.Lines()
		}
	}
	return n
}

// lineCount counts lines in raw content, matching scan.Source.Lines semantics.
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
