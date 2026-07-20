// Package goparser implements a structured parser for Go.
package goparser

import (
	"errors"
	"fmt"
	"go/constant"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/scan"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// Parser represents the state of a parser.
type Parser struct {
	*scan.Scanner

	Symbols         symbol.SymMap
	Seg             symbol.SegIndex // last-segment index over Symbols, for fast method resolution; kept in sync with Symbols
	Packages        map[string]*symbol.Package
	function        *symbol.Symbol                    // current function
	scope           string                            // current scope
	pkgName         string                            // current package name
	noPkg           bool                              // true if package statement is not mandatory (test, repl).
	pkgfs           fs.FS                             // filesystem to read imported sources from
	stdlibfs        fs.FS                             // fallback filesystem for embedded stdlib sources
	remotefs        fs.FS                             // last-resort filesystem (e.g. network module proxy)
	testSrcFS       fs.FS                             // test-only fs for bridged-stdlib *_test.go sources
	testSkipFiles   map[string]bool                   // basenames of bridged-stdlib test files to skip
	includeTests    bool                              // include _test.go files when loading package sources
	externalTests   []PackageSource                   // external `package X_test` files for `mvm test`
	importRemaining []DeferredDecl                    // code-gen declarations from imported source packages
	CompilingPkg    string                            // while a deferred decl is being parsed/compiled in Phase 2
	importingPkg    string                            // while parseSrc is running for an imported package
	fileAliases     map[int]map[string]*symbol.Symbol // per-file import scope: srcIndex -> alias name -> Pkg symbol
	bareAliases     map[string]bool                   // bare keys aliased to an import-path target's top-level symbols

	funcScope      string
	framelen       map[string]int      // length of function frames indexed by funcScope
	directLocals   map[string][]string // funcScope -> its direct-level LocalVar keys
	labelCount     map[string]int
	breakLabel     string
	continueLabel  string
	pendingLabel   string                   // user label preceding the current for/switch statement
	labeledJump    map[string][2]string     // maps user label to [continueLabel, breakLabel]
	ctrlStack      []ctrlFrame              // active for/switch/select frames (for labeled break/continue range unwind)
	clonum         int                      // closure instance number, package-global counter
	funcN          int                      // anonymous-function counter within the current outer function
	initNum        int                      // init function instance counter
	InitFuncs      []string                 // ordered list of init function internal names
	blankSeq       int                      // counter for unique blank identifier names
	namedOut       []string                 // scoped names of named return vars for current function
	symTracker     []string                 // accumulates newly-added symbol keys during a checkpoint window; nil = not tracking
	genCounter     int                      // monotonic source of resolveDecls generations
	curGen         int                      // generation of the current resolveDecls (nestable; saved/restored)
	typeGen        map[*mtype.Type]int      // generation each declared placeholder *Type was minted in
	batchFuncDecls map[string]bool          // canonical keys of top-level funcs/methods registered in the current resolveDecls batch
	forwardDecls   map[string]bool          // batch keys registered bodyless
	instanceDecls  []DeferredDecl           // generic instance bodies tagged with their template's package
	funcInstArgs   map[string][]*mtype.Type // generic-func instance name -> bound type args
	instantiating  map[string]bool          // generic-type instances whose body is parsing now
	typeOnly       bool                     // when true, addSymVar is a no-op (Phase 1 signature-only parse)
	regFuncSig     bool                     // set by parseFunc so the outermost func-type parse registers its params as locals
	inForInit      bool                     // true while parsing for-init or range clause (marks LoopVar)
	rangeAssign    Tokens                   // assign-form range per-iteration assigns, stashed for parseFor
	blockDepth     int                      // nesting depth of function bodies and loops (>0 discards unused expr-statement values)
	instDepth      int                      // nesting depth of generic instantiations
	buildCtx       *buildContext            // build constraint context for file filtering
	embeds         map[string][]byte        // //go:embed file bytes by canonical var key
}

// RecordBareAlias marks key as a bare alias of an import-path target's
// top-level symbol, so a later unit's own same-named top-level func shadows it
// (see batchFuncDecl) instead of being dropped.
func (p *Parser) RecordBareAlias(key string) {
	if p.bareAliases == nil {
		p.bareAliases = map[string]bool{}
	}
	p.bareAliases[key] = true
}

// SymSet inserts sym at key in the symbol table, recording the key for potential rollback.
func (p *Parser) SymSet(key string, sym *symbol.Symbol) {
	if p.symTracker != nil {
		p.symTracker = append(p.symTracker, key)
	}
	p.Symbols[key] = sym
	p.Seg.Add(key)
	if sym.Kind == symbol.LocalVar {
		p.recordDirectLocal(key)
	}
}

// SymAdd adds a new named symbol, recording the key for potential rollback.
func (p *Parser) SymAdd(i int, name string, v vm.Value, k symbol.Kind, t *mtype.Type) {
	name = strings.TrimPrefix(name, "/")
	if p.symTracker != nil {
		p.symTracker = append(p.symTracker, name)
	}
	p.Symbols[name] = &symbol.Symbol{Kind: k, Name: name, Index: i, Value: v, Type: t}
	p.Seg.Add(name)
	if k == symbol.LocalVar {
		p.recordDirectLocal(name)
	}
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
		// Don't let another package's bare alias satisfy an unqualified reference
		// here; defer so this package's own same-named decl registers first.
		if ok && sc == "" && p.foreignBareAlias(s, name) {
			return nil, "", false
		}
	}
	return s, sc, ok
}

