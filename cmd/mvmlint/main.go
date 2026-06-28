// Command mvmlint runs mvm's project-specific source checks over mvm's own Go
// source, using mvm's own scanner (github.com/mvm-sh/mvm/scan) rather than the
// Go AST or go/analysis. This keeps the tool dependency-free (no x/tools) and
// dogfoods the scanner on the whole repository as a side effect.
//
// Usage:
//
//	go run ./cmd/mvmlint [dir ...]   # default: the whole module
//
// Checks:
//
//	symkey  - writes to a .Symbols (symbol.SymMap) table must use a key that is
//	          pkg-qualified (pkgKey/QualifyName/...), lexically scoped (a "/"
//	          concat), a predeclared/builtin name, or forwarded from an enclosing
//	          parameter. Bare keys are the root of the cross-package symbol
//	          collision class. Suppress an intentional bare key with a trailing
//	          // mvm:symkey-ok comment.
//	posbase - PosBase is added by Compiler.emit; re-adding it elsewhere
//	          double-applies the position base. Suppress with // mvm:posbase-ok.
//
// The checks are text/token heuristics, not a type-checked analysis: key
// classification resolves locals through best-effort reaching definitions that
// are not block-scoped, so a safe definition in one branch can clear a
// same-named bare key in a sibling branch (a possible false negative). This is
// acceptable for a dogfooding guard whose green output is sanity, not proof.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/scan"
)

var sc = scan.NewScanner(golang.GoSpec)

// qualifiers are the helper functions whose call yields a qualified or scoped
// key (canonical list: goparser.QualifyName plus the helpers in
// goparser/scope.go). A call to any of them marks a key safe.
var qualifiers = []string{
	"pkgKey", "QualifyName", "scopedName", "labelName", "caseLabel",
	"caseBodyLabel", "mangledName", "qualifyLabel", "resolveLabel",
	"canonicalTypeKey",
}

// predeclared are bare names that are legitimately unqualified everywhere.
// Keep in sync with the universe scope registered by symbol.SymMap.Init.
var predeclared = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true, "int": true, "int8": true,
	"int16": true, "int32": true, "int64": true, "rune": true, "string": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "any": true, "comparable": true, "true": true,
	"false": true, "iota": true, "nil": true, "append": true, "cap": true,
	"clear": true, "close": true, "complex": true, "copy": true, "delete": true,
	"imag": true, "len": true, "make": true, "max": true, "min": true,
	"new": true, "panic": true, "print": true, "println": true, "real": true,
	"recover": true, "trap": true,
}

var (
	ident      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sepLiteral = regexp.MustCompile(`"[^"]*[/.][^"]*"`) // a string literal holding "/" or "."
)

type finding struct {
	pos     int // absolute byte offset in the file
	check   string
	message string
}

// fileLinter analyses one file's source.
type fileLinter struct {
	src      string
	findings []finding
}

// fctx is the lexical context threaded through the walk: identifiers in scope
// from enclosing signatures, best-effort reaching definitions for locals, and
// whether we are inside Compiler.emit (where adding PosBase is correct).
type fctx struct {
	params map[string]bool
	defs   map[string][]string
	inEmit bool
}

func newCtx() fctx {
	return fctx{params: map[string]bool{}, defs: map[string][]string{}}
}

func (c fctx) child() fctx {
	n := newCtx()
	n.inEmit = c.inEmit
	for k := range c.params {
		n.params[k] = true
	}
	for k, v := range c.defs {
		n.defs[k] = append([]string(nil), v...)
	}
	return n
}

func scanToks(src string) []scan.Token {
	toks, _ := sc.Scan(src, true)
	return toks
}

// collectDefs records simple `name := rhs` / `name = rhs` reaching definitions
// found directly in toks, so a bare-ident key can be resolved to the expression
// it was built from. Multi-assignment forms (`a, b := f()`) are not recorded.
func collectDefs(toks []scan.Token, c fctx) {
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].Tok != lang.Ident {
			continue
		}
		if toks[i+1].Tok != lang.Define && toks[i+1].Tok != lang.Assign {
			continue
		}
		var rhs []string
		for j := i + 2; j < len(toks) && toks[j].Tok != lang.Semicolon; j++ {
			rhs = append(rhs, toks[j].Str)
		}
		name := toks[i].Str
		c.defs[name] = append(c.defs[name], strings.Join(rhs, " "))
	}
}

// collectDefsDeep gathers reaching definitions over an entire function body
// (recursing through nested blocks) so a write is judged against every
// assignment to a key local, not just those seen earlier in source order.
func collectDefsDeep(toks []scan.Token, c fctx) {
	collectDefs(toks, c)
	for _, t := range toks {
		if t.Tok.IsBlock() {
			collectDefsDeep(scanToks(t.Block()), c)
		}
	}
}

