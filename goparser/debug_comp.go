package goparser

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

// DebugComp enables MVM_DEBUG_COMP compile-phase tracing.
var DebugComp = os.Getenv("MVM_DEBUG_COMP") != ""

// progStart approximates process start: package-var init runs before main.
var progStart = time.Now()

// Elapsed returns time since program start, for MVM_DEBUG_COMP stamps.
func Elapsed() time.Duration { return time.Since(progStart) }

// isShim reports whether a source is a generic-template shim (scaffolding).
func isShim(name string) bool { return path.Base(name) == "<shim>" }

// traceCompPkg prints one compile-phase trace line: the package's own line count
// and cumulative parse totals. Code/data stats don't exist until codegen; interp
// prints those at execution start.
func (p *Parser) traceCompPkg(label string, ownLines int) {
	cumSrcs, cumLines := 0, 0
	for i := range p.Sources {
		if isShim(p.Sources[i].Name) {
			continue
		}
		cumSrcs++
		cumLines += p.Sources[i].Lines()
	}
	fmt.Fprintf(os.Stderr, "[comp] %-44s %6d lines  +%-12s  cum: %d srcs / %d lines / %d syms\n",
		label, ownLines, Elapsed().Round(time.Microsecond).String(), cumSrcs, cumLines, len(p.Symbols))
}

// ownLines sums the lines of sources belonging to package name (target file or
// "name/<file>" imports), skipping shims.
func (p *Parser) ownLines(name string) int {
	prefix := name + "/"
	n := 0
	for i := range p.Sources {
		s := &p.Sources[i]
		if isShim(s.Name) {
			continue
		}
		if s.Name == name || strings.HasPrefix(s.Name, prefix) {
			n += s.Lines()
		}
	}
	return n
}
