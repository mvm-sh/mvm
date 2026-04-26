// Package goparser implements a structured parser for Go.
package goparser

import (
	"errors"
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
	function        *symbol.Symbol // current function
	scope           string         // current scope
	fname           string         // current function name
	pkgName         string         // current package name
	noPkg           bool           // true if package statement is not mandatory (test, repl).
	pkgfs           fs.FS          // filesystem to read imported sources from
	stdlibfs        fs.FS          // fallback filesystem for embedded stdlib sources
	importRemaining []Tokens       // code-gen declarations from imported source packages

	funcScope         string
	framelen          map[string]int // length of function frames indexed by funcScope
	labelCount        map[string]int
	breakLabel        string
	continueLabel     string
	pendingLabel      string               // user label preceding the current for/switch statement
	labeledJump       map[string][2]string // maps user label to [continueLabel, breakLabel]
	clonum            int                  // closure instance number
	initNum           int                  // init function instance counter
	InitFuncs         []string             // ordered list of init function internal names
	blankSeq          int                  // counter for unique blank identifier names
	namedOut          []string             // scoped names of named return vars for current function
	symTracker        []string             // accumulates newly-added symbol keys during a checkpoint window; nil = not tracking
	pendingMethodDefs Tokens               // method defs from generic type instantiation, drained into output
	typeOnly          bool                 // when true, addSymVar is a no-op (Phase 1 signature-only parse)
	inForInit         bool                 // true while parsing for-init or range clause (marks LoopVar)
	funcDepth         int                  // nesting depth of function bodies (>0 means inside a function)
	loopDepth         int                  // nesting depth of for loops (>0 means inside a loop)
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

// ImportPackageValues populates packages with values.
func (p *Parser) ImportPackageValues(m map[string]map[string]reflect.Value) {
	for k, v := range m {
		p.Packages[k] = symbol.BinPkg(v, k)
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

// Parser errors.
var (
	errBody     = errors.New("missign body")
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

// scan performs lexical analysis on s and returns Tokens or an error.
func (p *Parser) scan(s string, endSemi bool) (out Tokens, err error) {
	return p.scanAt(0, s, endSemi)
}

// scanAt is like scan but adds basePos to every token position, so that
// block-relative positions become absolute within the source file.
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

// scanBlock scans the inner content of a block token, adjusting positions
// so they are absolute within the source file rather than block-relative.
func (p *Parser) scanBlock(bt scan.Token, endSemi bool) (Tokens, error) {
	return p.scanAt(bt.Pos+bt.Beg, bt.Block(), endSemi)
}

// parseTokBlock parses the inner content of a block token with absolute positions.
func (p *Parser) parseTokBlock(bt scan.Token) (Tokens, error) {
	return p.parseAt(bt.Pos+bt.Beg, bt.Block())
}

// parseAt is like Parse but adds basePos to every token position.
func (p *Parser) parseAt(basePos int, src string) (out Tokens, err error) {
	in, err := p.scanAt(basePos, src, true)
	if err != nil {
		return out, err
	}
	return p.parseStmts(in)
}

// stmtEnd returns the index of the semicolon ending the first statement in toks,
// accounting for HasInit tokens (if, for, switch) which contain internal semicolons.
// Labeled statements (Label: for/switch ...) are treated as a single statement.
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
		for {
			last := end - 1
			for last >= 0 && toks[last].Tok == lang.Comment {
				last--
			}
			if toks[last].Tok == lang.BraceBlock {
				break
			}
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
	toks, err := p.scan(src, true)
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

// drainPendingMethods appends any accumulated generic method definitions
// to the output and clears the pending buffer.
func (p *Parser) drainPendingMethods(out *Tokens) {
	if len(p.pendingMethodDefs) > 0 {
		*out = append(*out, p.pendingMethodDefs...)
		p.pendingMethodDefs = p.pendingMethodDefs[:0]
	}
}

// ParseDecl resolves a declaration's symbols (Phase 1) without emitting code.
// Returns handled=true if fully resolved, false if code generation is needed.
func (p *Parser) ParseDecl(toks Tokens) (handled bool, err error) {
	for len(toks) > 0 && toks[len(toks)-1].Tok == lang.Comment {
		toks = toks[:len(toks)-1]
	}
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
