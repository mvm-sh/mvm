package goparser

import (
	"fmt"

	"github.com/mvm-sh/mvm/lang"
)

func (p *Parser) parseAssign(in Tokens, aindex int) (out Tokens, err error) {
	rhs := in[aindex+1:].Split(lang.Comma)
	lhs := in[:aindex].Split(lang.Comma)
	define := in[aindex].Tok == lang.Define
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
				if p.funcScope != "" {
					out[lhsPositions[i]].Str = p.addLocalVar(e[0].Str)
				} else {
					out[lhsPositions[i]].Str = p.addGlobalVar(e[0].Str)
				}
			}
			if p.funcScope != "" && len(lhs) == 1 {
				p.inferDefineType(toks, out[lhsPositions[0]].Str)
			}
			if p.funcScope != "" && isRange {
				p.inferRangeTypes(toks[:len(toks)-1], lhs, lhsPositions, out)
			}
		}
		return out, err
	}
	return p.parseAssignMultiRHS(in, lhs, rhs, aindex, define)
}

func (p *Parser) parseAssignMultiRHS(in Tokens, lhs, rhs []Tokens, aindex int, define bool) (out Tokens, err error) {
	// For plain-ident non-define assignments (e.g. a, b = b, a), use a batched approach:
	// emit all LHS first, then all RHS, then one Assign(n). This ensures all RHS values
	// are captured before any assignment takes effect, preserving swap semantics.
	if !define {
		allSimple := true
		for _, e := range lhs {
			if len(e) != 1 || e[0].Tok != lang.Ident {
				allSimple = false
				break
			}
		}
		if allSimple {
			for _, e := range lhs {
				toks, err := p.parseExpr(e, "")
				if err != nil {
					return out, err
				}
				out = append(out, toks...)
			}
			for _, e := range rhs {
				toks, err := p.parseExpr(e, "")
				if err != nil {
					return out, err
				}
				out = append(out, toks...)
			}
			out = append(out, newToken(lang.Assign, "", in[aindex].Pos, len(lhs)))
			return out, err
		}
	}
	// For multi-assignment with non-simple LHS (e.g. s[0], s[1] = s[1], s[0]),
	// capture all RHS values into temporaries first, then assign to LHS.
	// This ensures all RHS are evaluated before any LHS is modified.
	if !define && len(rhs) > 1 {
		pos := in[aindex].Pos
		// Phase 1: evaluate each RHS into a temporary variable.
		tmpNames := make([]string, len(rhs))
		for i, e := range rhs {
			tmpNames[i] = p.addTempVar(fmt.Sprintf("_swap_%d_", i))
			toks, err := p.parseExpr(e, "")
			if err != nil {
				return out, err
			}
			out = append(out, newToken(lang.Ident, tmpNames[i], pos, 0))
			out = append(out, toks...)
			out = append(out, newToken(lang.Define, "", pos, 1))
		}
		// Phase 2: assign from temporaries to LHS.
		for i := range lhs {
			toks, err := p.parseExpr(lhs[i], "")
			if err != nil {
				return out, err
			}
			out = append(out, toks...)
			rhsTok := newToken(lang.Ident, tmpNames[i], pos, 0)
			switch out[len(out)-1].Tok {
			case lang.Index:
				out = out[:len(out)-1]
				out = append(out, rhsTok)
				out = append(out, newToken(lang.IndexAssign, "", pos, 1))
			case lang.Deref:
				out = out[:len(out)-1]
				out = append(out, rhsTok)
				out = append(out, newToken(lang.DerefAssign, "", pos, 1))
			default:
				out = append(out, rhsTok)
				out = append(out, newToken(lang.Assign, "", pos, 1))
			}
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
					out[lhsPos].Str = p.addLocalVar(lt[0].Str)
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
