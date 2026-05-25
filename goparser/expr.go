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
		case lang.Int, lang.Float, lang.Imag, lang.String:
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
					if s, _, ok := p.symGet(in[i+1].Str); ok && s.Kind != symbol.Type && s.Kind != symbol.Pkg {
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
			} else {
				// Rewrite Ident.Str to the pkg-qualified canonical key.
				pkg := p.importingPkg
				if pkg == "" {
					pkg = p.CompilingPkg
				}
				if pkg != "" {
					qk := QualifyName(pkg, t.Str)
					if qs, qok := p.Symbols[qk]; qok && (!ok || qs != s) {
						s = qs
						ok = true
						t.Str = qk
					}
				}
			}
			// Free variable detection: defined in an enclosing function scope.
			// Exclude variables defined in sub-scopes of the current function (e.g. for loops).
			isInnerScope := sc == p.funcScope || strings.HasPrefix(sc, p.funcScope+"/")
			if ok && s != nil && s.Kind == symbol.LocalVar && sc != "" && p.fname != "" && !isInnerScope {
				// Every enclosing closure up to the defining function must capture
				// transitively; otherwise comp's MkClosure can't bridge the chain and
				// emits GetLocal against the wrong frame.
				p.propagateCapture(t.Str, sc)
				s.Captured = true
			}
			if s != nil && s.Kind == symbol.Type {
				ctype = t.Str
				// Carry the resolved type so the compiler resolves it by identity
				// (its global slot) rather than re-looking up t.Str at compile time.
				if s.Type != nil {
					t.Arg = append(t.Arg, s.Type)
				}
				// Non-composite uses of a Type ident: T(x) (conversion),
				// T.Method (method expression), and struct-field-key
				// shadows (T:value inside a composite).
				// For these, the speculative Fnew the compiler would otherwise emit is
				// pure noise; tag the token so the comp Ident handler skips it.
				// T{...} is the only composite use, so absence of the tag still drives
				// Fnew emission for that path.
				if i+1 < lin {
					switch in[i+1].Tok {
					case lang.ParenBlock, lang.Period, lang.Colon:
						t.MarkNoFnew()
					}
				}
			}
			out = append(out, t)

		case lang.ParenBlock:
			// Implicit generic call: Name(args) where Name is a generic function.
			if i > 0 && !in[i-1].Tok.IsOperator() && len(out) > 0 && out[len(out)-1].Tok == lang.Ident {
				prevName := out[len(out)-1].Str
				if gs, _, ok := p.Symbols.Get(prevName, p.scope); ok && gs.Kind == symbol.Generic {
					tmpl := gs.Data.(*genericTemplate)
					if tmpl.isFunc {
						typeArgs, err := p.inferTypeArgs(tmpl, gs, t.Token, nil)
						if err != nil {
							return out, err
						}
						instToks, mname, err := p.instantiate(tmpl, typeArgs, t)
						if err != nil {
							return out, err
						}
						out = out[:len(out)-1] // remove the generic name ident
						if err := p.emitGenericFunc(tmpl, instToks, mname, t.Pos, &out, typeArgs); err != nil {
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
								typeArgs, err := p.inferTypeArgs(tmpl, gs, t.Token, nil)
								if err != nil {
									return out, err
								}
								instToks, mname, err := p.instantiate(tmpl, typeArgs, t)
								if err != nil {
									return out, err
								}
								out = out[:len(out)-1] // remove the pkg ident
								ops = ops[:len(ops)-1] // remove the Period operator
								if err := p.emitGenericFunc(tmpl, instToks, mname, t.Pos, &out, typeArgs); err != nil {
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
			if isUnaryCtx(i) {
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
					if typ, err := p.resolvePkgType(s, typeName, pkgTok); err == nil {
						// Use the FULL-path-qualified key to avoid package name collisions.
						ctype = s.PkgPath + "." + typeName
						if _, ok := p.Symbols[ctype]; !ok {
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
					name, tok := typeStr, in[i]
					if len(out) > 0 && out[len(out)-1].Tok == lang.Ident {
						name, tok = out[len(out)-1].Str, out[len(out)-1]
					}
					return out, p.undef(name, tok)
				}
				inner := sym.Type.Elem()
				// In a map literal, a `{...}` immediately followed by `:` is an
				// (elided-type) key, so infer its type from the key, not the value.
				if sym.Type.Rtype.Kind() == reflect.Map && i+1 < lin && in[i+1].Tok == lang.Colon {
					inner = sym.Type.Key()
				}
				ctype = p.registerType(inner, t.Pos, &out)
			}
			toks, sliceLen, err := p.parseComposite(t.Block(), ctype, t.Pos+t.Beg)
			out = append(out, toks...)
			if err != nil {
				return out, err
			}
			ops = append(ops, newComposite(ctype, t.Pos, sliceLen))

		case lang.BracketBlock:
			if isUnaryCtx(i) {
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
						typeArgs, err := p.resolveTypeArgs(t.Token)
						if err != nil {
							return out, err
						}
						if len(typeArgs) < len(tmpl.typeParams) && i+1 < lin && in[i+1].Tok == lang.ParenBlock {
							// Partial explicit list: infer the trailing params from
							// the call args, seeding the explicit prefix.
							typeArgs, err = p.inferTypeArgs(tmpl, gs, in[i+1].Token, typeArgs)
							if err != nil {
								return out, err
							}
						}
						instToks, mname, err := p.instantiate(tmpl, typeArgs, t)
						if err != nil {
							return out, err
						}
						if err := p.emitGenericFunc(tmpl, instToks, mname, t.Pos, &out, typeArgs); err != nil {
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
								typeArgs, err := p.resolveTypeArgs(t.Token)
								if err != nil {
									return out, err
								}
								if len(typeArgs) < len(tmpl.typeParams) && i+1 < lin && in[i+1].Tok == lang.ParenBlock {
									// Partial explicit list: infer the trailing params
									// from the call args, seeding the explicit prefix.
									typeArgs, err = p.inferTypeArgs(tmpl, gs, in[i+1].Token, typeArgs)
									if err != nil {
										return out, err
									}
								}
								instToks, mname, err := p.instantiate(tmpl, typeArgs, t)
								if err != nil {
									return out, err
								}
								if err := p.emitGenericFunc(tmpl, instToks, mname, t.Pos, &out, typeArgs); err != nil {
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

func (p *Parser) registerType(typ *vm.Type, pos int, out *Tokens) string {
	ctype := typ.String()
	key := ctype
	if typ.Name != "" {
		switch {
		case p.CompilingPkg != "":
			key = p.CompilingPkg + "." + typ.Name
		case p.importingPkg != "":
			key = p.importingPkg + "." + typ.Name
		}
	}
	if existing, ok := p.Symbols[key]; !ok || existing.Type != typ {
		p.SymAdd(symbol.UnsetAddr, key, vm.NewValue(typ.Rtype), symbol.Type, typ)
	}
	// Carry the resolved type on the emitted ident so the compiler resolves it by
	// identity (its global slot) rather than by re-looking up key at compile time.
	*out = append(*out, newIdent(key, pos, typ))
	return key
}

func (p *Parser) addTypeExpr(in Tokens, out *Tokens) (string, int, error) {
	typ, n, err := p.parseTypeExpr(in)
	if err != nil {
		return "", 0, err
	}
	return p.registerType(typ, in[0].Pos, out), n, nil
}

func (p *Parser) parseComposite(s, typ string, basePos int) (Tokens, int, error) {
	tokens, err := p.scanAt(basePos, s, false)
	if err != nil {
		return nil, 0, err
	}

	noColon := len(tokens) > 0 && tokens.Index(lang.Colon) == -1
	isStruct := false
	isSlice := false
	if !noColon {
		if sym := p.Symbols[typ]; sym != nil {
			switch {
			case sym.Type.IsStruct():
				// For struct composite literals, the LHS of `field: value`
				// is always a field NAME (not a value reference).
				isStruct = true
			case sym.Type.IsSlice():
				// Indexed-key slice literal: sliceLen must be highest index + 1
				// per Go spec, otherwise Fnew allocates a zero-length slice.
				isSlice = true
			}
		}
	}

	var result Tokens
	var sliceLen int
	curIdx := 0
	for i, sub := range tokens.Split(lang.Comma) {
		if len(sub) == 0 {
			continue
		}
		if isStruct && len(sub) >= 2 && sub[0].Tok == lang.Ident && sub[1].Tok == lang.Colon {
			fieldName := sub[0].Str
			valueToks, verr := p.parseExpr(sub[2:], typ)
			if verr != nil {
				return result, 0, verr
			}
			if len(valueToks) == 0 {
				continue
			}
			result = append(result, valueToks...)
			result = append(result, newFieldColon(fieldName, sub[1].Pos))
			continue
		}
		toks, err := p.parseExpr(sub, typ)
		if err != nil {
			return result, 0, err
		}
		if len(toks) == 0 {
			continue
		}
		if noColon {
			// Insert a numeric index key and a colon operator.
			result = append(result, newInt(i, toks[0].Pos))
			result = append(result, toks...)
			result = append(result, newColon(toks[0].Pos))
			sliceLen++
		} else {
			if isSlice && sub.Index(lang.Colon) == -1 {
				// Unkeyed element in a MIXED slice literal: synthesize
				// [curIdx, value..., colon] so the compiler emits IndexSet,
				// matching the shape parseExpr produces for keyed elements.
				result = append(result, newInt(curIdx, toks[0].Pos))
				result = append(result, toks...)
				result = append(result, newColon(toks[0].Pos))
			} else {
				result = append(result, toks...)
			}
			if isSlice {
				if ci := sub.Index(lang.Colon); ci > 0 {
					if k, ok := p.constIntKey(sub[:ci]); ok {
						curIdx = k
					}
				}
				curIdx++
				if curIdx > sliceLen {
					sliceLen = curIdx
				}
			}
		}
	}

	return result, sliceLen, nil
}

func (p *Parser) emitGenericFunc(tmpl *genericTemplate, instToks Tokens, mname string, pos int, out *Tokens, typeArgs []*vm.Type) error {
	if instToks == nil {
		*out = append(*out, newIdent(mname, pos))
		return nil
	}
	savedScope := p.scope
	savedCompilingPkg := p.CompilingPkg
	p.scope = ""
	if tmpl != nil && tmpl.pkgPath != "" {
		p.CompilingPkg = tmpl.pkgPath
	}
	// The instantiated body keeps the type-param names; bindTypeParams maps each
	// to its concrete type arg (by identity, not name) for the registerFunc +
	// parseFunc re-parse, restored on return by the deferred closure.
	if tmpl != nil {
		restore := p.bindTypeParams(tmpl.typeParams, typeArgs)
		defer restore()
	}
	p.instDepth++
	defer func() { p.instDepth-- }()
	if _, err := p.registerFunc(instToks); err != nil {
		p.scope = savedScope
		p.CompilingPkg = savedCompilingPkg
		return err
	}
	fout, err := p.parseFunc(instToks)
	p.scope = savedScope
	p.CompilingPkg = savedCompilingPkg
	if err != nil {
		return err
	}
	fid := fout[1]
	fid.Tok = lang.Ident
	// Route the instance body through pendingMethodDefs (drained at statement
	// end) rather than appending it inline. inferExprType discards its parseExpr
	// output, so a body emitted inline there would be lost while the instance
	// symbol stays registered - leaving an empty func slot that nil-derefs at
	// runtime when a generic call is an argument to another generic call (e.g.
	// cmp.Or(cmp.Compare(...))). The reference (fid) still goes inline.
	p.pendingMethodDefs = append(p.pendingMethodDefs, fout...)
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
		parts := tokens.Split(lang.Colon)
		for i, sub := range parts {
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
		result = append(result, newSlice(t.Pos, len(parts) == 3))
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