// isIdentByte reports whether b can appear in a Go identifier.
func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// mentionsCall reports whether name appears in s as a call (`name(`) with name
// at an identifier boundary, so `pkgKey(` matches but `mypkgKey(` does not.
func mentionsCall(s, name string) bool {
	needle := name + "("
	for off := 0; ; {
		k := strings.Index(s[off:], needle)
		if k < 0 {
			return false
		}
		p := off + k
		if p == 0 || !isIdentByte(s[p-1]) {
			return true
		}
		off = p + 1
	}
}

// mentionsWord reports whether w appears in s as a whole identifier token, so
// `PkgPath` matches `s.PkgPath` but not `PkgPathological`.
func mentionsWord(s, w string) bool {
	for off := 0; ; {
		k := strings.Index(s[off:], w)
		if k < 0 {
			return false
		}
		p := off + k
		end := p + len(w)
		atStart := p == 0 || !isIdentByte(s[p-1])
		atEnd := end >= len(s) || !isIdentByte(s[end])
		if atStart && atEnd {
			return true
		}
		off = p + 1
	}
}

// safeText reports whether a key expression source is acceptable.
func (c fctx) safeText(src string, seen map[string]bool) bool {
	src = strings.TrimSpace(src)
	if src == "" {
		return false
	}
	// string literal: "int", "pkg.Name", `raw`, etc.
	if src[0] == '"' || src[0] == '`' {
		if s, err := strconv.Unquote(src); err == nil {
			return predeclared[s] || strings.ContainsAny(s, "/.#")
		}
	}
	// Token rejoining inserts spaces ("p . pkgKey ( x )"); compact for the
	// boundary-aware substring tests below.
	compact := strings.ReplaceAll(src, " ", "")
	for _, q := range qualifiers {
		if mentionsCall(compact, q) {
			return true
		}
	}
	// hand-built qualified key via a package path, e.g. `s.PkgPath + "." + name`
	if mentionsWord(compact, "PkgPath") {
		return true
	}
	// concatenation containing a string literal that holds a "/" (scope) or
	// "." (pkg) separator, e.g. `p.scope + "/_ts"` or `pkgPath + "." + name`.
	if strings.Contains(compact, "+") && sepLiteral.MatchString(src) {
		return true
	}
	// bare identifier: a parameter (caller's responsibility), a builtin, or a
	// local whose reaching definition resolves safe.
	if ident.MatchString(src) {
		if c.params[src] || predeclared[src] {
			return true
		}
		if rhss, ok := c.defs[src]; ok && !seen[src] {
			seen[src] = true
			for _, rhs := range rhss {
				if c.safeText(rhs, seen) {
					return true
				}
			}
		}
	}
	return false
}

// identsIn returns the identifier names appearing in a block's source, used to
// loosely collect signature parameter names (both names and type names).
func identsIn(block string) []string {
	var out []string
	for _, t := range scanToks(block) {
		if t.Tok == lang.Ident {
			out = append(out, t.Str)
		}
	}
	return out
}

// splitArgs splits a ParenBlock body into top-level comma-separated arg sources.
func splitArgs(block string) []string {
	var args []string
	var cur []string
	for _, t := range scanToks(block) {
		if t.Tok == lang.Comma {
			args = append(args, strings.Join(cur, " "))
			cur = nil
			continue
		}
		cur = append(cur, t.Str)
	}
	if len(cur) > 0 {
		args = append(args, strings.Join(cur, " "))
	}
	return args
}

func (fl *fileLinter) report(pos int, check, msg string) {
	fl.findings = append(fl.findings, finding{pos, check, msg})
}

