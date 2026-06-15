package goparser

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

func moveDefaultLast(clauses []Tokens) {
	last := len(clauses) - 1
	for i, cl := range clauses {
		if len(cl) < 2 {
			continue
		}
		if cl[1].Tok == lang.Colon && i != last {
			clauses[i], clauses[last] = clauses[last], clauses[i]
			return
		}
	}
}

func (p *Parser) caseClauses(body Tokens) []Tokens {
	sc := body.SplitStart(lang.Case)
	out := sc[:0]
	for _, cl := range sc {
		if len(cl) == 0 {
			continue
		}
		if cl[0].Tok != lang.Case && cl[0].Tok != lang.Default {
			continue
		}
		out = append(out, cl)
	}
	moveDefaultLast(out)
	return out
}

func (p *Parser) splitAndSortVarDecls(decls []DeferredDecl) []DeferredDecl {
	type slot struct {
		pos  int          // position in expanded list
		decl DeferredDecl // the var declaration
	}
	var expanded []DeferredDecl
	var varSlots []slot
	for _, decl := range decls {
		if len(decl.Toks) == 0 {
			continue
		}
		// Expand var blocks and identify var slot positions.
		switch decl.Toks[0].Tok {
		case lang.Var:
			for _, vd := range p.splitVarBlock(decl.Toks) {
				vdd := DeferredDecl{PkgPath: decl.PkgPath, Toks: vd}
				varSlots = append(varSlots, slot{pos: len(expanded), decl: vdd})
				expanded = append(expanded, vdd)
			}
		default:
			expanded = append(expanded, decl)
		}
	}
	if len(varSlots) <= 1 {
		return expanded
	}

	// Pre-populate Symbol.Reads only when there's something to reorder.
	p.collectFuncReads(expanded)

	// Extract var declarations, sort by dependency, and place back.
	vars := make([]DeferredDecl, len(varSlots))
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
		return nil, p.errAt(toks[0], "missing expression after var")
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

func (p *Parser) sortByDeps(decls []DeferredDecl) []DeferredDecl {
	if len(decls) <= 1 {
		return decls
	}
	// Key nameSet by the canonical Symbol key so it matches sym.Name from walkRefs.
	nameSet := map[string]int{}
	for i, decl := range decls {
		if len(decl.Toks) >= 2 && decl.Toks[1].Tok == lang.Ident {
			nameSet[p.pkgKey(decl.Toks[1].Str)] = i
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
		rhs := decl.Toks[1:] // skip "var" keyword
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
	result := make([]DeferredDecl, 0, n)
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
	p.walkRefs(toks, "", func(sym *symbol.Symbol) {
		switch sym.Kind {
		case symbol.Var:
			if dep, ok := nameSet[sym.Name]; ok {
				out[dep] = true
			}
		case symbol.Func:
			for r := range sym.Reads {
				if dep, ok := nameSet[r.Name]; ok {
					out[dep] = true
				}
			}
		}
	})
}

func (p *Parser) collectFuncReads(decls []DeferredDecl) {
	// calls is keyed only by the funcs we actually walk, not the entire
	// symbol table; the fixed-point below iterates this small set.
	calls := map[*symbol.Symbol]map[*symbol.Symbol]bool{}
	for _, dd := range decls {
		decl := dd.Toks
		if len(decl) < 2 || decl[0].Tok != lang.Func {
			continue
		}
		// init() runs after var-inits and is uncallable from code per Go
		// spec, so its body's reads can't affect var-init ordering.
		if decl[1].Tok == lang.Ident && decl[1].Str == "init" {
			continue
		}
		sym, body, scope := p.funcSymBodyScope(decl)
		if sym == nil || body == nil {
			continue
		}
		sym.Reads = map[*symbol.Symbol]bool{}
		called := map[*symbol.Symbol]bool{}
		calls[sym] = called
		p.registerParamPlaceholders(sym, scope)
		p.walkRefs(body, scope, func(s *symbol.Symbol) {
			switch s.Kind {
			case symbol.Var:
				sym.Reads[s] = true
			case symbol.Func:
				if s != sym {
					called[s] = true
				}
			}
		})
	}
	for {
		changed := false
		for sym, called := range calls {
			for callee := range called {
				for v := range callee.Reads {
					if !sym.Reads[v] {
						sym.Reads[v] = true
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
}

func (p *Parser) registerParamPlaceholders(sym *symbol.Symbol, scope string) {
	if scope == "" {
		return
	}
	add := func(name string) {
		if name == "" || name == "_" {
			return
		}
		key := scope + "/" + name
		if _, exists := p.Symbols[key]; exists {
			return
		}
		p.Symbols[key] = &symbol.Symbol{Kind: symbol.LocalVar, Name: name}
		p.Seg.Add(key)
		p.recordDirectLocal(key)
	}
	for _, n := range sym.InNames {
		add(n)
	}
	for _, n := range sym.OutNames {
		add(n)
	}
	add(sym.RecvName)
}

func (p *Parser) funcSymBodyScope(decl Tokens) (*symbol.Symbol, Tokens, string) {
	bi := decl.LastIndex(lang.BraceBlock)
	if bi < 0 {
		return nil, nil, ""
	}
	var name string
	switch decl[1].Tok {
	case lang.Ident:
		name = decl[1].Str
	case lang.ParenBlock:
		if !isMethodDecl(decl) {
			return nil, nil, ""
		}
		recvr, err := p.scanBlock(decl[1].Token, false)
		if err != nil {
			return nil, nil, ""
		}
		recvType := recvTypeName(recvr)
		if recvType == "" {
			return nil, nil, ""
		}
		name = recvType + "." + decl[2].Str
	default:
		return nil, nil, ""
	}
	sym, _, ok := p.symGet(name)
	if !ok {
		return nil, nil, ""
	}
	body, err := p.scanBlock(decl[bi].Token, false)
	if err != nil {
		return nil, nil, ""
	}
	return sym, body, name
}

func (p *Parser) walkRefs(toks Tokens, scope string, visit func(*symbol.Symbol)) {
	savedScope := p.scope
	p.scope = scope
	defer func() { p.scope = savedScope }()
	for i, t := range toks {
		if t.Tok != lang.Ident {
			if t.Tok.IsBlock() {
				if inner, err := p.scanBlock(t.Token, false); err == nil {
					p.walkRefs(inner, scope, visit)
				}
			}
			continue
		}
		// Period-prefix first: x.Foo is never a sibling-var ref to Foo.
		if i > 0 && toks[i-1].Tok == lang.Period {
			j := prevIdentBeforePeriod(toks, i)
			if j < 0 {
				continue
			}
			recv, _, ok := p.symGet(toks[j].Str)
			if !ok || recv.Kind == symbol.Pkg || recv.Kind == symbol.Func {
				continue
			}
			if m, _ := p.Symbols.MethodByName(recv, t.Str, p.Seg); m != nil && m.Kind == symbol.Func {
				visit(m)
			}
			continue
		}
		sym, sc, ok := p.symGet(t.Str)
		if !ok || sc != "" {
			continue
		}
		if sym.Kind == symbol.Var || sym.Kind == symbol.Func {
			visit(sym)
		}
	}
}

func prevIdentBeforePeriod(toks Tokens, idx int) int {
	j := idx - 2
	for j >= 0 && (toks[j].Tok == lang.BraceBlock || toks[j].Tok == lang.ParenBlock || toks[j].Tok == lang.BracketBlock) {
		j--
	}
	if j < 0 || toks[j].Tok != lang.Ident {
		return -1
	}
	return j
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
			for _, ct := range slices.Backward(parts) {
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
				name := p.pkgKey(rawName)
				if _, _, ok := p.symGet(rawName); !ok {
					p.SymAdd(symbol.UnsetAddr, name, nilValue, symbol.Var, nil)
				}
				if len(ct) > 1 {
					// Propagate ErrUndefined so the fixed-point loop retries
					// once the type lands. Otherwise the Symbol's Type stays
					// nil at the qualified key and Phase 2's parseVarLine
					// writes a divergent entry at the bare key.
					t, _, terr := p.parseTypeExpr(ct[1:])
					if terr != nil {
						var eu ErrUndefined
						if errors.As(terr, &eu) {
							return false, terr
						}
					}
					if t != nil {
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
	if len(in) == 0 {
		return nil, nil
	}
	// Preliminary: make sure that a pkgName is defined (or about to be).
	if in[0].Tok != lang.Package && p.pkgName == "" {
		if !p.noPkg {
			return out, p.errAt(in[0], "no package defined")
		}
		p.pkgName = "main"
		p.backfillPlaceholderPkgPath()
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
		return out, p.errAt(in[0], "fallthrough statement out of place")
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
	// Discard unused values from expression statements inside function bodies or loops.
	// At the top level outside loops, leave values for the REPL.
	if len(expr) > 0 && (p.funcDepth > 0 || p.loopDepth > 0) {
		switch expr[len(expr)-1].Tok {
		case lang.Call:
			out := make(Tokens, 0, len(expr)+2)
			out = append(out, newToken(lang.PopExpr, "", in[0].Pos, 0)) // mark start
			out = append(out, expr...)
			out = append(out, newToken(lang.PopExpr, "", in[0].Pos, 1)) // pop excess
			return out, nil
		case lang.Arrow:
			// Bare receive statement (<-ch): drop the discarded received value.
			return append(expr, newDrop(in[0].Pos)), nil
		}
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
