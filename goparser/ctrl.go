package goparser

import (
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

func (p *Parser) parseDefer(in Tokens) (out Tokens, err error) {
	if len(in) < 2 {
		return nil, errors.New("invalid defer statement")
	}
	expr := in[1:]
	// The last token must be a ParenBlock containing the call arguments.
	// We split here rather than calling parseExpr on the whole expression
	// because the lang.Func case in parseExpr would consume a trailing '()'.
	last := len(expr) - 1
	if last < 0 || expr[last].Tok != lang.ParenBlock {
		return nil, errors.New("defer requires a function call")
	}
	callTok := expr[last]
	narg, err := p.numItems(callTok.Block(), lang.Comma)
	if err != nil {
		return nil, err
	}

	// Parse the function expression (tokens before the call paren).
	if out, err = p.parseExpr(expr[:last], ""); err != nil {
		return out, err
	}
	// Parse the argument list (reversed into LIFO push order, as in parseBlock).
	argToks, err := p.parseBlock(callTok, "")
	if err != nil {
		return out, err
	}
	out = append(out, argToks...)
	out = append(out, newDefer(callTok.Pos, narg))
	return out, nil
}

func (p *Parser) parseGo(in Tokens) (out Tokens, err error) {
	if len(in) < 2 {
		return nil, errors.New("invalid go statement")
	}
	expr := in[1:]
	last := len(expr) - 1
	if last < 0 || expr[last].Tok != lang.ParenBlock {
		return nil, errors.New("go requires a function call")
	}
	callTok := expr[last]
	narg, err := p.numItems(callTok.Block(), lang.Comma)
	if err != nil {
		return nil, err
	}

	if out, err = p.parseExpr(expr[:last], ""); err != nil {
		return out, err
	}
	argToks, err := p.parseBlock(callTok, "")
	if err != nil {
		return out, err
	}
	out = append(out, argToks...)
	out = append(out, newGo(callTok.Pos, narg))
	return out, nil
}

func (p *Parser) parseChanSend(in Tokens, arrowIdx int) (out Tokens, err error) {
	if out, err = p.parseExpr(in[:arrowIdx], ""); err != nil {
		return out, err
	}
	val, err := p.parseExpr(in[arrowIdx+1:], "")
	if err != nil {
		return nil, err
	}
	out = append(out, val...)
	out = append(out, newChanSend(in[arrowIdx].Pos))
	return out, nil
}

func (p *Parser) parseBreak(in Tokens) (out Tokens, err error) {
	var label string
	switch len(in) {
	case 1:
		label = p.breakLabel
	case 2:
		if in[1].Tok != lang.Ident {
			return nil, errBreak
		}
		j, ok := p.labeledJump[p.labelName(in[1].Str)]
		if !ok {
			return nil, errBreak
		}
		label = j[1]
	default:
		return nil, errBreak
	}
	out = append(out, newGoto(label, in[0].Pos))
	return out, err
}

func (p *Parser) parseContinue(in Tokens) (out Tokens, err error) {
	var label string
	switch len(in) {
	case 1:
		label = p.continueLabel
	case 2:
		if in[1].Tok != lang.Ident {
			return nil, errContinue
		}
		j, ok := p.labeledJump[p.labelName(in[1].Str)]
		if !ok || j[0] == "" {
			return nil, errContinue
		}
		label = j[0]
	default:
		return nil, errContinue
	}
	out = append(out, newGoto(label, in[0].Pos))
	return out, err
}

func (p *Parser) parseGoto(in Tokens) (out Tokens, err error) {
	if len(in) != 2 || in[1].Tok != lang.Ident {
		return nil, errGoto
	}
	// TODO: check validity of user provided label
	return Tokens{newGoto(p.labelName(in[1].Str), in[0].Pos)}, nil
}

func (p *Parser) parseFor(in Tokens) (out Tokens, err error) {
	// TODO: detect invalid code.
	var init, cond, post, body, final Tokens
	hasRange := in.Index(lang.Range) >= 0
	pendingLabel := p.takePendingLabel()
	defer p.pushBreakScope("for", pendingLabel, true)()
	pre := in[1 : len(in)-1].Split(lang.Semicolon)
	// condLabel is the top of the loop (where Goto jumps back to).
	// For 3-clause for loops, continueLabel is set to the post-statement label
	// so that continue executes the post statement before re-checking the condition.
	condLabel := p.scope + "b"
	switch len(pre) {
	case 1:
		if hasRange {
			init = pre[0]
			p.inForInit = true
			init, err = p.parseStmt(init)
			p.inForInit = false
			if err != nil {
				return nil, err
			}
			out = init
			last := &out[len(out)-1]
			if len(last.Arg) == 0 {
				last.Arg = []any{0}
			}
			n, _ := last.Arg[0].(int)
			cond = Tokens{newNext(p.breakLabel, in[1].Pos, n)}
			final = Tokens{newToken(lang.Stop, "", in[1].Pos, n)}
		} else {
			cond = pre[0]
		}
	case 3:
		init, cond, post = pre[0], pre[1], pre[2]
		p.inForInit = true
		init, err = p.parseStmt(init)
		p.inForInit = false
		if err != nil {
			return nil, err
		}
		out = init
		// continue must run the post statement before looping; use a separate label.
		p.continueLabel = p.scope + "p"
	default:
		return nil, errFor
	}
	if pendingLabel != "" {
		p.labeledJump[pendingLabel] = [2]string{p.continueLabel, p.breakLabel}
	}
	out = append(out, newLabel(condLabel, in[0].Pos))
	if len(cond) > 0 {
		if cond, err = p.parseExpr(cond, ""); err != nil {
			return nil, err
		}
		out = append(out, cond...)
		if !hasRange {
			out = append(out, newJumpFalse(p.breakLabel, in[0].Pos))
		}
	}
	p.pushScope("b")
	p.loopDepth++
	body, err = p.parseTokBlock(in[len(in)-1].Token)
	p.loopDepth--
	p.popScope()
	if err != nil {
		return nil, err
	}
	out = append(out, body...)
	if len(post) > 0 {
		if post, err = p.parseStmt(post); err != nil {
			return nil, err
		}
		if p.continueLabel != condLabel {
			out = append(out, newLabel(p.continueLabel, in[0].Pos))
		}
		out = append(out, post...)
	}
	out = append(out,
		newGoto(condLabel, in[0].Pos),
		newLabel(p.breakLabel, in[0].Pos),
	)
	out = append(out, final...)
	return out, err
}

func (p *Parser) parseIf(in Tokens) (out Tokens, err error) {
	label := "if" + strconv.Itoa(p.labelCount[p.scope])
	p.labelCount[p.scope]++
	p.pushScope(label)
	defer p.popScope()
	endLabel := p.scope + "e0"
	elseCount := 1 // counter for intermediate else-branch labels: "e1", "e2", ...

	// Parse the if-else chain forward. Init is parsed before the body so that
	// variables defined in the init are visible inside the body block.
	i := 1 // skip initial 'if'
	for i < len(in) {
		clauseStart := i
		// Find the body block ({...}) for this clause.
		for i < len(in) && in[i].Tok != lang.BraceBlock {
			i++
		}
		if i >= len(in) {
			return nil, errors.New("expected '{', got end of input")
		}
		bodyIdx := i
		hasCondition := bodyIdx > clauseStart

		// Parse init and/or condition before the body.
		if hasCondition {
			initcond := in[clauseStart:bodyIdx]
			var init, cond Tokens
			if si := initcond.Index(lang.Semicolon); si >= 0 {
				init = initcond[:si]
				cond = initcond[si+1:]
			} else {
				cond = initcond
			}
			// Parse init first so that variables defined here are in scope for the body.
			if len(init) > 0 {
				if init, err = p.parseStmt(init); err != nil {
					return nil, err
				}
				out = append(out, init...)
			}
			if cond, err = p.parseExpr(cond, ""); err != nil {
				return nil, err
			}
			out = append(out, cond...)
		}

		i++ // move past body block
		hasMore := i < len(in) && in[i].Tok == lang.Else

		// Emit conditional jump to the next else clause or to the end.
		elseLabel := endLabel
		if hasMore {
			elseLabel = p.scope + "e" + strconv.Itoa(elseCount)
		}
		if hasCondition {
			out = append(out, newJumpFalse(elseLabel, in[bodyIdx].Pos))
		}

		// Parse body.
		body, err := p.parseTokBlock(in[bodyIdx].Token)
		if err != nil {
			return nil, err
		}
		out = append(out, body...)

		if hasMore {
			out = append(out, newGoto(endLabel, in[bodyIdx].Pos))
			out = append(out, newLabel(elseLabel, in[bodyIdx].Pos))
			elseCount++
			i++ // skip 'else'
			if i < len(in) && in[i].Tok == lang.If {
				i++ // skip 'if' in 'else if'
			}
		} else {
			break
		}
	}

	out = append(out, newLabel(endLabel, in[0].Pos))
	return out, err
}

func (p *Parser) parseSwitch(in Tokens) (out Tokens, err error) {
	var init, cond Tokens
	initcond := in[1 : len(in)-1]
	if ii := initcond.Index(lang.Semicolon); ii < 0 {
		cond = initcond
	} else {
		init = initcond[:ii]
		cond = initcond[ii+1:]
	}
	// Detect type switch: cond contains ".(type)".
	for i, t := range cond {
		if t.Tok == lang.Period && i+1 < len(cond) && cond[i+1].Tok == lang.ParenBlock {
			if strings.TrimSpace(cond[i+1].Block()) == "type" {
				return p.parseTypeSwitch(in, init, cond, i)
			}
		}
	}
	pendingLabel := p.takePendingLabel()
	defer p.pushBreakScope("switch", pendingLabel, false)()
	if len(init) > 0 {
		if init, err = p.parseStmt(init); err != nil {
			return nil, err
		}
		out = init
	}
	condSwitch := false
	if len(cond) > 0 {
		if cond, err = p.parseExpr(cond, ""); err != nil {
			return nil, err
		}
		out = append(out, cond...)
		condSwitch = true
	}
	// Split switch body into case clauses.
	clauses, err := p.scanBlock(in[len(in)-1].Token, true)
	if err != nil {
		return nil, err
	}
	sc := clauses.SplitStart(lang.Case)
	moveDefaultLast(sc)
	// Process each clause.
	nc := len(sc) - 1
	prevFallthrough := false
	for i, cl := range sc {
		co, hasFallthrough, err := p.parseCaseClause(cl, i, nc, condSwitch, prevFallthrough)
		if err != nil {
			return nil, err
		}
		out = append(out, co...)
		prevFallthrough = hasFallthrough
	}
	out = append(out, newLabel(p.breakLabel, in[len(in)-1].Pos))
	return out, err
}

func (p *Parser) parseTypeSwitch(in, init, cond Tokens, periodIdx int) (out Tokens, err error) {
	pos := in[0].Pos
	guardToks := cond[:periodIdx]
	varName := ""
	if defPos := guardToks.Index(lang.Define); defPos >= 0 {
		if defPos == 1 {
			varName = guardToks[0].Str
		}
		guardToks = guardToks[defPos+1:]
	}
	pendingLabel := p.takePendingLabel()
	defer p.pushBreakScope("switch", pendingLabel, false)()
	if len(init) > 0 {
		if init, err = p.parseStmt(init); err != nil {
			return nil, err
		}
		out = init
	}
	tsName := p.scope + "/_ts"
	p.SymAdd(symbol.UnsetAddr, tsName, vm.Value{}, symbol.Var, nil)
	guardParsed, err := p.parseExpr(guardToks, "")
	if err != nil {
		return nil, err
	}
	out = append(out, newIdent(tsName, pos))
	out = append(out, guardParsed...)
	out = append(out, newToken(lang.Define, "", pos, 1))
	// Split switch body into case clauses.
	clauses, err := p.scanBlock(in[len(in)-1].Token, true)
	if err != nil {
		return nil, err
	}
	sc := clauses.SplitStart(lang.Case)
	moveDefaultLast(sc)
	nc := len(sc) - 1
	for i, cl := range sc {
		co, err := p.parseTypeSwitchClause(cl, i, nc, tsName, varName)
		if err != nil {
			return nil, err
		}
		out = append(out, co...)
	}
	out = append(out, newLabel(p.breakLabel, in[len(in)-1].Pos))
	return out, nil
}

func (p *Parser) parseTypeSwitchClause(in Tokens, index, maximum int, tsName, varName string) (out Tokens, err error) {
	in = append(in, newSemicolon(in[len(in)-1].Pos))
	tl := in.Split(lang.Colon)
	if len(tl) != 2 {
		return nil, errors.New("invalid type switch case clause")
	}
	conds := tl[0][1:] // tokens after 'case' keyword
	pos := in[0].Pos
	switchScope := p.scope // e.g. "main/switch0"

	// Push a case-specific scope so that v has a unique symbol per case.
	caseScopeName := "c" + strconv.Itoa(index)
	p.pushScope(caseScopeName)

	// Declare v in this case scope.
	var vScoped string
	if varName != "" {
		vScoped = p.scope + "/" + varName
		p.SymAdd(symbol.UnsetAddr, vScoped, vm.Value{}, symbol.Var, nil)
	}
	body, err := p.parseStmts(tl[1])
	p.popScope() // back to switchScope
	if err != nil {
		return nil, err
	}

	endLabel := p.breakLabel // e.g. "main/switch0e"

	// Check for default clause (no types between 'case' and ':').
	if len(conds) == 0 || (len(conds) == 1 && conds[0].Tok == lang.Semicolon) {
		caseLabel := caseLabel(switchScope, index, 0)
		out = append(out, newLabel(caseLabel, pos))
		if varName != "" {
			// v = iface (interface type); compiler infers type from rhs.
			out = append(out, newIdent(vScoped, pos))
			out = append(out, newIdent(tsName, pos))
			out = append(out, newToken(lang.Assign, "", pos, 1))
		}
		out = append(out, body...)
		return out, nil
	}

	lcond := conds.Split(lang.Comma)
	isMulti := len(lcond) > 1
	bodyLabel := caseBodyLabel(switchScope, index)

	for i, cond := range lcond {
		subLabel := caseLabel(switchScope, index, i)
		var nextLabel string
		switch {
		case i < len(lcond)-1:
			nextLabel = caseLabel(switchScope, index, i+1)
		case index < maximum:
			nextLabel = caseLabel(switchScope, index+1, 0)
		default:
			nextLabel = endLabel
		}

		out = append(out, newLabel(subLabel, pos))

		var typ *vm.Type
		isNilCase := len(cond) == 1 && cond[0].Tok == lang.Ident && cond[0].Str == "nil"
		if !isNilCase {
			typ, _, err = p.parseTypeExpr(cond)
			if err != nil {
				return nil, err
			}
		}

		out = append(out, newIdent(tsName, pos))
		out = append(out, newToken(lang.TypeSwitchJump, nextLabel, pos, typ))

		if varName != "" {
			out = append(out, newIdent(vScoped, pos))
			out = append(out, newIdent(tsName, pos))
			if isMulti || isNilCase {
				out = append(out, newToken(lang.Assign, "", pos, 1))
			} else {
				out = append(out, newTypeAssert(typ, pos, 0))
				out = append(out, newToken(lang.Assign, "", pos, 1))
			}
		}

		if isMulti && i < len(lcond)-1 {
			out = append(out, newGoto(bodyLabel, pos))
		}
	}

	if isMulti {
		out = append(out, newLabel(bodyLabel, pos))
	}
	out = append(out, body...)
	if index != maximum {
		out = append(out, newGoto(endLabel, pos))
	}
	return out, nil
}

func (p *Parser) parseCaseClause(in Tokens, index, maximum int, condSwitch, prevFallthrough bool) (out Tokens, hasFallthrough bool, err error) {
	in = append(in, newSemicolon(in[len(in)-1].Pos)) // Force a ';' at the end of body clause.
	var conds, body Tokens
	tl := in.Split(lang.Colon)
	if len(tl) != 2 {
		return nil, false, errors.New("invalid case clause")
	}
	conds = tl[0][1:]
	pos := in[0].Pos

	// Pre-scan raw body for fallthrough before parsing statements.
	bodyRaw := tl[1]
	if fi := bodyRaw.Index(lang.Fallthrough); fi >= 0 {
		if index == maximum {
			return nil, false, errors.New("cannot fallthrough final case in switch")
		}
		if fi+2 < len(bodyRaw) {
			return nil, false, errors.New("fallthrough statement out of place")
		}
		hasFallthrough = true
		bodyRaw = bodyRaw[:fi]
	}
	if body, err = p.parseStmts(bodyRaw); err != nil {
		return nil, false, err
	}

	lcond := conds.Split(lang.Comma)
	isMulti := len(lcond) > 1
	bodyLabel := caseBodyLabel(p.scope, index)
	miss := p.scope + "e"
	if index < maximum {
		miss = caseLabel(p.scope, index+1, 0)
	}
	for i, cond := range lcond {
		if cond, err = p.parseExpr(cond, ""); err != nil {
			return nil, false, err
		}
		out = append(out, newLabel(caseLabel(p.scope, index, i), 0))
		if len(cond) > 0 {
			out = append(out, cond...)
			if condSwitch {
				out = append(out, newEqualSet(cond[0].Pos))
			}
			if isMulti && i < len(lcond)-1 {
				out = append(out, newJumpFalse(caseLabel(p.scope, index, i+1), cond[len(cond)-1].Pos))
				out = append(out, newGoto(bodyLabel, pos))
				continue
			}
			out = append(out, newJumpFalse(miss, cond[len(cond)-1].Pos))
		} else if condSwitch && index == maximum {
			// Default case in a condSwitch: drop the switch expression left on the stack.
			out = append(out, newDrop(pos))
		}
	}
	if isMulti || prevFallthrough {
		out = append(out, newLabel(bodyLabel, pos))
	}
	out = append(out, body...)
	if hasFallthrough {
		out = append(out, newGoto(caseBodyLabel(p.scope, index+1), pos))
	} else if index != maximum {
		out = append(out, newGoto(p.scope+"e", 0))
	}
	return out, hasFallthrough, err
}

type selectCase struct {
	dir      reflect.SelectDir
	chanToks Tokens
	sendToks Tokens
	body     Tokens
	valName  string
	okName   string
}

func (p *Parser) parseSelectCase(cl Tokens, index int, pos int, ci *selectCase) error {
	tl := cl.Split(lang.Colon)
	if len(tl) != 2 {
		return errors.New("invalid select case clause")
	}
	header := tl[0][1:]
	bodyToks := append(Tokens{}, tl[1]...)
	bodyToks = append(bodyToks, newSemicolon(pos))

	if len(header) == 0 {
		ci.dir = reflect.SelectDefault
		var err error
		ci.body, err = p.parseStmts(bodyToks)
		return err
	}

	arrowIdx := header.Index(lang.Arrow)
	defIdx := header.Index(lang.Define)
	assIdx := header.Index(lang.Assign)

	if arrowIdx > 0 && (defIdx < 0 || arrowIdx < defIdx) && (assIdx < 0 || arrowIdx < assIdx) {
		ci.dir = reflect.SelectSend
		var err error
		if ci.chanToks, err = p.parseExpr(header[:arrowIdx], ""); err != nil {
			return err
		}
		if ci.sendToks, err = p.parseExpr(header[arrowIdx+1:], ""); err != nil {
			return err
		}
		ci.body, err = p.parseStmts(bodyToks)
		return err
	}

	ci.dir = reflect.SelectRecv
	assignIdx := defIdx
	if assignIdx < 0 {
		assignIdx = assIdx
	}
	var chExpr Tokens
	if assignIdx >= 0 {
		isDefine := defIdx >= 0
		// Per-case scope so each case's variables are independent.
		p.pushScope("c" + strconv.Itoa(index))
		lhsToks := header[:assignIdx].Split(lang.Comma)
		for j, lt := range lhsToks {
			if len(lt) != 1 || lt[0].Tok != lang.Ident {
				p.popScope()
				return errors.New("invalid select recv assignment")
			}
			name := lt[0].Str
			if name == "_" {
				continue
			}
			var scopedName string
			if isDefine {
				if p.funcScope != "" {
					scopedName = p.addLocalVar(name)
				} else {
					scopedName = p.addGlobalVar(name)
				}
			} else {
				if _, sn, ok := p.Symbols.Get(name, p.scope); ok && sn != "" {
					scopedName = sn + "/" + name
				} else {
					scopedName = p.scopedName(name)
				}
			}
			if j == 0 {
				ci.valName = scopedName
			} else {
				ci.okName = scopedName
			}
		}
		chExpr = header[assignIdx+1:]
	} else {
		chExpr = header
	}
	if len(chExpr) == 0 || chExpr[0].Tok != lang.Arrow {
		if assignIdx >= 0 {
			p.popScope()
		}
		return errors.New("select recv case requires <-")
	}
	var err error
	if ci.chanToks, err = p.parseExpr(chExpr[1:], ""); err != nil {
		if assignIdx >= 0 {
			p.popScope()
		}
		return err
	}
	// Parse body after creating recv variables so the body can reference them.
	ci.body, err = p.parseStmts(bodyToks)
	if assignIdx >= 0 {
		p.popScope()
	}
	return err
}

// SelectCaseDesc describes one case of a select statement for the compiler.
type SelectCaseDesc struct {
	Dir     reflect.SelectDir
	ValName string // scoped name of recv value var ("" if none)
	OkName  string // scoped name of recv ok var ("" if none)
}

// parseSelect parses a select statement: select { case <-ch: ... case ch <- v: ... default: ... }.
func (p *Parser) parseSelect(in Tokens) (out Tokens, err error) {
	if len(in) < 2 || in[len(in)-1].Tok != lang.BraceBlock {
		return nil, errors.New("invalid select statement")
	}
	pos := in[0].Pos

	pendingLabel := p.takePendingLabel()
	defer p.pushBreakScope("select", pendingLabel, false)()

	clauses, err := p.scanBlock(in[len(in)-1].Token, true)
	if err != nil {
		return nil, err
	}
	caseClauses := clauses.SplitStart(lang.Case)

	moveDefaultLast(caseClauses)

	cases := make([]selectCase, len(caseClauses))
	for i, cl := range caseClauses {
		if err := p.parseSelectCase(cl, i, pos, &cases[i]); err != nil {
			return nil, err
		}
	}

	descs := make([]SelectCaseDesc, len(cases))
	for i := range cases {
		descs[i] = SelectCaseDesc{Dir: cases[i].dir, ValName: cases[i].valName, OkName: cases[i].okName}
		switch cases[i].dir {
		case reflect.SelectRecv:
			out = append(out, cases[i].chanToks...)
		case reflect.SelectSend:
			out = append(out, cases[i].chanToks...)
			out = append(out, cases[i].sendToks...)
		}
	}

	out = append(out, newToken(lang.Select, "", pos, descs))

	// Emit condSwitch-style dispatch on chosen index.
	nc := len(cases) - 1
	for i, c := range cases {
		out = append(out, newLabel(caseLabel(p.scope, i, 0), 0))
		if i < nc {
			out = append(out, newToken(lang.Int, strconv.Itoa(i), pos))
			out = append(out, newEqualSet(pos))
			out = append(out, newJumpFalse(caseLabel(p.scope, i+1, 0), pos))
		} else {
			// Last case: drop the chosen index left on the stack by prior EqualSet misses.
			out = append(out, newDrop(pos))
		}
		out = append(out, c.body...)
		if i < nc {
			out = append(out, newGoto(p.breakLabel, 0))
		}
	}

	out = append(out, newLabel(p.breakLabel, pos))
	return out, nil
}

func (p *Parser) parseLabel(in Tokens) (out Tokens, err error) {
	p.pendingLabel = p.labelName(in[0].Str)
	out = Tokens{newLabel(p.pendingLabel, in[0].Pos)}
	if len(in) > 2 {
		// Label followed by a compound statement (for, switch, ...) on the same line.
		stmtOut, err := p.parseStmt(in[2:])
		if err != nil {
			return out, err
		}
		out = append(out, stmtOut...)
	}
	return out, nil
}

func (p *Parser) parseReturn(in Tokens) (out Tokens, err error) {
	if l := len(in); l > 1 {
		for _, val := range in[1:].Split(lang.Comma) {
			if len(val) == 0 {
				continue
			}
			toks, err := p.parseExpr(val, "")
			if err != nil {
				return out, err
			}
			out = append(out, toks...)
		}
	} else {
		if l == 0 {
			in = Tokens{newReturn(0)} // Implicit return in functions with no return parameters.
		}
		// Bare return: push named return vars in declaration order (reverse of namedOut).
		for i := len(p.namedOut) - 1; i >= 0; i-- {
			out = append(out, newIdent(p.namedOut[i], in[0].Pos))
		}
	}

	s := p.function
	if s == nil || s.Type == nil {
		return nil, errors.New("return statement outside function")
	}
	in[0].Arg = []any{s.Type.Rtype.NumOut(), s.Type}
	out = append(out, in[0])
	return out, err
}
