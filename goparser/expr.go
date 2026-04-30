package goparser

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// parseExpr transforms an infix expression into a postfix notation.
func (p *Parser) parseExpr(in Tokens, typeStr string) (out Tokens, err error) {
	var ops Tokens
	var ctype string

	popop := func() Token {
		l := len(ops) - 1
		t := ops[l]
		ops = ops[:l]
		if t.Tok.IsLogicalOp() {
			t.Tok = lang.Label // Implement conditional branching directly.
		}
		return t
	}

	flushops := func(minPrec int) {
		for len(ops) > 0 && p.precedence(ops[len(ops)-1]) >= minPrec {
			out = append(out, popop())
		}
	}

	addop := func(t Token) {
		// Binary operators are left-associative; unary are right-associative.
		if t.Tok.IsUnaryOp() {
			flushops(p.precedence(t) + 1)
		} else {
			flushops(p.precedence(t))
		}
		ops = append(ops, t)
	}

	// isUnaryCtx reports whether position i is in a unary-operator context,
	// i.e. the preceding token implies the next operator is unary, not binary.
	isUnaryCtx := func(i int) bool {
		return i == 0 || in[i-1].Tok.IsOperator() || in[i-1].Tok == lang.Colon || in[i-1].Tok == lang.Range
	}

	lin := len(in)
	for i := 0; i < lin; i++ {
		switch t := in[i]; t.Tok {
		case lang.Int, lang.Float, lang.String:
			out = append(out, t)

		case lang.Func:
			// Function as value (i.e closure).
			bi := in[i:].LastIndex(lang.BraceBlock)
			prevOut := out
			if out, err = p.parseFunc(in[i:]); err != nil {
				return out, err
			}
			fid := out[1]
			fid.Tok = lang.Ident
			out = append(prevOut, out...)
			out = append(out, fid)
			i += bi // advance past body; loop will increment to bi+1 (e.g. IIFE call args)

		case lang.Period:
			if i+1 < lin && in[i+1].Tok == lang.ParenBlock {
				// Type assertion: x.(T).
				flushops(p.precedence(t))
				btoks, err := p.scanBlock(in[i+1].Token, false)
				if err != nil {
					return out, err
				}
				typ, _, err := p.parseTypeExpr(btoks)
				if err != nil {
					return out, err
				}
				out = append(out, newTypeAssert(typ, t.Pos, 0))
				i++ // Skip following ParenBlock.
			} else {
				// Normal field selector. Use left-associative flushing so that
				// postfix chains like foo().Name evaluate the call before the access.
				t.Str += in[i+1].Str
				flushops(p.precedence(t))
				ops = append(ops, t)
				i++ // Skip over next ident.
			}

		case lang.Next:
			out = append(out, t)

		case lang.Range:
			addop(t)

		case lang.Colon:
			t.Str = typeStr
			addop(t)

		case lang.Mul:
			if isUnaryCtx(i) {
				if i+1 < lin && in[i+1].Tok == lang.Ident {
					// Known non-type identifier after * is a dereference.
					if s, _, ok := p.Symbols.Get(in[i+1].Str, p.scope); ok && s.Kind != symbol.Type && s.Kind != symbol.Pkg {
						t.Tok = lang.Deref
						addop(t)
						break
					}
				}
				if typ, n, err2 := p.parseTypeExpr(in[i:]); err2 == nil {
					ctype = typ.String()
					if typ.Rtype.Kind() == reflect.Pointer && !strings.HasPrefix(ctype, "*") {
						ctype = "*" + ctype
					}
					p.SymAdd(symbol.UnsetAddr, ctype, vm.NewValue(typ.Rtype), symbol.Type, typ)
					out = append(out, newIdent(ctype, t.Pos))
					i += n - 1
					break
				}
				t.Tok = lang.Deref
				addop(t)
			} else {
				addop(t)
			}

		case lang.Add, lang.And, lang.AndNot, lang.Equal, lang.Greater, lang.GreaterEqual, lang.Less, lang.LessEqual, lang.Not, lang.NotEqual, lang.Or, lang.Quo, lang.Rem, lang.Sub, lang.Shl, lang.Shr, lang.Xor:
			if isUnaryCtx(i) {
				t.Tok = lang.UnaryOp[t.Tok]
			}
			addop(t)

		case lang.Land:
			addop(t)
			xp := strconv.Itoa(p.labelCount[p.scope])
			p.labelCount[p.scope]++
			out = append(out, newJumpSetFalse(p.scope+"x"+xp, t.Pos))
			ops[len(ops)-1].Str = p.scope + "x" + xp

		case lang.Lor:
			addop(t)
			xp := strconv.Itoa(p.labelCount[p.scope])
			p.labelCount[p.scope]++
			out = append(out, newJumpSetTrue(p.scope+"x"+xp, t.Pos))
			ops[len(ops)-1].Str = p.scope + "x" + xp

		case lang.Ident:
			s, sc, ok := p.Symbols.Get(t.Str, p.scope)
			if ok && sc != "" {
				t.Str = sc + "/" + t.Str
			}
			// Free variable detection: defined in an enclosing function scope.
			// Exclude variables defined in sub-scopes of the current function (e.g. for loops).
			isInnerScope := sc == p.funcScope || strings.HasPrefix(sc, p.funcScope+"/")
			if ok && s != nil && s.Kind == symbol.LocalVar && sc != "" && p.fname != "" && !isInnerScope {
				if cloSym := p.Symbols[p.fname]; cloSym != nil {
					if cloSym.FreeVarIndex(t.Str) < 0 {
						cloSym.FreeVars = append(cloSym.FreeVars, t.Str)
						s.Captured = true
					}
				}
			}
			if s != nil && s.Kind == symbol.Type {
				ctype = t.Str
			}
			out = append(out, t)

		case lang.ParenBlock:
			// Implicit generic call: Name(args) where Name is a generic function.
			if i > 0 && !in[i-1].Tok.IsOperator() && len(out) > 0 && out[len(out)-1].Tok == lang.Ident {
				prevName := out[len(out)-1].Str
				if gs, _, ok := p.Symbols.Get(prevName, p.scope); ok && gs.Kind == symbol.Generic {
					tmpl := gs.Data.(*genericTemplate)
					if tmpl.isFunc {
						typeArgs, err := p.inferTypeArgs(tmpl, gs, t.Token)
						if err != nil {
							return out, err
						}
						instToks, mname, err := p.instantiate(tmpl, typeArgs, nil)
						if err != nil {
							return out, err
						}
						out = out[:len(out)-1] // remove the generic name ident
						if err := p.emitGenericFunc(instToks, mname, t.Pos, &out); err != nil {
							return out, err
						}
					}
				}
			}
			// Package-qualified implicit generic call: pkg.Generic(args).
			if len(out) > 0 && len(ops) > 0 && ops[len(ops)-1].Tok == lang.Period {
				pkgTok := out[len(out)-1]
				if pkgTok.Tok == lang.Ident {
					if ps := p.Symbols[pkgTok.Str]; ps != nil && ps.Kind == symbol.Pkg {
						memberName := ops[len(ops)-1].Str[1:] // Strip leading ".".
						qualifiedName := ps.PkgPath + "." + memberName
						if gs, ok := p.Symbols[qualifiedName]; ok && gs.Kind == symbol.Generic {
							tmpl := gs.Data.(*genericTemplate)
							if tmpl.isFunc {
								typeArgs, err := p.inferTypeArgs(tmpl, gs, t.Token)
								if err != nil {
									return out, err
								}
								instToks, mname, err := p.instantiate(tmpl, typeArgs, nil)
								if err != nil {
									return out, err
								}
								out = out[:len(out)-1] // remove the pkg ident
								ops = ops[:len(ops)-1] // remove the Period operator
								if err := p.emitGenericFunc(instToks, mname, t.Pos, &out); err != nil {
									return out, err
								}
							}
						}
					}
				}
			}

			toks, err := p.parseBlock(t, typeStr)
			if err != nil {
				return out, err
			}
			if i == 0 || in[i-1].Tok.IsOperator() {
				out = append(out, toks...)
			} else {
				flushops(p.precedence(newCall(0)))
				// func call: ensure that the func token in on the top of the stack, after args.
				bToks, _ := p.scanBlock(t.Token, false)
				spread := len(bToks) > 0 && bToks[len(bToks)-1].Tok == lang.Ellipsis
				narg := 0
				for _, sub := range bToks.Split(lang.Comma) {
					if len(sub) > 0 {
						narg++
					}
				}
				if spread {
					ops = append(ops, newCall(t.Pos, narg, 1))
				} else {
					ops = append(ops, newCall(t.Pos, narg))
				}
				out = append(out, toks...)
			}

		case lang.BraceBlock:
			// Check for package-qualified composite type: pkg.Type{}.
			if ctype == "" && len(out) > 0 && len(ops) > 0 && ops[len(ops)-1].Tok == lang.Period {
				pkgTok := out[len(out)-1]
				if s := p.Symbols[pkgTok.Str]; pkgTok.Tok == lang.Ident && s != nil && s.Kind == symbol.Pkg {
					typeName := ops[len(ops)-1].Str[1:] // Strip leading ".".
					if typ, err := p.resolvePkgType(s, typeName); err == nil {
						ctype = typ.String()
						if p.Symbols[ctype] == nil {
							p.SymAdd(symbol.UnsetAddr, ctype, vm.NewValue(typ.Rtype), symbol.Type, typ)
						}
						out[len(out)-1] = newIdent(ctype, pkgTok.Pos)
						ops = ops[:len(ops)-1] // Remove Period operator.
					}
				}
			}
			if ctype == "" {
				// Infer composite inner type from passed typeStr.
				sym := p.Symbols[typeStr]
				if sym == nil || sym.Type == nil {
					// Type not yet defined: look for preceding Ident in output.
					name := typeStr
					if len(out) > 0 && out[len(out)-1].Tok == lang.Ident {
						name = out[len(out)-1].Str
					}
					return out, ErrUndefined{Name: name}
				}
				ctype = p.registerType(sym.Type.Elem(), t.Pos, &out)
			}
			toks, sliceLen, err := p.parseComposite(t.Block(), ctype)
			out = append(out, toks...)
			if err != nil {
				return out, err
			}
			ops = append(ops, newComposite(ctype, t.Pos, sliceLen))

		case lang.BracketBlock:
			if i == 0 || in[i-1].Tok.IsOperator() || in[i-1].Tok == lang.Range || in[i-1].Tok == lang.Colon {
				// Array or slice type expression.
				elemTyp, n, err := p.parseTypeExpr(in[i:])
				if errors.Is(err, ErrEllipsisArray) {
					elemTyp, err = p.resolveEllipsisArray(elemTyp, in, i+n)
				}
				if err != nil {
					return out, err
				}
				ctype = p.registerType(elemTyp, t.Pos, &out)
				i += n - 1
				break
			}
			// Generic instantiation: Name[TypeArgs](...) or Name[TypeArgs]{...}.
			if len(out) > 0 && out[len(out)-1].Tok == lang.Ident {
				prevName := out[len(out)-1].Str
				if gs, _, ok := p.Symbols.Get(prevName, p.scope); ok && gs.Kind == symbol.Generic {
					tmpl := gs.Data.(*genericTemplate)
					out = out[:len(out)-1] // remove the generic name ident
					if tmpl.isFunc {
						typeArgs, typeArgSources, err := p.resolveTypeArgs(t.Token)
						if err != nil {
							return out, err
						}
						instToks, mname, err := p.instantiate(tmpl, typeArgs, typeArgSources)
						if err != nil {
							return out, err
						}
						if err := p.emitGenericFunc(instToks, mname, t.Pos, &out); err != nil {
							return out, err
						}
					} else {
						mname, err := p.ensureTypeInstantiated(tmpl, t.Token)
						if err != nil {
							return out, err
						}
						p.drainPendingMethods(&out)
						ctype = mname
						out = append(out, newIdent(mname, t.Pos))
					}
					break
				}
			}
			// Package-qualified generic: pkg.Generic[TypeArgs].
			if len(out) > 0 && len(ops) > 0 && ops[len(ops)-1].Tok == lang.Period {
				pkgTok := out[len(out)-1]
				if pkgTok.Tok == lang.Ident {
					if ps := p.Symbols[pkgTok.Str]; ps != nil && ps.Kind == symbol.Pkg {
						memberName := ops[len(ops)-1].Str[1:] // Strip leading ".".
						qualifiedName := ps.PkgPath + "." + memberName
						if gs, ok := p.Symbols[qualifiedName]; ok && gs.Kind == symbol.Generic {
							tmpl := gs.Data.(*genericTemplate)
							out = out[:len(out)-1] // remove the pkg ident
							ops = ops[:len(ops)-1] // remove the Period operator
							if tmpl.isFunc {
								typeArgs, typeArgSources, err := p.resolveTypeArgs(t.Token)
								if err != nil {
									return out, err
								}
								instToks, mname, err := p.instantiate(tmpl, typeArgs, typeArgSources)
								if err != nil {
									return out, err
								}
								if err := p.emitGenericFunc(instToks, mname, t.Pos, &out); err != nil {
									return out, err
								}
							} else {
								mname, err := p.ensureTypeInstantiated(tmpl, t.Token)
								if err != nil {
									return out, err
								}
								p.drainPendingMethods(&out)
								ctype = mname
								out = append(out, newIdent(mname, t.Pos))
							}
							break
						}
					}
				}
			}
			toks, err := p.parseBlock(t, typeStr)
			if err != nil {
				return out, err
			}
			if len(toks) == 0 {
				break
			}
			flushops(p.precedence(newIndex(t.Pos))) // left-associative: flush prior Index before next
			out = append(out, toks...)
			if toks[len(toks)-1].Tok != lang.Slice {
				ops = append(ops, newIndex(t.Pos))
			}

		case lang.Interface, lang.Struct:
			var n int
			if ctype, n, err = p.addTypeExpr(in[i:i+2], &out); err != nil {
				return out, err
			}
			i += n - 1

		case lang.Map:
			var n int
			if ctype, n, err = p.addTypeExpr(in[i:], &out); err != nil {
				return out, err
			}
			i += n - 1

		case lang.Chan:
			var n int
			if ctype, n, err = p.addTypeExpr(in[i:], &out); err != nil {
				return out, err
			}
			i += n - 1

		case lang.Arrow:
			// Unary channel receive: <-ch
			addop(t)

		case lang.Comment:

		case lang.Ellipsis:

		default:
			return out, fmt.Errorf("unexpected token: %v", t)
		}
	}
	for len(ops) > 0 {
		out = append(out, popop())
	}
	return out, err
}

