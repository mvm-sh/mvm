package goparser

import (
	"fmt"

	"github.com/mvm-sh/mvm/lang"
)

func (p *Parser) parseAssign(in Tokens, aindex int) (out Tokens, err error) {
	rhs := in[aindex+1:].Split(lang.Comma)
	lhs := in[:aindex].Split(lang.Comma)
	define := in[aindex].Tok == lang.Define
	if len(rhs) > 1 && len(lhs) != len(rhs) {
		return out, p.errAt(in[aindex], "assignment mismatch: %d variables but %d values", len(lhs), len(rhs))
	}
	// `a[i], ok = f()` and similar: the multi-assign branch in
	// compiler.go assumes Var/LocalVar lhs and mishandles indexed/deref'd
	// lhs (the Index/Deref opcode runs at parse-emit time and consumes
	// the container/pointer). Desugar to N temps + N single-value
	// assigns so each lhs flows through Assign / IndexAssign / DerefAssign.
	isRangeRHS := len(rhs) == 1 && len(rhs[0]) > 0 && rhs[0][0].Tok == lang.Range
	if !define && len(rhs) == 1 && len(lhs) > 1 && lhsNeedsTemps(lhs) && !isRangeRHS {
		return p.parseAssignSingleRHSViaTemps(lhs, rhs[0], in[aindex])
	}
	// Assign-form range (`for x, y = range e`): the compiler's Range path can
	// only DEFINE loop vars, so desugar to a `:=` range over temps plus
	// per-iteration assigns to the targets (covers captured vars, a[i], *p).
	if !define && p.inForInit && isRangeRHS && !allBlankIdents(lhs) {
		return p.parseRangeAssignViaTemps(in, lhs, aindex)
	}
	if len(rhs) == 1 {
		var isRange bool
		// Track positions of LHS tokens for local fixup (one entry per lhs element).
		lhsPositions := make([]int, len(lhs))
		for j, e := range lhs {
			lhsPositions[j] = len(out)
			toks, err := p.parseExpr(e, "")
			if err != nil {
				return out, err
			}
			out = append(out, toks...)
		}
		toks, err := p.parseExpr(rhs[0], "")
		if err != nil {
			return out, err
		}
		if err := p.checkSingleRHSArity(in[aindex], len(lhs), toks); err != nil {
			return out, err
		}
		switch out[len(out)-1].Tok {
		case lang.Index:
			// Map elements cannot be assigned directly, but only through IndexAssign.
			out = out[:len(out)-1]
			out = append(out, toks...)
			out = append(out, newToken(lang.IndexAssign, "", in[aindex].Pos, len(lhs)))
		case lang.Deref:
			// Pointer deref cannot be assigned directly, use DerefAssign.
			out = out[:len(out)-1]
			out = append(out, toks...)
			out = append(out, newToken(lang.DerefAssign, "", in[aindex].Pos, len(lhs)))
		default:
			if len(lhs) == 2 {
				setCommaOkForm(toks)
			}
			out = append(out, toks...)
			isRange = out[len(out)-1].Tok == lang.Range
			if isRange {
				// Pass the number of values to set to range.
				// When all LHS variables are blank identifiers ("_ = range x"),
				// treat as "range x" (n=0) and remove the blank ident tokens.
				nVars := len(lhs)
				if !define && allBlankIdents(lhs) {
					for j := nVars - 1; j >= 0; j-- {
						pos := lhsPositions[j]
						out = append(out[:pos], out[pos+1:]...)
					}
					nVars = 0
				}
				out[len(out)-1].Arg = []any{nVars}
			} else {
				out = append(out, newToken(in[aindex].Tok, "", in[aindex].Pos, len(lhs)))
			}
		}
		// Register define symbols after parsing both LHS and RHS so that
		// the RHS can still reference outer-scope variables being shadowed.
		if define {
			for i, e := range lhs {
				if _, err := p.bindDefineLHS(in[aindex], e, out, lhsPositions[i]); err != nil {
					return out, err
				}
			}
			if p.funcScope != "" {
				switch {
				case isRange:
					p.inferRangeTypes(toks[:len(toks)-1], lhs, lhsPositions, out)
				case len(toks) > 0 && toks[len(toks)-1].Tok == lang.Call:
					// `a, b := f()` / `a := f()`: type each LHS local from the
					// call's return tuple so later passes (generic inference)
					// see them. Composite/other single-value RHS falls through.
					p.inferCallDefineTypes(toks, lhs, lhsPositions, out)
				case len(lhs) == 1:
					p.inferDefineType(toks, out[lhsPositions[0]].Str)
				}
			}
		}
		return out, err
	}
	return p.parseAssignMultiRHS(in, lhs, rhs, aindex, define)
}