// foreignBareAlias reports whether s, found at bare key name while parsing
// imported package p.importingPkg, is another package's symbol aliased to that
// key by aliasTargetTopLevel. Such an alias has a qualified Name "<pkg>.<name>"
// for a foreign pkg; a universe alias (byte -> uint8) or dot-import is not
// qualified for name and is kept.
func (p *Parser) foreignBareAlias(s *symbol.Symbol, name string) bool {
	if s == nil || !strings.HasSuffix(s.Name, "."+name) {
		return false
	}
	return s.Name != QualifyName(p.importingPkg, name)
}

// foreignBareDecl reports whether a bare-key top-level decl of name (no package
// context) found an imported pkg's symbol aliased there; its canonical Name is
// "<pkg>.<name>", whereas a same-unit bare var keeps Name == name.
func (p *Parser) foreignBareDecl(s *symbol.Symbol, name string) bool {
	if p.importingPkg != "" || p.CompilingPkg != "" || s == nil {
		return false
	}
	return s.Name != name && strings.HasSuffix(s.Name, "."+name)
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

// SkipBridges unregisters the named packages' native bridges so they resolve
// from interpreted source instead, as the wasm `!wasm` tags do. No-op for
// unregistered paths.
func (p *Parser) SkipBridges(paths ...string) {
	for _, path := range paths {
		delete(p.Packages, path)
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
// pkgfs nor stdlibfs contain the requested import path.
func (p *Parser) SetRemoteFS(fsys fs.FS) {
	p.remotefs = fsys
}

// SetIncludeTests toggles whether ParseAll's directory-mode load includes _test.go files.
func (p *Parser) SetIncludeTests(b bool) {
	p.includeTests = b
}

// SetTestSourceFS installs the test-source filesystem consulted by
// LoadPackageSources only when (a) includeTests is on and (b) the target
// import path is a bridge-only stdlib package.
func (p *Parser) SetTestSourceFS(fsys fs.FS) {
	p.testSrcFS = fsys
}

// SetTestSkipFiles records basenames of bridged-stdlib test files that loadBridgedTestSources must skip.
func (p *Parser) SetTestSkipFiles(names map[string]bool) {
	p.testSkipFiles = names
}

// WithImportingPkg sets p.importingPkg to pkg and returns a function that restores the previous value.
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
		Scanner:      scan.NewScanner(spec),
		Symbols:      symbol.SymMap{},
		Seg:          symbol.SegIndex{},
		Packages:     map[string]*symbol.Package{},
		noPkg:        noPkg,
		framelen:     map[string]int{},
		directLocals: map[string][]string{},
		labelCount:   map[string]int{},
		labeledJump:  map[string][2]string{},
		buildCtx:     defaultBuildContext(),
	}
	p.Symbols.Init()
	p.rebuildSeg()
	return p
}

// rebuildSeg rebuilds Seg from the symbol table, after Init or a bulk mutation
// (RestoreUnit) where incremental tracking is impractical.
func (p *Parser) rebuildSeg() {
	p.Seg = make(symbol.SegIndex, len(p.Symbols))
	for k := range p.Symbols {
		p.Seg.Add(k)
	}
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
// struct type rather than a statement body.
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
			first = j
		default:
			break walk
		}
	}
	if first < 0 {
		return false
	}
	// A leftmost BracketBlock after an operand is a postfix index, not a `[]T{}` type prefix.
	if toks[first].Tok == lang.BracketBlock && first > 0 {
		switch toks[first-1].Tok {
		case lang.Char, lang.Float, lang.Imag, lang.Int, lang.String, lang.ParenBlock:
			return false
		}
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
		return -1, p.wrapAt(toks[0], scan.ErrBlock, "no statement terminator after %s", toks[0].Describe())
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
				return -1, p.wrapAt(toks[0], scan.ErrBlock, "missing body terminator for %s statement", firstTok)
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
	return p.parseStmt(toks)
}