// registerType registers typ in the symbol table and appends an Ident token to out.
// It returns the type name for use as composite type context.
func (p *Parser) registerType(typ *vm.Type, pos int, out *Tokens) string {
	ctype := typ.String()
	p.SymAdd(symbol.UnsetAddr, ctype, vm.NewValue(typ.Rtype), symbol.Type, typ)
	*out = append(*out, newIdent(ctype, pos))
	return ctype
}

// addTypeExpr parses a type expression, registers it in the symbol table,
// and appends the corresponding Ident token to out.
func (p *Parser) addTypeExpr(in Tokens, out *Tokens) (string, int, error) {
	typ, n, err := p.parseTypeExpr(in)
	if err != nil {
		return "", 0, err
	}
	return p.registerType(typ, in[0].Pos, out), n, nil
}

func (p *Parser) parseComposite(s, typ string) (Tokens, int, error) {
	tokens, err := p.scan(s, false)
	if err != nil {
		return nil, 0, err
	}

	noColon := len(tokens) > 0 && tokens.Index(lang.Colon) == -1
	var result Tokens
	var sliceLen int
	for i, sub := range tokens.Split(lang.Comma) {
		if len(sub) == 0 {
			continue
		}
		toks, err := p.parseExpr(sub, typ)
		if err != nil {
			return result, 0, err
		}
		if noColon {
			// Insert a numeric index key and a colon operator.
			result = append(result, newInt(i, toks[0].Pos))
			result = append(result, toks...)
			result = append(result, newColon(toks[0].Pos))
			sliceLen++
		} else {
			result = append(result, toks...)
		}
	}

	return result, sliceLen, nil
}