// bindDefineLHS registers one `:=` LHS element and resets its token in out to
// a clean binding target: when the LHS name shadows an outer type (e.g.
// `poser := &poser{}`), parseExpr resolved the type onto the token (Arg
// carries the *vm.Type), which would make the compiler treat the define
// target as a type. Returns the scoped name. A non-ident LHS is an error.
func (p *Parser) bindDefineLHS(at Token, e Tokens, out Tokens, lhsPos int) (string, error) {
	if len(e) > 0 {
		at = e[0]
	}
	if len(e) != 1 || e[0].Tok != lang.Ident {
		return "", p.errAt(at, "non-name on left side of :=")
	}
	var name string
	if p.funcScope != "" {
		name = p.addOrRebindLocalVar(e[0].Str)
	} else {
		name = p.addOrRebindGlobalVar(e[0].Str)
	}
	out[lhsPos] = newIdent(name, out[lhsPos].Pos)
	return name, nil
}

func lhsNeedsTemps(lhs []Tokens) bool {
	for _, e := range lhs {
		if len(e) == 1 && e[0].Tok == lang.Ident {
			continue
		}
		return true
	}
	return false
}

func (p *Parser) parseAssignSingleRHSViaTemps(lhs []Tokens, rhsExpr Tokens, opTok Token) (out Tokens, err error) {
	pos := opTok.Pos
	tmpNames := make([]string, len(lhs))
	for i := range lhs {
		tmpNames[i] = p.addTempVar(fmt.Sprintf("_ma_%d_", i))
		out = append(out, newToken(lang.Ident, tmpNames[i], pos, 0))
	}
	rhsToks, err := p.parseExpr(rhsExpr, "")
	if err != nil {
		return out, err
	}
	if err := p.checkSingleRHSArity(opTok, len(lhs), rhsToks); err != nil {
		return out, err
	}
	// A 2-LHS assign to temps still needs the (value, ok) pair from a comma-ok RHS.
	if len(lhs) == 2 {
		setCommaOkForm(rhsToks)
	}
	out = append(out, rhsToks...)
	out = append(out, newToken(lang.Define, "", pos, len(lhs)))
	for i := range lhs {
		toks, err := p.parseExpr(lhs[i], "")
		if err != nil {
			return out, err
		}
		out = append(out, toks...)
		out = appendSingleAssign(out, newToken(lang.Ident, tmpNames[i], pos, 0), pos)
	}
	return out, nil
}

// parseRangeAssignViaTemps rewrites an assign-form range clause to a `:=`
// range over per-position temps (blanks stay blank); the `<lhs> = <temp>`
// assigns go to p.rangeAssign for parseFor to emit after the Next opcode.
func (p *Parser) parseRangeAssignViaTemps(in Tokens, lhs []Tokens, aindex int) (out Tokens, err error) {
	pos := in[aindex].Pos
	init := make(Tokens, 0, len(in)+2)
	var assigns Tokens
	for i, e := range lhs {
		name := "_"
		if !isBlankIdent(e) {
			name = fmt.Sprintf("_range_%d_", i)
			toks, err := p.parseExpr(e, "")
			if err != nil {
				return nil, err
			}
			assigns = append(assigns, toks...)
			// The recursive parseAssign registers the temp under its scoped key.
			assigns = appendSingleAssign(assigns, newToken(lang.Ident, p.scopedName(name), pos, 0), pos)
		}
		if i > 0 {
			init = append(init, newToken(lang.Comma, ",", pos))
		}
		init = append(init, newIdent(name, pos))
	}
	init = append(init, newToken(lang.Define, ":=", pos))
	init = append(init, in[aindex+1:]...)
	out, err = p.parseAssign(init, 2*len(lhs)-1)
	if err != nil {
		return nil, err
	}
	// Stash after the recursive parse: a for loop inside a closure in the
	// range subject must not consume this.
	p.rangeAssign = assigns
	return out, nil
}

func isBlankIdent(e Tokens) bool {
	return len(e) == 1 && e[0].Tok == lang.Ident && e[0].Str == "_"
}

func allBlankIdents(lhs []Tokens) bool {
	for _, e := range lhs {
		if !isBlankIdent(e) {
			return false
		}
	}
	return true
}

// checkSingleRHSArity rejects `a, b := <single-valued expr>`: a multi-LHS,
// single-RHS assignment requires the RHS to yield multiple values (a function
// call, a range, or a comma-ok form when lhs has 2 entries).
func (p *Parser) checkSingleRHSArity(opTok Token, nLHS int, rhsToks Tokens) error {
	if nLHS <= 1 || len(rhsToks) == 0 {
		return nil
	}
	last := rhsToks[len(rhsToks)-1].Tok
	if last == lang.Call || last == lang.Range {
		return nil
	}
	if nLHS == 2 && (last == lang.TypeAssert || last == lang.Index || last == lang.Arrow) {
		return nil
	}
	return p.errAt(opTok, "assignment mismatch: %d variables but 1 value", nLHS)
}

