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
	if !define && len(rhs) == 1 && len(lhs) > 1 && lhsNeedsTemps(lhs) {
		return p.parseAssignSingleRHSViaTemps(lhs, rhs[0], in[aindex])
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
			if len(lhs) == 2 && len(toks) > 0 {
				switch toks[len(toks)-1].Tok {
				case lang.TypeAssert:
					toks[len(toks)-1].Arg[0] = 1
				case lang.Index:
					toks[len(toks)-1].Arg = []any{1}
				case lang.Arrow:
					toks[len(toks)-1].Arg = []any{1}
				}
			}
			out = append(out, toks...)
			isRange = out[len(out)-1].Tok == lang.Range
			if isRange {
				// Pass the number of values to set to range.
				// When all LHS variables are blank identifiers ("_ = range x"),
				// treat as "range x" (n=0) and remove the blank ident tokens.
				nVars := len(lhs)
				allBlank := !define
				for _, l := range lhs {
					if len(l) != 1 || l[0].Tok != lang.Ident || l[0].Str != "_" {
						allBlank = false
						break
					}
				}
				if allBlank {
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
				if len(e) != 1 || e[0].Tok != lang.Ident {
					continue
				}
				var name string
				if p.funcScope != "" {
					name = p.addOrRebindLocalVar(e[0].Str)
				} else {
					name = p.addGlobalVar(e[0].Str)
				}
				// Reset to a clean binding target. When the LHS name shadows an
				// outer type (e.g. `poser := &poser{}`), parseExpr resolved the
				// type onto this token (Arg carries the *vm.Type), which would
				// make the compiler treat the define target as a type.
				out[lhsPositions[i]] = newIdent(name, out[lhsPositions[i]].Pos)
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
	for i, e := range rhs {
		lhsPos := len(out)
		toks, err := p.parseExpr(lhs[i], "")
		if err != nil {
			return out, err
		}
		out = append(out, toks...)
		toks, err = p.parseExpr(e, "")
		if err != nil {
			return out, err
		}
		switch out[len(out)-1].Tok {
		case lang.Index:
			out = out[:len(out)-1]
			out = append(out, toks...)
			out = append(out, newToken(lang.IndexAssign, "", in[aindex].Pos, 1))
		case lang.Deref:
			out = out[:len(out)-1]
			out = append(out, toks...)
			out = append(out, newToken(lang.DerefAssign, "", in[aindex].Pos, 1))
		default:
			out = append(out, toks...)
			out = append(out, newToken(in[aindex].Tok, "", in[aindex].Pos, 1))
		}
		if define {
			lt := lhs[i]
			if len(lt) == 1 && lt[0].Tok == lang.Ident {
				if p.funcScope != "" {
					out[lhsPos].Str = p.addOrRebindLocalVar(lt[0].Str)
					// Type the local from its single-value RHS (toks) so a later
					// generic call can infer from it, matching the single-RHS
					// define path. postfixType is pure.
					if lt[0].Str != "_" {
						if sym := p.Symbols[out[lhsPos].Str]; sym != nil && sym.Type == nil {
							if t, _ := p.postfixType(toks); t != nil {
								sym.Type = t
							}
						}
					}
				} else {
					out[lhsPos].Str = p.addGlobalVar(lt[0].Str)
				}
			}
		}
	}
	return out, err
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
	// Build: lhs = lhs op (rhs)
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
	return p.parseAssign(newIn, len(lhs))
}

func (p *Parser) parseIncDec(in Tokens) (Tokens, error) {
	last := in[len(in)-1]
	lhs := in[:len(in)-1]
	op := lang.Add
	if last.Tok == lang.Dec {
		op = lang.Sub
	}
	pos := last.Pos
	newIn := make(Tokens, 0, len(lhs)*2+3)
	newIn = append(newIn, lhs...)
	newIn = append(newIn, newToken(lang.Assign, "", pos, 1))
	newIn = append(newIn, lhs...)
	newIn = append(newIn, newToken(op, "", pos))
	newIn = append(newIn, newToken(lang.Int, "1", pos))
	return p.parseAssign(newIn, len(lhs))
}
