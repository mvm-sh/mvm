package goparser

import (
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/scan"
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
			p.SymSet(p.pkgKey(fname), &symbol.Symbol{
				Kind: symbol.Generic,
				Name: fname,
				Used: true,
				Type: genType,
				Data: &genericTemplate{
					name:       fname,
					typeParams: params,
					rawTokens:  toks,
					isFunc:     true,
					pkgPath:    p.importingPkg,
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
		if s, _, ok := p.symGet(toks[2].Str); ok && s.IsType() {
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
					pkgPath:    tmpl.pkgPath,
				})
				return true, nil
			}
			// Base type has a bracketed receiver but isn't registered as generic
			// yet - likely a forward reference whose own declaration is still
			// pending (e.g. constraint referencing a not-yet-seen generic type).
			// Defer via ErrUndefined so the retry loop processes this after the
			// generic type declaration completes.
			return false, ErrUndefined{Name: baseName}
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

	// Top-level funcs/methods live at the canonical pkgKey ("<pkgPath>.<fname>"
	// inside an imported pkg, bare in main/REPL). Each pkg gets its own
	// canonical Symbol, so sibling-pkg same-named funcs (e.g.
	// golang.org/x/text/{language,internal/language,internal/language/compact}
	// all declare `func Make`) never collide on the bare key. The retry-loop
	// reentry guard then collapses to a simple "this Symbol already has a
	// Type" check at the canonical key. Anonymous closures (fname starts with
	// '#') and the special `init` rewrite stay scope-relative via scopedName.
	key := p.pkgKey(fname)
	s, ok := p.Symbols[key]
	if ok && s.Type != nil {
		return false, nil
	}
	if !ok {
		s = &symbol.Symbol{Name: fname, Used: true, Index: symbol.UnsetAddr}
		p.SymSet(key, s)
	}
	typ, inNames, outNames, err := p.parseFuncSig(sigToks)
	if err != nil {
		if !ok {
			delete(p.Symbols, key)
		}
		return false, err
	}
	s.Kind = symbol.Func
	s.Type = typ
	s.RecvName = recvVarName
	s.InNames = inNames
	s.OutNames = outNames
	// Cache the receiver's base *vm.Type now, while the bare receiver name still
	// resolves to this package's type. Phase 2 (body compilation) runs after all
	// imports, by which point a sibling import may have shadowed the bare name
	// (the unscoped-symbol-table problem); see Symbol.RecvType.
	if recvVarName != "" {
		recvTypName, _, _ := strings.Cut(fname, ".")
		if recvTypSym, _, ok := p.symGet(strings.TrimPrefix(recvTypName, "*")); ok && recvTypSym.IsType() {
			s.RecvType = recvTypSym.Type
		}
	}
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

// anonFuncName synthesizes a name for an anonymous closure. Inside a
// named outer function the form is "#<outer>.func<N>" with N a
// per-outer counter, matching Go's "<outer>.func<N>" stack-trace
// convention. Inside an outer that is itself an anonymous closure
// (p.fname starts with '#') the form drops the "func" prefix to
// yield "#<outer>.<N>", matching Go's "<outer>.func<N>.<M>" form
// for nested closures. Outside any function it falls back to
// "#f<clonum>" with the package-global counter. The leading '#' is
// the scope marker that distinguishes synthesized symbols from
// user-named methods of form "TypeName.MethodName".
func (p *Parser) anonFuncName() string {
	clo := p.clonum
	p.clonum++
	if p.fname != "" {
		p.funcN++
		nestedAnon := strings.HasPrefix(p.fname, "#")
		if nestedAnon {
			return "#" + p.fname + "." + strconv.Itoa(p.funcN)
		}
		return "#" + p.fname + ".func" + strconv.Itoa(p.funcN)
	}
	return "#f" + strconv.Itoa(clo)
}

// detectPassthrough recognises a func literal whose body is exactly
// `return TARGET(params...)` -- the args being the literal's declared
// params in order, with no other statements, conversions, or spread.
// Returns the qualified-name path of TARGET (e.g. ["regexp", "MatchString"])
// or nil if the pattern doesn't match. The compiler later checks that
// TARGET resolves to a native func of the exact same Go signature; if so,
// the closure value is replaced by a reference to TARGET, avoiding the
// per-call bridge overhead.
func trimTrailingSemi(ts Tokens) Tokens {
	for len(ts) > 0 && ts[len(ts)-1].Tok == lang.Semicolon {
		ts = ts[:len(ts)-1]
	}
	return ts
}

func (p *Parser) detectPassthrough(s *symbol.Symbol, bodyTok scan.Token) []string {
	body, err := p.scanBlock(bodyTok, false)
	if err != nil {
		return nil
	}
	body = trimTrailingSemi(body)
	if len(body) < 3 || body[0].Tok != lang.Return {
		return nil
	}
	if body[1].Tok != lang.Ident {
		return nil
	}
	path := []string{body[1].Str}
	i := 2
	for i+1 < len(body) && body[i].Tok == lang.Period && body[i+1].Tok == lang.Ident {
		path = append(path, body[i+1].Str)
		i += 2
	}
	if i != len(body)-1 || body[i].Tok != lang.ParenBlock {
		return nil
	}
	argToks, err := p.scanBlock(body[i].Token, false)
	if err != nil {
		return nil
	}
	argToks = trimTrailingSemi(argToks)
	if len(argToks) == 0 {
		if len(s.InNames) != 0 {
			return nil
		}
		return path
	}
	args := argToks.Split(lang.Comma)
	if len(args) != len(s.InNames) {
		return nil
	}
	for idx, arg := range args {
		arg = trimTrailingSemi(arg)
		if len(arg) != 1 || arg[0].Tok != lang.Ident || arg[0].Str != s.InNames[idx] {
			return nil
		}
	}
	return path
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
			if s, _, ok := p.symGet(t.Str); ok && s.IsType() {
				if len(in) < 4 || in[3].Tok != lang.ParenBlock {
					fname = p.anonFuncName()
					break
				}
			}
			// Method: derive fname from receiver type name. If the receiver
			// paren block has no Ident (e.g. empty `()`), this is actually an
			// anonymous func whose return type starts with a non-Type ident,
			// like `func() time.Time { ... }` where `time` is a Pkg, not a
			// Type. Treat it as anonymous instead of synthesizing a bogus
			// `<scope>.<ident>` symbol name.
			recvr, scanErr := p.scanBlock(in[1].Token, false)
			if scanErr != nil {
				return nil, scanErr
			}
			if rname := recvTypeName(recvr); rname != "" {
				fname = rname + "." + in[2].Str
			}
		}
		if fname == "" {
			// Anonymous function whose return type starts with a keyword (e.g. func() func() int {}).
			fname = p.anonFuncName()
		}
	default:
		fname = p.anonFuncName()
	}

	ofname := p.fname
	p.fname = fname
	ofuncN := p.funcN
	p.funcN = 0
	ofunc := p.function
	funcScope := p.funcScope
	onamedOut := p.namedOut
	p.namedOut = nil
	// Phase-2 deferred body of a top-level pkg func: look up via symGet which
	// probes the canonical pkgKey ("<CompilingPkg>.<fname>") before falling
	// back to bare. Nested funcs (scope non-empty) and anon closures (#-prefix)
	// follow lexical-scope semantics through symGet's normal Symbols.Get walk.
	s, _, ok := p.symGet(fname)
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
	// Two packages can both declare a top-level func of the same name, so
	// p.funcScope (bare fname) collides cross-pkg. Without this purge, a
	// stale LocalVar Symbol from the prior pkg's parse can be picked up by
	// addOrRebindLocalVar as a valid `:=` rebind target -- aliasing the new
	// pkg's local onto the wrong frame slot. Safe to drop: parse and compile
	// run back-to-back per decl (ParseOneStmt then generate), so the prior
	// decl's bytecode no longer needs its LocalVar entries.
	p.clearDirectLocals(p.funcScope)

	// For methods, register the receiver directly at the function scope
	// using cached info from Phase 1.
	if s.RecvName != "" {
		recvScoped := p.scope + "/" + s.RecvName
		s.FreeVars = []string{recvScoped}
		recvTypName, _, _ := strings.Cut(fname, ".")
		// Prefer the receiver base type resolved at signature time: by now a
		// sibling import may have shadowed the bare receiver name in p.Symbols.
		recvBase := s.RecvType
		if recvBase == nil {
			if recvTypSym, _, ok := p.symGet(strings.TrimPrefix(recvTypName, "*")); ok && recvTypSym.IsType() {
				recvBase = recvTypSym.Type
			}
		}
		if recvBase != nil {
			recvTyp := recvBase
			if strings.HasPrefix(recvTypName, "*") {
				recvTyp = vm.PointerTo(recvTyp)
			}
			p.addSymVar(0, 1, recvScoped, recvTyp, parseTypeRecv)
		}
	}

	defer func() {
		p.fname = ofname // TODO remove in favor of function.
		p.funcN = ofuncN
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
		// Recover param names so passthrough detection can match them to body args.
		// parseTypeExpr registers params as locals but discards the name list.
		if _, inNames, outNames, sigErr := p.parseFuncSig(in[:bi]); sigErr == nil {
			s.InNames = inNames
			s.OutNames = outNames
		}
	}
	if path := p.detectPassthrough(s, in[bi].Token); path != nil {
		s.PassthroughTarget = path
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
	// Zero-initialize named-return slots that need a typed zero reflect.Value.
	// Without this, Grow leaves the slot as an empty Value{} with an invalid
	// reflect.Value, breaking:
	//   - struct/array: `&t` falls into Addr's `!v.ref.IsValid()` branch which
	//     synthesizes `*interface{}`, breaking later field/index access.
	//   - slice/map: `append(t, x)` / `t[k] = v` panics in reflect because
	//     `result.Type()` is called on a zero Value.
	// Nilable kinds with no implicit ops on the zero (pointer, chan, iface,
	// func) work with the empty slot, so they don't need pre-init.
	// p.namedOut is right-to-left, so j=0 -> Returns[n-1].
	if n := len(p.namedOut); n > 0 {
		var initVars []string
		var initTypes []*vm.Type
		for j, name := range p.namedOut {
			typ := s.Type.Returns[n-1-j]
			switch typ.Rtype.Kind() {
			case reflect.Struct, reflect.Array, reflect.Slice, reflect.Map:
				initVars = append(initVars, name)
				initTypes = append(initTypes, typ)
			}
		}
		if len(initVars) > 0 {
			out = append(out, p.zeroInitLocals(initVars, initTypes)...)
		}
	}
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