// setCommaOkForm flags a 2-value RHS (type assertion, map index, channel receive)
// to yield its (value, ok) pair for a 2-LHS assignment.
func setCommaOkForm(toks Tokens) {
	if len(toks) == 0 {
		return
	}
	switch toks[len(toks)-1].Tok {
	case lang.TypeAssert:
		toks[len(toks)-1].Arg[0] = 1
	case lang.Index:
		toks[len(toks)-1].Arg = []any{1}
	case lang.Arrow:
		toks[len(toks)-1].Arg = []any{1}
	}
}

func appendSingleAssign(out Tokens, src Token, pos int) Tokens {
	switch out[len(out)-1].Tok {
	case lang.Index:
		out = out[:len(out)-1]
		out = append(out, src, newToken(lang.IndexAssign, "", pos, 1))
	case lang.Deref:
		out = out[:len(out)-1]
		out = append(out, src, newToken(lang.DerefAssign, "", pos, 1))
	default:
		out = append(out, src, newToken(lang.Assign, "", pos, 1))
	}
	return out
}

func isBareNil(e Tokens) bool {
	return len(e) == 1 && e[0].Tok == lang.Ident && e[0].Str == "nil"
}

func (p *Parser) parseAssignMultiRHS(in Tokens, lhs, rhs []Tokens, aindex int, define bool) (out Tokens, err error) {
	// Non-define multi-RHS assignment (e.g. a, b = b, a) capture every RHS value into
	// a temporary first, then assign the temporaries to the LHS.
	if !define && len(rhs) > 1 {
		pos := in[aindex].Pos
		// Phase 1: evaluate each RHS into a temporary variable. A bare nil aliases
		// no LHS and has no type to define a temp from (`_swap_i_ := nil` is
		// untyped), so skip it here and assign it straight to the LHS in phase 2.
		tmpNames := make([]string, len(rhs))
		bareNil := make([]bool, len(rhs))
		for i, e := range rhs {
			if isBareNil(e) {
				bareNil[i] = true
				continue
			}
			tmpNames[i] = p.addTempVar(fmt.Sprintf("_swap_%d_", i))
			toks, err := p.parseExpr(e, "")
			if err != nil {
				return out, err
			}
			out = append(out, newToken(lang.Ident, tmpNames[i], pos, 0))
			out = append(out, toks...)
			out = append(out, newToken(lang.Define, "", pos, 1))
		}
		// Phase 2: assign from temporaries (or a bare nil) to LHS.
		for i := range lhs {
			toks, err := p.parseExpr(lhs[i], "")
			if err != nil {
				return out, err
			}
			out = append(out, toks...)
			src := newToken(lang.Ident, tmpNames[i], pos, 0)
			if bareNil[i] {
				src = newToken(lang.Ident, "nil", rhs[i][0].Pos, 0)
			}
			out = appendSingleAssign(out, src, pos)
		}
		return out, err
	}
	// Define multi-RHS (`a, b := e1, e2`): parse all LHS idents, then all RHS
	// in the pre-statement scope, and bind with a single Define so every RHS
	// is evaluated before any LHS is assigned (n, d := n/10, n%10).
	pos := in[aindex].Pos
	lhsPositions := make([]int, len(lhs))
	for i, e := range lhs {
		lhsPositions[i] = len(out)
		toks, err := p.parseExpr(e, "")
		if err != nil {
			return out, err
		}
		out = append(out, toks...)
	}
	rhsToks := make([]Tokens, len(rhs))
	for i, e := range rhs {
		toks, err := p.parseExpr(e, "")
		if err != nil {
			return out, err
		}
		rhsToks[i] = toks
		out = append(out, toks...)
	}
	out = append(out, newToken(lang.Define, "", pos, len(lhs)))
	// Register define symbols after parsing every RHS so the RHS can still
	// reference outer-scope variables being shadowed.
	for i, e := range lhs {
		name, err := p.bindDefineLHS(in[aindex], e, out, lhsPositions[i])
		if err != nil {
			return out, err
		}
		// Type the local from its RHS so a later generic call can infer
		// from it, matching the single-RHS define path. postfixType is pure.
		if p.funcScope != "" && e[0].Str != "_" {
			if sym := p.Symbols[name]; sym != nil && sym.Type == nil {
				if t, _ := p.postfixType(rhsToks[i]); t != nil {
					sym.Type = t
				}
			}
		}
	}
	return out, nil
}

