package goparser

import (
	"errors"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// registerFunc registers a function or method declaration (Phase 1).
// Returns (true, nil) if the declaration is a generic template (fully handled).
func (p *Parser) registerFunc(toks Tokens) (bool, error) {
	if len(toks) < 3 || toks[0].Tok != lang.Func {
		return false, nil
	}

	var fname string
	var recvVarName string // raw receiver variable name (for methods)
	var sigToks Tokens     // tokens to pass to parseTypeExpr (signature without receiver)

	bi := toks.LastIndex(lang.BraceBlock)

	switch {
	case toks[1].Tok == lang.Ident:
		// Plain function: func Name(params) rettype { ... }
		fname = toks[1].Str
		if fname == "init" {
			return false, nil // init functions are handled in Phase 2 only.
		}
		// Generic function: func Name[T any](params) rettype { ... }
		if len(toks) > 2 && toks[2].Tok == lang.BracketBlock {
			params, err := p.parseTypeParamList(toks[2].Token)
			if err != nil {
				return false, err
			}
			// Parse the function signature so type params are resolved.
			// Register temporary placeholder types for the type parameters,
			// build sig tokens without the bracket block, and parse.
			for _, tp := range params {
				p.Symbols[tp.name] = &symbol.Symbol{Kind: symbol.Type, Name: tp.name, Type: &vm.Type{Name: tp.name, Rtype: vm.AnyRtype}}
			}
			sigEnd := bi
			if sigEnd <= 0 {
				sigEnd = len(toks)
			}
			sigToks := make(Tokens, 0, sigEnd-1)
			sigToks = append(sigToks, toks[:2]...)       // func Name
			sigToks = append(sigToks, toks[3:sigEnd]...) // (params) rettype (skip BracketBlock)
			genType, _, _, gerr := p.parseFuncSig(sigToks)
			for _, tp := range params {
				delete(p.Symbols, tp.name)
			}
			// Forward reference in the signature (e.g. return type names a
			// not-yet-declared generic): defer via ErrUndefined so the retry
			// loop re-registers this template once the referenced type exists.
			var eu ErrUndefined
			if errors.As(gerr, &eu) {
				return false, gerr
			}
			p.SymSet(p.scopedName(fname), &symbol.Symbol{
				Kind: symbol.Generic,
				Name: fname,
				Used: true,
				Type: genType,
				Data: &genericTemplate{
					name:       fname,
					typeParams: params,
					rawTokens:  toks,
					isFunc:     true,
				},
			})
			return true, nil
		}
		if bi > 0 {
			sigToks = toks[:bi]
		} else {
			sigToks = toks // Body-less function (e.g. runtime-linked).
		}

	case toks[1].Tok == lang.ParenBlock && len(toks) > 2 && toks[2].Tok == lang.Ident:
		// Method or anonymous function. Disambiguate: if toks[2] is a known
		// type and toks[3] is not a ParenBlock (param list), this is an anonymous
		// func with a named return type (e.g. func(int) T {...}), not a method.
		if s, _, ok := p.Symbols.Get(toks[2].Str, p.scope); ok && s.IsType() {
			if len(toks) < 4 || toks[3].Tok != lang.ParenBlock {
				return false, nil
			}
		}
		// Method: func (recv) Name(params) rettype { ... }
		recvr, err := p.scanBlock(toks[1].Token, false)
		if err != nil {
			return false, nil
		}
		// Generic method: receiver has type params (e.g. Box[T]).
		// Store as a method template on the generic type.
		if baseName, ok := recvGenericBaseName(recvr); ok {
			gs, _, gok := p.Symbols.Get(baseName, p.scope)
			if gok && gs.Kind == symbol.Generic {
				tmpl := gs.Data.(*genericTemplate)
				ptrRecv := false
				for _, t := range recvr {
					if t.Tok == lang.Mul {
						ptrRecv = true
						break
					}
				}
				tmpl.methods = append(tmpl.methods, &genericTemplate{
					name:       toks[2].Str,
					typeParams: tmpl.typeParams,
					rawTokens:  toks,
					isFunc:     true,
					ptrRecv:    ptrRecv,
				})
				return true, nil
			}
			// Base type has a bracketed receiver but isn't registered as generic
			// yet - likely a forward reference whose own declaration is still
			// pending (e.g. constraint referencing a not-yet-seen generic type).
			// Defer via ErrUndefined so the retry loop processes this after the
			// generic type declaration completes.
			return false, ErrUndefined{baseName}
		}
		typeName := recvTypeName(recvr)
		if typeName == "" {
			return false, nil
		}
		fname = typeName + "." + toks[2].Str
		if len(recvr) >= 2 && recvr[0].Tok == lang.Ident {
			recvVarName = recvr[0].Str
		}
		// Build signature tokens without receiver: [func, Name, params..., rettype].
		end := bi
		if end < 0 {
			end = len(toks) // Body-less method (e.g. runtime-linked).
		}
		sigToks = make(Tokens, 0, 1+end-2)
		sigToks = append(sigToks, toks[0])
		sigToks = append(sigToks, toks[2:end]...)

	default:
		return false, nil // Anonymous function.
	}

	s, _, ok := p.Symbols.Get(fname, p.scope)
	if ok && s.Type != nil {
		return false, nil
	}
	if !ok {
		s = &symbol.Symbol{Name: fname, Used: true, Index: symbol.UnsetAddr}
		key := p.scopedName(fname)
		p.SymSet(key, s)
	}
	typ, inNames, outNames, err := p.parseFuncSig(sigToks)
	if err != nil {
		if !ok {
			delete(p.Symbols, p.scopedName(fname))
		}
		return false, err
	}
	s.Kind = symbol.Func
	s.Type = typ
	s.RecvName = recvVarName
	s.InNames = inNames
	s.OutNames = outNames
	return false, nil
}

func recvTypeName(recvr Tokens) string {
	// Walk backwards: last Ident is the type name, preceded by * for pointer receivers.
	for i := len(recvr) - 1; i >= 0; i-- {
		if recvr[i].Tok == lang.Ident {
			if i > 0 && recvr[i-1].Tok == lang.Mul {
				return "*" + recvr[i].Str
			}
			return recvr[i].Str
		}
	}
	return ""
}

func (p *Parser) registerParamsFromSym(s *symbol.Symbol) {
	nparams := len(s.Type.Params)
	for i, name := range s.InNames {
		if name == "" {
			continue
		}
		p.addSymVar(i, nparams, p.scopedName(name), s.Type.Params[i], parseTypeIn)
	}
	// Reverse order to match frame slot assignment in parseParamTypes.
	for i := len(s.OutNames) - 1; i >= 0; i-- {
		name := s.OutNames[i]
		if name == "" {
			continue
		}
		p.addSymVar(i, len(s.OutNames), p.scopedName(name), &vm.Type{Rtype: s.Type.Rtype.Out(i)}, parseTypeOut)
	}
}

func (p *Parser) parseFunc(in Tokens) (out Tokens, err error) {
	var fname string

	switch in[1].Tok {
	case lang.Ident:
		// Skip generic function templates - they are instantiated on use.
		if s, _, ok := p.Symbols.Get(in[1].Str, p.scope); ok && s.Kind == symbol.Generic {
			return nil, nil
		}
		fname = in[1].Str
		if fname == "init" {
			fname = "#init" + strconv.Itoa(p.initNum)
			p.initNum++
			p.InitFuncs = append(p.InitFuncs, fname)
		}
	case lang.ParenBlock:
		// receiver, or anonymous function parameters.
		if t := in[2]; t.Tok == lang.Ident {
			// If in[2] is a known type and in[3] is not a ParenBlock (param list),
			// this is an anonymous func with a named return type (e.g. func(T) Ret{}).
			if s, _, ok := p.Symbols.Get(t.Str, p.scope); ok && s.IsType() {
				if len(in) < 4 || in[3].Tok != lang.ParenBlock {
					fname = "#f" + strconv.Itoa(p.clonum) // Generated closure symbol name.
					p.clonum++
					break
				}
			}
			// Method: derive fname from receiver type name.
			recvr, scanErr := p.scanBlock(in[1].Token, false)
			if scanErr != nil {
				return nil, scanErr
			}
			fname = recvTypeName(recvr) + "." + in[2].Str
		}
		if fname == "" {
			// Anonymous function whose return type starts with a keyword (e.g. func() func() int {}).
			fname = "#f" + strconv.Itoa(p.clonum)
			p.clonum++
		}
	default:
		fname = "#f" + strconv.Itoa(p.clonum) // Generated closure symbol name.
		p.clonum++
	}

	ofname := p.fname
	p.fname = fname
	ofunc := p.function
	funcScope := p.funcScope
	onamedOut := p.namedOut
	p.namedOut = nil
	s, _, ok := p.Symbols.Get(fname, p.scope)
	if !ok {
		s = &symbol.Symbol{Name: fname, Used: true, Index: symbol.UnsetAddr}
		key := fname
		if !strings.HasPrefix(fname, "#") {
			key = p.scope + fname
		}
		p.SymSet(key, s)
	}
	p.pushScope(fname)
	p.funcScope = p.scope
	// Local variable indices start at 1; index 0 is the frame header (prevFP).
	p.framelen[p.funcScope] = 1

	// For methods, register the receiver directly at the function scope
	// using cached info from Phase 1.
	if s.RecvName != "" {
		recvScoped := p.scope + "/" + s.RecvName
		s.FreeVars = []string{recvScoped}
		recvTypName, _, _ := strings.Cut(fname, ".")
		lookupName := strings.TrimPrefix(recvTypName, "*")
		if recvTypSym, _, ok := p.Symbols.Get(lookupName, p.scope); ok && recvTypSym.IsType() {
			recvTyp := recvTypSym.Type
			if strings.HasPrefix(recvTypName, "*") {
				recvTyp = vm.PointerTo(recvTyp)
			}
			p.addSymVar(0, 1, recvScoped, recvTyp, parseTypeRecv)
		}
	}

	defer func() {
		p.fname = ofname // TODO remove in favor of function.
		p.function = ofunc
		p.funcScope = funcScope
		p.namedOut = onamedOut
		p.popScope()
	}()

	out = Tokens{
		newGoto(fname+"_end", in[0].Pos), // Skip function definition.
		newLabel(fname, in[0].Pos),
	}

	bi := in.LastIndex(lang.BraceBlock)
	if bi < 0 {
		return out, errBody
	}

	if s.Type != nil {
		p.registerParamsFromSym(s)
	} else {
		typ, _, err := p.parseTypeExpr(in[:bi])
		if err != nil {
			return out, err
		}
		if typ == nil {
			return out, errors.New("could not determine function type")
		}
		s.Kind = symbol.Func
		s.Type = typ
	}
	p.function = s

	p.funcDepth++
	toks, err := p.parseTokBlock(in[bi].Token)
	p.funcDepth--
	if err != nil {
		return out, err
	}
	l := max(p.framelen[p.funcScope]-1, 0)
	out = append(out, newGrow(l, in[0].Pos))
	out = append(out, toks...)
	if out[len(out)-1].Tok != lang.Return {
		// Ensure that a return statement is always added at end of function.
		// TODO: detect missing or wrong returns.
		x, err := p.parseReturn(nil)
		if err != nil {
			return out, err
		}
		out = append(out, x...)
	}
	out = append(out, newLabel(fname+"_end", in[0].Pos))
	return out, err
}