// ResetUnitLabels resets the per-scope label counter so re-parsing a unit on a
// reused Parser is idempotent; labelCount is never decremented, so otherwise
// scope names drift (for0 -> for1 -> ...) and desync closure captures.
func (p *Parser) ResetUnitLabels() { clear(p.labelCount) }

// TakeInstanceDecls returns and clears the queued generic-instance bodies.
func (p *Parser) TakeInstanceDecls() []DeferredDecl {
	d := p.instanceDecls
	p.instanceDecls = nil
	return d
}

// UnitState is an opaque pre-compile snapshot for SnapshotUnit/RestoreUnit.
type UnitState struct {
	syms  map[string]*symbol.Symbol
	pkgs  map[string]*symbol.Package
	insts map[*genericTemplate]int // per pre-existing generic template: len(instances)
	inits int
}

// SnapshotUnit captures the symbol-table state before a top-level compile.
func (p *Parser) SnapshotUnit() UnitState {
	syms := make(map[string]*symbol.Symbol, len(p.Symbols))
	insts := map[*genericTemplate]int{}
	for k, v := range p.Symbols {
		syms[k] = v
		// A pre-existing generic template grows its instances slice in place;
		// record the length so a failed instantiation can be truncated back.
		if v.Kind == symbol.Generic {
			if t, ok := v.Data.(*genericTemplate); ok {
				insts[t] = len(t.instances)
			}
		}
	}
	pkgs := make(map[string]*symbol.Package, len(p.Packages))
	maps.Copy(pkgs, p.Packages)
	return UnitState{syms: syms, pkgs: pkgs, insts: insts, inits: len(p.InitFuncs)}
}

// RestoreUnit reverts a failed compile to s.
func (p *Parser) RestoreUnit(s UnitState) {
	for k := range p.Symbols {
		if _, ok := s.syms[k]; !ok {
			delete(p.Symbols, k)
		}
	}
	for k, v := range s.syms {
		if p.Symbols[k] != v {
			p.Symbols[k] = v // mvm:symkey-ok: restores snapshot keys verbatim, not a new binding
		}
	}
	for k := range p.Packages {
		if _, ok := s.pkgs[k]; !ok {
			delete(p.Packages, k)
		}
	}
	for t, n := range s.insts {
		if len(t.instances) > n {
			t.instances = t.instances[:n]
		}
	}
	if len(p.InitFuncs) > s.inits {
		p.InitFuncs = p.InitFuncs[:s.inits]
	}
	p.instanceDecls = nil
	p.importRemaining = nil
	p.rebuildSeg()          // map was mutated wholesale; resync the index
	p.rebuildDirectLocals() // and the direct-locals index
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
		p.backfillPlaceholderPkgPath()
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