// emitGenericFunc registers and parses an instantiated generic function,
// appending the function definition and identifier to out.
// If instToks is nil (already instantiated), it appends the mangled name.
func (p *Parser) emitGenericFunc(instToks Tokens, mname string, pos int, out *Tokens) error {
	if instToks == nil {
		*out = append(*out, newIdent(mname, pos))
		return nil
	}
	savedScope := p.scope
	p.scope = ""
	if _, err := p.registerFunc(instToks); err != nil {
		p.scope = savedScope
		return err
	}
	fout, err := p.parseFunc(instToks)
	p.scope = savedScope
	if err != nil {
		return err
	}
	fid := fout[1]
	fid.Tok = lang.Ident
	*out = append(*out, fout...)
	*out = append(*out, fid)
	return nil
}

func (p *Parser) parseBlock(t Token, typ string) (result Tokens, err error) {
	tokens, err := p.scanBlock(t.Token, false)
	if err != nil {
		return tokens, err
	}

	if tokens.Index(lang.Colon) >= 0 {
		// Slice expression, a[low : high] or a[low : high : max].
		for i, sub := range tokens.Split(lang.Colon) {
			if i > 2 {
				return nil, errors.New("expected ']', found ':'")
			}
			if len(sub) == 0 {
				if i == 0 {
					result = append(result, newInt(0, tokens[0].Pos))
					continue
				} else if i == 2 {
					return nil, errors.New("final index required in 3-index slice")
				}
				result = append(result, newLen(1, tokens[0].Pos))
				continue
			}
			toks, err := p.parseExpr(sub, typ)
			if err != nil {
				return result, err
			}
			result = append(result, toks...)
		}
		result = append(result, newSlice(t.Pos))
		return result, err
	}

	for _, sub := range tokens.Split(lang.Comma) {
		toks, err := p.parseExpr(sub, typ)
		if err != nil {
			return result, err
		}
		result = append(result, toks...)
	}

	return result, err
}