// walk scans src (a block body starting at absolute offset base) and recurses.
// Reaching definitions are pre-collected per function body in the Func handler,
// so walk only traverses and checks.
func (fl *fileLinter) walk(src string, base int, c fctx) {
	toks := scanToks(src)
	for i := 0; i < len(toks); i++ {
		t := toks[i]

		// --- function header: capture name + params for the body block ---
		if t.Tok == lang.Func {
			child := c.child()
			j := i + 1
			// optional receiver: `(recv T)` immediately followed by the name
			if j+1 < len(toks) && toks[j].Tok == lang.ParenBlock && toks[j+1].Tok == lang.Ident {
				j++
			}
			if j < len(toks) && toks[j].Tok == lang.Ident {
				child.inEmit = toks[j].Str == "emit"
				j++
			}
			for ; j < len(toks); j++ {
				if toks[j].Tok == lang.ParenBlock {
					for _, n := range identsIn(toks[j].Block()) {
						child.params[n] = true
					}
				}
				if toks[j].Tok == lang.BraceBlock {
					body := toks[j].Block()
					collectDefsDeep(scanToks(body), child)
					fl.walk(body, base+toks[j].Pos+toks[j].Beg, child)
					i = j
					break
				}
				if toks[j].Tok == lang.Semicolon {
					break // forward declaration / func type, no body here
				}
			}
			continue
		}

		// --- symkey: direct write  X.Symbols[key] = ... ---
		if t.Tok == lang.Ident && t.Str == "Symbols" && i > 0 && toks[i-1].Tok == lang.Period &&
			i+2 < len(toks) && toks[i+1].Tok == lang.BracketBlock && toks[i+2].Tok == lang.Assign {
			key := toks[i+1].Block()
			if !c.safeText(key, map[string]bool{}) {
				fl.report(base+t.Pos, "symkey",
					"symbol-table key `"+strings.TrimSpace(key)+"` is not pkg-qualified or scoped")
			}
		}

		// --- symkey: helper calls  SymSet(key, ...) / SymAdd(_, key, ...) ---
		if t.Tok == lang.Ident && (t.Str == "SymSet" || t.Str == "SymAdd") &&
			i+1 < len(toks) && toks[i+1].Tok == lang.ParenBlock {
			args := splitArgs(toks[i+1].Block())
			idx := 0
			if t.Str == "SymAdd" {
				idx = 1
			}
			if idx < len(args) && !c.safeText(args[idx], map[string]bool{}) {
				fl.report(base+t.Pos, "symkey",
					"symbol-table key `"+strings.TrimSpace(args[idx])+"` (via "+t.Str+") is not pkg-qualified or scoped")
			}
		}

		// --- posbase: a `.PosBase` that is an operand of + or += outside emit ---
		if t.Tok == lang.Ident && t.Str == "PosBase" && i > 0 && toks[i-1].Tok == lang.Period &&
			!c.inEmit && additiveContext(toks, i) {
			fl.report(base+t.Pos, "posbase",
				"PosBase is added by emit(); re-adding it double-applies the position base")
		}

		if t.Tok.IsBlock() {
			fl.walk(t.Block(), base+t.Pos+t.Beg, c)
		}
	}
}

// additiveContext reports whether the selector chain `X.Y.PosBase` ending at
// toks[i] is an operand of a `+` or `+=` expression. It walks back over the
// member-access chain to its root and inspects the operators immediately
// bounding it, so it is not fooled by an unrelated `+` a few tokens away.
func additiveContext(toks []scan.Token, i int) bool {
	start := i
	for start-2 >= 0 && toks[start-1].Tok == lang.Period {
		start -= 2
	}
	if start-1 >= 0 {
		if op := toks[start-1].Tok; op == lang.Add || op == lang.AddAssign {
			return true
		}
	}
	if i+1 < len(toks) {
		if op := toks[i+1].Tok; op == lang.Add || op == lang.AddAssign {
			return true
		}
	}
	return false
}

// lineAt returns 1-based line:col for a byte offset and the full line text.
func lineAt(src string, pos int) (int, int, string) {
	if pos > len(src) {
		pos = len(src)
	}
	line, col, start := 1, 1, 0
	for i := 0; i < pos; i++ {
		if src[i] == '\n' {
			line++
			col = 1
			start = i + 1
		} else {
			col++
		}
	}
	end := strings.IndexByte(src[start:], '\n')
	if end < 0 {
		end = len(src) - start
	}
	return line, col, src[start : start+end]
}

// results returns the formatted, directive-filtered findings for file, ordered
// by source position.
func (fl *fileLinter) results(file string) []string {
	sort.Slice(fl.findings, func(i, j int) bool { return fl.findings[i].pos < fl.findings[j].pos })
	var out []string
	for _, fd := range fl.findings {
		line, col, text := lineAt(fl.src, fd.pos)
		if strings.Contains(text, "mvm:"+fd.check+"-ok") {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d:%d: %s (%s)", file, line, col, fd.message, fd.check))
	}
	return out
}

func skipDir(p string) bool {
	switch filepath.Base(p) {
	case ".git", ".claude", "stdlib", "_samples", "testdata":
		return true
	}
	return false
}

func main() {
	dirs := os.Args[1:]
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	var files []string
	for _, d := range dirs {
		//nolint:gosec // dev tool: walks repo-local directories given on argv
		_ = filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				if err == nil && info.IsDir() && skipDir(p) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
	}

	total := 0
	for _, f := range files {
		b, err := os.ReadFile(f) // dev tool linting repo-local source paths
		if err != nil {
			continue
		}
		fl := &fileLinter{src: string(b)}
		fl.walk(fl.src, 0, newCtx())
		for _, line := range fl.results(f) {
			fmt.Println(line)
			total++
		}
	}
	if total > 0 {
		fmt.Fprintf(os.Stderr, "\nmvmlint: %d finding(s)\n", total)
		os.Exit(1)
	}
}
