// Package goparser implements a structured parser for Go.
package goparser

import (
	"errors"
	"fmt"
	"go/constant"
	"io/fs"
	"os"
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/scan"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// Parser represents the state of a parser.
type Parser struct {
	*scan.Scanner

	Symbols         symbol.SymMap
	Packages        map[string]*symbol.Package
	function        *symbol.Symbol  // current function
	scope           string          // current scope
	fname           string          // current function name
	pkgName         string          // current package name
	noPkg           bool            // true if package statement is not mandatory (test, repl).
	pkgfs           fs.FS           // filesystem to read imported sources from
	stdlibfs        fs.FS           // fallback filesystem for embedded stdlib sources
	remotefs        fs.FS           // last-resort filesystem (e.g. network module proxy)
	testSrcFS       fs.FS           // test-only fs for bridged-stdlib *_test.go sources ($GOROOT/src); never consulted by ordinary import resolution
	testSkipFiles   map[string]bool // basenames of bridged-stdlib test files to skip (drop-on-compile-error retry); see SetTestSkipFiles
	includeTests    bool            // include _test.go files when loading package sources
	importRemaining []DeferredDecl  // code-gen declarations from imported source packages, tagged with their origin package
	CompilingPkg    string          // while a deferred decl is being parsed/compiled in Phase 2: its origin package's import path ("" = main/REPL); makes unqualified type/name lookups prefer that package's symbols (see symGet, comp.Compiler.symAt)
	importingPkg    string          // while parseSrc is running for an imported package: its full import path; "" outside any import. Used by pkgKey to qualify top-level Type/Func/Method/Generic symbol keys at definition time (Path B); also probed as a fallback in symGet for Phase-1 lookups.

	funcScope         string
	framelen          map[string]int // length of function frames indexed by funcScope
	labelCount        map[string]int
	breakLabel        string
	continueLabel     string
	pendingLabel      string               // user label preceding the current for/switch statement
	labeledJump       map[string][2]string // maps user label to [continueLabel, breakLabel]
	clonum            int                  // closure instance number, package-global counter
	funcN             int                  // anonymous-function counter within the current outer function
	initNum           int                  // init function instance counter
	InitFuncs         []string             // ordered list of init function internal names
	blankSeq          int                  // counter for unique blank identifier names
	namedOut          []string             // scoped names of named return vars for current function
	symTracker        []string             // accumulates newly-added symbol keys during a checkpoint window; nil = not tracking
	batchFuncDecls    map[string]bool      // canonical keys of top-level funcs/methods registered in the current resolveDecls batch; a second hit is a redeclaration (saved/restored across nested imports)
	pendingMethodDefs Tokens               // generic method+func instance defs, drained into output at statement end (survives inference's discarded parseExpr buffers)
	typeOnly          bool                 // when true, addSymVar is a no-op (Phase 1 signature-only parse)
	inForInit         bool                 // true while parsing for-init or range clause (marks LoopVar)
	funcDepth         int                  // nesting depth of function bodies (>0 means inside a function)
	loopDepth         int                  // nesting depth of for loops (>0 means inside a loop)
	instDepth         int                  // nesting depth of generic instantiations; guards unbounded-growth recursion (instantiation cycle)
	buildCtx          *buildContext        // build constraint context for file filtering
}

// SymSet inserts sym at key in the symbol table, recording the key for potential rollback.
func (p *Parser) SymSet(key string, sym *symbol.Symbol) {
	if p.symTracker != nil {
		p.symTracker = append(p.symTracker, key)
	}
	p.Symbols[key] = sym
}

// SymAdd adds a new named symbol, recording the key for potential rollback.
func (p *Parser) SymAdd(i int, name string, v vm.Value, k symbol.Kind, t *vm.Type) {
	name = strings.TrimPrefix(name, "/")
	if p.symTracker != nil {
		p.symTracker = append(p.symTracker, name)
	}
	p.Symbols[name] = &symbol.Symbol{Kind: k, Name: name, Index: i, Value: v, Type: t}
}

// symGet resolves an unqualified name like Symbols.Get(name, p.scope), except
// that -- while a deferred declaration is being parsed in Phase 2 (CompilingPkg
// set) -- a top-level name resolves to that package's symbol (key
// "CompilingPkg.name") rather than to a bare key that a sibling import may have
// left pointing at a different package's same-named symbol. A lexical local
// (returned scope != "") still shadows it, matching Go scoping.
func (p *Parser) symGet(name string) (*symbol.Symbol, string, bool) {
	s, sc, ok := p.Symbols.Get(name, p.scope)
	if ok && sc != "" {
		return s, sc, true
	}
	// Phase 2 deferred parsing: CompilingPkg is set; Phase 1 of an imported pkg:
	// importingPkg is set. Never both at once. Probe each in turn so a top-level
	// name resolves to this pkg's canonical Symbol rather than a bare key that
	// might be shadowed by a sibling import (Pkg/Type/Func/Var/Const all keyed
	// at "<pkg>.<name>" for imported pkgs by their respective writers). For
	// pointer-method names ("*Tag.M"), the canonical form has '*' at the very
	// front of the key ("*<pkg>.Tag.M"); pkgKey writes that shape and we mirror
	// it here.
	if p.CompilingPkg != "" {
		if qs, qok := p.Symbols[QualifyName(p.CompilingPkg, name)]; qok {
			return qs, "", true
		}
	}
	if p.importingPkg != "" {
		if qs, qok := p.Symbols[QualifyName(p.importingPkg, name)]; qok {
			return qs, "", true
		}
	}
	return s, sc, ok
}

// QualifyName composes the canonical pkg-qualified symbol-table key for a
// top-level name. For pointer-receiver method names ("*Tag.M"), the '*' moves
// to the very front of the key ("*<pkg>.Tag.M") so the standard pointer-
// method composition `"*"+typeKey+"."+method` still produces the same key.
// Exported so the comp package (qualifyLabel) shares the exact composition.
func QualifyName(pkg, name string) string {
	if strings.HasPrefix(name, "*") {
		return "*" + pkg + "." + name[1:]
	}
	return pkg + "." + name
}

// ImportPackageConsts attaches high-precision constant values
// to already-imported packages, so the compiler can fold
// constant expressions involving bridged floats at full precision.
// Call it after ImportPackageValues.
func (p *Parser) ImportPackageConsts(m map[string]map[string]string) {
	for pkg, consts := range m {
		bp, ok := p.Packages[pkg]
		if !ok {
			continue
		}
		if bp.Cvals == nil {
			bp.Cvals = make(map[string]constant.Value, len(consts))
		}
		for name, exact := range consts {
			if cv := ConstFromExact(exact); cv != nil {
				bp.Cvals[name] = cv
			}
		}
	}
}

// ImportPackageValues populates packages with values.
func (p *Parser) ImportPackageValues(m map[string]map[string]reflect.Value) {
	for k, v := range m {
		p.Packages[k] = symbol.BinPkg(v, k)
	}
	// Install registered generic shims now that the target packages exist
	// in p.Packages. Shims add interpreted-source generic templates for
	// natives that cannot be wrapped as a single reflect.ValueOf binding
	// (e.g. reflect.TypeFor). See generic_shim.go for the registry API.
	if err := p.installGenericShims(); err != nil {
		// Shim installation failures are programmer errors (bad shim
		// source); panic so they surface in tests rather than silently
		// leaving the target generic undefined.
		panic(err)
	}
}

// SetPkgfs sets the parser virtual filesystem for reading sources.
func (p *Parser) SetPkgfs(pkgPath string) {
	p.pkgfs = os.DirFS(pkgPath)
}

// SetStdlibFS installs a fallback filesystem for resolving imported source
// packages that are not present in the primary pkgfs. This is used to
// resolve generics-first stdlib packages (cmp, slices, maps, ...) whose
// sources are embedded in the interpreter binary.
func (p *Parser) SetStdlibFS(fsys fs.FS) {
	p.stdlibfs = fsys
}

// SetRemoteFS installs a last-resort filesystem consulted when neither
// pkgfs nor stdlibfs contain the requested import path. Typical use is a
// modfs.FS that fetches modules from a proxy on demand.
func (p *Parser) SetRemoteFS(fsys fs.FS) {
	p.remotefs = fsys
}

// SetIncludeTests toggles whether ParseAll's directory-mode load includes
// _test.go files. Off by default (matching `import "X"` resolution); turn
// on for `mvm test <importpath>` so test functions become callable.
func (p *Parser) SetIncludeTests(b bool) {
	p.includeTests = b
}

// SetTestSourceFS installs the test-source filesystem consulted by
// LoadPackageSources only when (a) includeTests is on and (b) the target
// import path is a bridge-only stdlib package (i.e. has a Bin entry in
// p.Packages but no source in pkgfs/stdlibfs/remotefs). The intended
// supplier is stdlib.GorootTestFS(), which serves $GOROOT/src so external
// `package X_test` files run against the existing reflect bindings.
//
// This FS is deliberately separate from the pkgfs -> stdlibfs -> remotefs
// chain: feeding $GOROOT/src into that chain would make ordinary
// `import "strings"` start loading interpreted source alongside the
// reflect bridge, double-defining every exported symbol.
func (p *Parser) SetTestSourceFS(fsys fs.FS) {
	p.testSrcFS = fsys
}

// SetTestSkipFiles records basenames of bridged-stdlib test files that
// loadBridgedTestSources must skip. Used by `mvm test`'s drop-on-compile-
// error retry: a stdlib external test file that references export_test.go-
// only symbols (e.g. a method the real native type lacks) can't compile
// against the bridge, so the driver drops it and reloads the rest. nil or
// empty means skip nothing.
func (p *Parser) SetTestSkipFiles(names map[string]bool) {
	p.testSkipFiles = names
}

// WithImportingPkg sets p.importingPkg to pkg and returns a function that
// restores the previous value. Callers loading a package's source directly
// (e.g. `mvm test <importpath>`) use this to mirror the canonical-key setup
// that importSrc performs for transitive imports, so the target's top-level
// Type/Func/Method/Var/Const symbols land at `<pkg>.<name>` keys rather than
// bare keys (which would mismatch every subsequent qualified lookup in the
// target's own deferred bodies). See pkgKey, symGet, and the Phase 2 Path B
// memory notes.
func (p *Parser) WithImportingPkg(pkg string) func() {
	saved := p.importingPkg
	p.importingPkg = pkg
	return func() { p.importingPkg = saved }
}

// Parser errors.
var (
	errBody     = errors.New("missing body")
	errBreak    = errors.New("invalid break statement")
	errContinue = errors.New("invalid continue statement")
	errFor      = errors.New("invalid for statement")
	errGoto     = errors.New("invalid goto statement")
)

// NewParser returns a new parser.
func NewParser(spec *lang.Spec, noPkg bool) *Parser {
	p := &Parser{
		Scanner:     scan.NewScanner(spec),
		Symbols:     symbol.SymMap{},
		Packages:    map[string]*symbol.Package{},
		noPkg:       noPkg,
		framelen:    map[string]int{},
		labelCount:  map[string]int{},
		labeledJump: map[string][2]string{},
		buildCtx:    defaultBuildContext(),
	}
	p.Symbols.Init()
	return p
}

func (p *Parser) errAt(tok Token, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if loc := p.Sources.FormatPos(tok.Pos); loc != "" {
		return fmt.Errorf("%s: %s", loc, msg)
	}
	return errors.New(msg)
}

// wrapAt wraps base (preserving it for errors.Is) with the source position of tok and a message.
func (p *Parser) wrapAt(tok Token, base error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	if loc := p.Sources.FormatPos(tok.Pos); loc != "" {
		return fmt.Errorf("%s: %w: %s", loc, base, msg)
	}
	return fmt.Errorf("%w: %s", base, msg)
}

func (p *Parser) scan(s string, endSemi bool) (out Tokens, err error) {
	return p.scanAt(0, s, endSemi)
}

func (p *Parser) scanAt(basePos int, s string, endSemi bool) (out Tokens, err error) {
	toks, err := p.Scan(s, endSemi)
	if err != nil {
		return out, err
	}
	for _, t := range toks {
		t.Pos += basePos
		out = append(out, Token{Token: t})
	}
	return out, err
}

func (p *Parser) scanBlock(bt scan.Token, endSemi bool) (Tokens, error) {
	return p.scanAt(bt.Pos+bt.Beg, bt.Block(), endSemi)
}

func (p *Parser) parseTokBlock(bt scan.Token) (Tokens, error) {
	return p.parseAt(bt.Pos+bt.Beg, bt.Block())
}

func (p *Parser) parseAt(basePos int, src string) (out Tokens, err error) {
	in, err := p.scanAt(basePos, src, true)
	if err != nil {
		return out, err
	}
	return p.parseStmts(in)
}

// compositeBraceAt reports whether toks[i] is a BraceBlock that is the value
// part of a composite literal whose LiteralType is a slice, array, map or
// struct type (e.g. the "{}" in `[]byte{}`, `map[K]V{}`, `[N]T{}`,
// `[]pkg.T{}`, `struct{...}{}`) rather than a statement body. Such literals may
// appear unparenthesized in the header of a for/if/switch statement (only the
// bare TypeName form, `T{}`, requires parentheses there), so stmtEnd must not
// mistake their brace for the statement body when scanning past clause
// separators.
//
// It walks left from toks[i-1] over the tokens that could form such a literal
// type (the element type Idents/`.`/`*`/`<-`/`chan`, the `[]`/`[N]`/`map`
// openers, and `struct{...}`/`interface{...}`) and returns true only if the
// leftmost token consumed is one of those openers -- i.e. the type genuinely
// *starts* with `[`/`map`/`struct`/`interface`, distinguishing `[]byte{...}`
// from an index expression like `flags[i]` that happens to precede a body.
func (p *Parser) compositeBraceAt(toks Tokens, i int) bool {
	if i <= 0 || toks[i].Tok != lang.BraceBlock {
		return false
	}
	first := -1 // index of the leftmost token belonging to the literal type
walk:
	for j := i - 1; j >= 0; j-- {
		switch toks[j].Tok {
		case lang.Ident, lang.Period, lang.Mul, lang.Arrow, lang.Chan,
			lang.BracketBlock, lang.Map, lang.Struct, lang.Interface:
			first = j
		case lang.BraceBlock:
			// A `{...}` is part of the type only as the body of a `struct{...}` /
			// `interface{...}` immediately to its left.
			if j == 0 || (toks[j-1].Tok != lang.Struct && toks[j-1].Tok != lang.Interface) {
				break walk
			}
			first = j - 1
			j-- // also step over the struct/interface keyword
		default:
			break walk
		}
	}
	if first < 0 {
		return false
	}
	switch toks[first].Tok {
	case lang.BracketBlock, lang.Map, lang.Struct, lang.Interface:
		return true
	}
	return false
}

func (p *Parser) stmtEnd(toks Tokens) (int, error) {
	end := toks.Index(lang.Semicolon)
	if end == -1 {
		return -1, scan.ErrBlock
	}
	firstTok := toks[0].Tok
	// A label "Ident :" followed by a HasInit statement is treated as one statement.
	if firstTok == lang.Ident && len(toks) > 2 && toks[1].Tok == lang.Colon {
		firstTok = toks[2].Tok
	}
	if p.TokenProps[firstTok].HasInit {
		// Skip clause-separator semicolons until the one that follows the
		// statement body. A BraceBlock just before a semicolon is normally the
		// body, except when it is a composite literal value in a header clause
		// (e.g. `for x := []byte{}; cond; { ... }`).
		for toks[end-1].Tok != lang.BraceBlock || p.compositeBraceAt(toks, end-1) {
			e2 := toks[end+1:].Index(lang.Semicolon)
			if e2 == -1 {
				return -1, scan.ErrBlock
			}
			end += 1 + e2
		}
	}
	return end, nil
}

func (p *Parser) parseStmts(in Tokens) (out Tokens, err error) {
	for len(in) > 0 {
		end, err := p.stmtEnd(in)
		if err != nil {
			return out, err
		}
		o, err := p.parseStmt(in[:end])
		if err != nil {
			return out, err
		}
		out = append(out, o...)
		p.drainPendingMethods(&out)
		in = in[end+1:]
	}
	return out, err
}

// scanDecls scans src and returns its top-level statements as token slices, without parsing them.
func (p *Parser) scanDecls(src string) ([]Tokens, error) {
	toks, err := p.scanAt(p.PosBase, src, true)
	if err != nil {
		return nil, err
	}
	var decls []Tokens
	for len(toks) > 0 {
		end, err := p.stmtEnd(toks)
		if err != nil {
			return nil, err
		}
		decls = append(decls, toks[:end])
		toks = toks[end+1:]
	}
	return decls, nil
}

// ParseOneStmt parses a single pre-scanned statement token slice.
func (p *Parser) ParseOneStmt(toks Tokens) (Tokens, error) {
	out, err := p.parseStmt(toks)
	p.drainPendingMethods(&out)
	return out, err
}

func (p *Parser) drainPendingMethods(out *Tokens) {
	if len(p.pendingMethodDefs) > 0 {
		*out = append(*out, p.pendingMethodDefs...)
		p.pendingMethodDefs = p.pendingMethodDefs[:0]
	}
}

// ParseDecl resolves a declaration's symbols (Phase 1) without emitting code.
// Returns handled=true if fully resolved, false if code generation is needed.
func (p *Parser) ParseDecl(toks Tokens) (handled bool, err error) {
	if len(toks) == 0 {
		return true, nil
	}
	if toks[0].Tok != lang.Package && p.pkgName == "" {
		if !p.noPkg {
			return false, errors.New("no package defined")
		}
		p.pkgName = "main"
	}
	switch toks[0].Tok {
	case lang.Package:
		_, err = p.parsePackageDecl(toks)
		return true, err
	case lang.Import:
		_, err = p.parseImports(toks)
		return true, err
	case lang.Const:
		_, err = p.parseConst(toks)
		return true, err
	case lang.Type:
		_, err = p.parseType(toks)
		return true, err
	case lang.Func:
		isTemplate, err := p.registerFunc(toks)
		if err != nil {
			return false, err
		}
		if isTemplate {
			return true, nil // Generic template - instantiated on use.
		}
		if toks.LastIndex(lang.BraceBlock) < 0 {
			return true, nil // Body-less function (e.g. runtime-linked): signature only.
		}
		return false, nil // Body still needs full parse + generate.
	case lang.Var:
		return p.parseVarDecl(toks)
	}
	return false, nil
}

func (p *Parser) precedence(t Token) int {
	return p.TokenProps[t.Tok].Precedence
}