var compoundAssignOp = map[lang.Token]lang.Token{
	lang.AddAssign:    lang.Add,
	lang.SubAssign:    lang.Sub,
	lang.MulAssign:    lang.Mul,
	lang.QuoAssign:    lang.Quo,
	lang.RemAssign:    lang.Rem,
	lang.AndAssign:    lang.And,
	lang.OrAssign:     lang.Or,
	lang.XorAssign:    lang.Xor,
	lang.ShlAssign:    lang.Shl,
	lang.ShrAssign:    lang.Shr,
	lang.AndNotAssign: lang.AndNot,
}

func indexCompoundAssign(in Tokens) (lang.Token, int) {
	for i, t := range in {
		if op, ok := compoundAssignOp[t.Tok]; ok {
			return op, i
		}
	}
	return 0, -1
}

func (p *Parser) parseCompoundAssign(in Tokens, aindex int, op lang.Token) (Tokens, error) {
	lhs := in[:aindex]
	rhs := in[aindex+1:]
	pos := in[aindex].Pos
	prefix, lhs, err := p.hoistCompoundLHS(lhs, pos)
	if err != nil {
		return nil, err
	}
	newIn := make(Tokens, 0, len(lhs)*2+len(rhs)+2)
	newIn = append(newIn, lhs...)
	newIn = append(newIn, newToken(lang.Assign, "", pos, 1))
	newIn = append(newIn, lhs...)
	newIn = append(newIn, newToken(op, "", pos))
	if len(rhs) > 1 {
		// Wrap rhs in parens to preserve precedence.
		newIn = append(newIn, newToken(lang.ParenBlock, tokensToBlock(rhs), rhs[0].Pos))
	} else {
		newIn = append(newIn, rhs...)
	}
	out, err := p.parseAssign(newIn, len(lhs))
	if err != nil {
		return nil, err
	}
	return append(prefix, out...), nil
}

func (p *Parser) parseIncDec(in Tokens) (Tokens, error) {
	last := in[len(in)-1]
	lhs := in[:len(in)-1]
	op := lang.Add
	if last.Tok == lang.Dec {
		op = lang.Sub
	}
	pos := last.Pos
	prefix, lhs, err := p.hoistCompoundLHS(lhs, pos)
	if err != nil {
		return nil, err
	}
	newIn := make(Tokens, 0, len(lhs)*2+3)
	newIn = append(newIn, lhs...)
	newIn = append(newIn, newToken(lang.Assign, "", pos, 1))
	newIn = append(newIn, lhs...)
	newIn = append(newIn, newToken(op, "", pos))
	newIn = append(newIn, newToken(lang.Int, "1", pos))
	out, err := p.parseAssign(newIn, len(lhs))
	if err != nil {
		return nil, err
	}
	return append(prefix, out...), nil
}

// hoistCompoundLHS rewrites an index-form lhs of a compound
// assignment or ++/-- so a side-effecting index evaluates exactly once.
func (p *Parser) hoistCompoundLHS(lhs Tokens, pos int) (prefix, newLHS Tokens, err error) {
	if len(lhs) < 2 || lhs[len(lhs)-1].Tok != lang.BracketBlock {
		return nil, lhs, nil
	}
	idxTok := lhs[len(lhs)-1]
	idxToks, err := p.scanBlock(idxTok.Token, false)
	if err != nil {
		return nil, lhs, err
	}
	// Only hoist a side-effecting single index.
	if idxToks.Index(lang.Colon) >= 0 || !p.tokensMayHaveSideEffect(idxToks) {
		return nil, lhs, nil
	}
	name := fmt.Sprintf("_lhsidx_%d", pos)
	defStmt := append(Tokens{newIdent(name, pos), newToken(lang.Define, ":=", pos)}, idxToks...)
	prefix, err = p.parseAssign(defStmt, 1)
	if err != nil {
		return nil, lhs, err
	}
	// Rebuild lhs with the index replaced by the temp.
	newLHS = make(Tokens, 0, len(lhs))
	newLHS = append(newLHS, lhs[:len(lhs)-1]...)
	newLHS = append(newLHS, newToken(lang.BracketBlock, name, pos))
	return prefix, newLHS, nil
}

func (p *Parser) tokensMayHaveSideEffect(toks Tokens) bool {
	for _, t := range toks {
		switch t.Tok {
		case lang.ParenBlock, lang.Arrow:
			return true
		case lang.BracketBlock, lang.BraceBlock:
			if sub, err := p.scanBlock(t.Token, false); err == nil && p.tokensMayHaveSideEffect(sub) {
				return true
			}
		}
	}
	return false
}
