package goparser

import (
	"errors"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

func moveDefaultLast(clauses []Tokens) {
	last := len(clauses) - 1
	for i, cl := range clauses {
		if cl[1].Tok == lang.Colon && i != last {
			clauses[i], clauses[last] = clauses[last], clauses[i]
			return
		}
	}
}

// splitAndSortVarDecls splits var(...) blocks into individual declarations,
// topologically sorts them by dependency, and returns the reordered list.
// Funcs and other statements keep their original relative positions; only
// var declarations are extracted, sorted, and placed back into var slots.
func (p *Parser) splitAndSortVarDecls(decls []Tokens) []Tokens {
	// Expand var blocks and identify var slot positions.
	type slot struct {
		pos  int    // position in expanded list
		decl Tokens // the var declaration
	}
	var expanded []Tokens
	var varSlots []slot
	for _, decl := range decls {
		if len(decl) == 0 {
			continue
		}
		switch decl[0].Tok {
		case lang.Var:
			for _, vd := range p.splitVarBlock(decl) {
				varSlots = append(varSlots, slot{pos: len(expanded), decl: vd})
				expanded = append(expanded, vd)
			}
		default:
			expanded = append(expanded, decl)
		}
	}
	if len(varSlots) <= 1 {
		return expanded
	}

	// Extract var declarations, sort by dependency, and place back.
	vars := make([]Tokens, len(varSlots))
	for i, s := range varSlots {
		vars[i] = s.decl
	}
	vars = p.sortByDeps(vars)
	for i, s := range varSlots {
		expanded[s.pos] = vars[i]
	}
	return expanded
}

func (p *Parser) varLines(toks Tokens) ([]Tokens, error) {
	if len(toks) < 2 {
		return nil, errors.New("missing expression")
	}
	if toks[1].Tok != lang.ParenBlock {
		return []Tokens{toks[1:]}, nil
	}
	inner, err := p.scanBlock(toks[1].Token, false)
	if err != nil {
		return nil, err
	}
	return inner.Split(lang.Semicolon), nil
}

func (p *Parser) splitVarBlock(decl Tokens) []Tokens {
	lines, err := p.varLines(decl)
	if err != nil || len(lines) <= 1 {
		return []Tokens{decl}
	}
	result := make([]Tokens, 0, len(lines))
	for _, line := range lines {
		if len(line) > 0 {
			d := make(Tokens, 1, 1+len(line))
			d[0] = decl[0]
			result = append(result, append(d, line...))
		}
	}
	return result
}

func (p *Parser) sortByDeps(decls []Tokens) []Tokens {
	if len(decls) <= 1 {
		return decls
	}
	nameSet := map[string]int{}
	for i, decl := range decls {
		if len(decl) >= 2 && decl[1].Tok == lang.Ident {
			nameSet[decl[1].Str] = i
		}
	}
	if len(nameSet) == 0 {
		return decls
	}

	n := len(decls)
	rdeps := make([][]int, n)
	inDeg := make([]int, n)
	for i, decl := range decls {
		seen := map[int]bool{}
		rhs := decl[1:] // skip "var" keyword
		if j := rhs.Index(lang.Assign); j >= 0 {
			rhs = rhs[j+1:]
		}
		p.collectIdents(rhs, nameSet, seen)
		for dep := range seen {
			rdeps[dep] = append(rdeps[dep], i)
			inDeg[i]++
		}
	}

	queue := make([]int, 0, n)
	for i, d := range inDeg {
		if d == 0 {
			queue = append(queue, i)
		}
	}
	result := make([]Tokens, 0, n)
	for head := 0; head < len(queue); head++ {
		i := queue[head]
		result = append(result, decls[i])
		for _, j := range rdeps[i] {
			if inDeg[j]--; inDeg[j] == 0 {
				queue = append(queue, j)
			}
		}
	}
	for i, d := range inDeg {
		if d > 0 {
			result = append(result, decls[i])
		}
	}
	return result
}

func (p *Parser) collectIdents(toks Tokens, nameSet map[string]int, out map[int]bool) {
	for _, t := range toks {
		if t.Tok == lang.Ident {
			if dep, ok := nameSet[t.Str]; ok {
				out[dep] = true
			}
		} else if t.Tok.IsBlock() {
			if inner, err := p.scanBlock(t.Token, false); err == nil {
				p.collectIdents(inner, nameSet, out)
			}
		}
	}
}

func (p *Parser) parseVarDecl(toks Tokens) (handled bool, err error) {
	lines, err := p.varLines(toks)
	if err != nil {
		return true, err
	}
	hasInit := false
	for _, lt := range lines {
		if lt.Index(lang.Assign) >= 0 {
			hasInit = true
			break
		}
	}
	if hasInit {
		for _, lt := range lines {
			decl := lt
			var rhs Tokens
			if i := decl.Index(lang.Assign); i >= 0 {
				rhs = decl[i+1:]
				decl = decl[:i]
			}
			// Resolve type once for all names sharing this declaration.
			var rhsTyp *vm.Type
			if len(rhs) > 0 && rhs[0].Tok == lang.BracketBlock {
				elemTyp, n, err := p.parseTypeExpr(rhs)
				if errors.Is(err, ErrEllipsisArray) {
					rhsTyp, _ = p.resolveEllipsisArray(elemTyp, rhs, n)
				} else if err == nil {
					rhsTyp = elemTyp
				}
			}
			// Right-to-left so a trailing type applies to all names (Go grammar: "a, b int").
			parts := decl.Split(lang.Comma)
			var lastTyp *vm.Type
			for i := len(parts) - 1; i >= 0; i-- {
				ct := parts[i]
				if len(ct) == 0 {
					continue
				}
				if ct[0].Tok != lang.Ident {
					continue
				}
				rawName := ct[0].Str
				if rawName == "_" {
					rawName = p.blankName()
				}
				name := p.scopedName(rawName)
				if _, _, ok := p.Symbols.Get(rawName, p.scope); !ok {
					p.SymAdd(symbol.UnsetAddr, name, nilValue, symbol.Var, nil)
				}
				if len(ct) > 1 {
					if t, _, _ := p.parseTypeExpr(ct[1:]); t != nil {
						lastTyp = t
					}
				}
				typ := rhsTyp
				if lastTyp != nil {
					typ = lastTyp
				}
				if typ != nil {
					p.Symbols[name].Type = typ
				}
			}
		}
		return false, nil
	}
	// No initializer: full parse is just symbol registration.
	_, err = p.parseVar(toks)
	return true, err
}

func (p *Parser) parseStmt(in Tokens) (out Tokens, err error) {
	for len(in) > 0 && in[len(in)-1].Tok == lang.Comment {
		in = in[:len(in)-1]
	}
	if len(in) == 0 {
		return nil, nil
	}
	// Preliminary: make sure that a pkgName is defined (or about to be).
	if in[0].Tok != lang.Package && p.pkgName == "" {
		if !p.noPkg {
			return out, errors.New("no package defined")
		}
		p.pkgName = "main"
	}

	switch t := in[0]; t.Tok {
	case lang.Break:
		return p.parseBreak(in)
	case lang.Continue:
		return p.parseContinue(in)
	case lang.Const:
		return p.parseConst(in)
	case lang.For:
		return p.parseFor(in)
	case lang.Func:
		// If a ParenBlock follows the function body, this is an IIFE
		// (immediately invoked function expression), not a function declaration.
		bi := in.LastIndex(lang.BraceBlock)
		if bi >= 0 && bi+1 < len(in) && in[bi+1].Tok == lang.ParenBlock {
			return p.parseExprStmt(in)
		}
		return p.parseFunc(in)
	case lang.Fallthrough:
		return out, errors.New("fallthrough statement out of place")
	case lang.Defer:
		return p.parseDefer(in)
	case lang.Go:
		return p.parseGo(in)
	case lang.Select:
		return p.parseSelect(in)
	case lang.Goto:
		return p.parseGoto(in)
	case lang.If:
		return p.parseIf(in)
	case lang.Import:
		return p.parseImports(in)
	case lang.Package:
		return p.parsePackageDecl(in)
	case lang.Return:
		return p.parseReturn(in)
	case lang.Switch:
		return p.parseSwitch(in)
	case lang.Type:
		return p.parseType(in)
	case lang.Var:
		return p.parseVar(in)
	case lang.BraceBlock:
		label := "block" + strconv.Itoa(p.labelCount[p.scope])
		p.labelCount[p.scope]++
		p.pushScope(label)
		defer p.popScope()
		return p.parseTokBlock(in[0].Token)
	case lang.Mul, lang.ParenBlock:
		if i := in.Index(lang.Assign); i > 0 {
			return p.parseAssign(in, i)
		}
		if op, i := indexCompoundAssign(in); i > 0 {
			return p.parseCompoundAssign(in, i, op)
		}
		if l := len(in); l >= 2 && (in[l-1].Tok == lang.Inc || in[l-1].Tok == lang.Dec) {
			return p.parseIncDec(in)
		}
		return p.parseExprStmt(in)
	case lang.Ident:
		if in.Index(lang.Colon) == 1 {
			return p.parseLabel(in)
		}
		if i := in.Index(lang.Arrow); i > 0 {
			// Only a send statement (ch <- v) if the arrow precedes any assignment.
			defIdx := in.Index(lang.Define)
			assIdx := in.Index(lang.Assign)
			if (defIdx < 0 || i < defIdx) && (assIdx < 0 || i < assIdx) {
				return p.parseChanSend(in, i)
			}
		}
		if i := in.Index(lang.Assign); i > 0 {
			return p.parseAssign(in, i)
		}
		if i := in.Index(lang.Define); i > 0 {
			return p.parseAssign(in, i)
		}
		if op, i := indexCompoundAssign(in); i > 0 {
			return p.parseCompoundAssign(in, i, op)
		}
		if l := len(in); l >= 2 && (in[l-1].Tok == lang.Inc || in[l-1].Tok == lang.Dec) {
			return p.parseIncDec(in)
		}
		fallthrough
	default:
		return p.parseExprStmt(in)
	}
}

// parseExprStmt parses an expression used as a statement, bracketing it with
// PopExpr markers so the compiler discards any unused return values.
func (p *Parser) parseExprStmt(in Tokens) (Tokens, error) {
	expr, err := p.parseExpr(in, "")
	if err != nil {
		return expr, err
	}
	// Discard unused return values from expression-statement calls inside function
	// bodies or loops. At the top level outside loops, leave values for the REPL.
	if len(expr) > 0 && expr[len(expr)-1].Tok == lang.Call && (p.funcDepth > 0 || p.loopDepth > 0) {
		out := make(Tokens, 0, len(expr)+2)
		out = append(out, newToken(lang.PopExpr, "", in[0].Pos, 0)) // mark start
		out = append(out, expr...)
		out = append(out, newToken(lang.PopExpr, "", in[0].Pos, 1)) // pop excess
		return out, nil
	}
	return expr, nil
}

func tokensToBlock(toks Tokens) string {
	var sb strings.Builder
	for i, t := range toks {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(t.Str)
	}
	return sb.String()
}

func (p *Parser) numItems(s string, sep lang.Token) (int, error) {
	tokens, err := p.scan(s, false)
	if err != nil {
		return -1, err
	}
	r := 0
	for _, t := range tokens.Split(sep) {
		if len(t) == 0 {
			continue
		}
		r++
	}
	return r, nil
}
