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
		if fname == "_" {
			// Blank funcs are never callable; Go allows many per package
			// (stringer emits `func _()` per file). Don't register: no collision.
			return false, nil
		}
		if err := p.redeclaredAsImport(fname, toks[1]); err != nil {
			return false, err
		}
		// Generic function: func Name[T any](params) rettype { ... }
		if len(toks) > 2 && toks[2].Tok == lang.BracketBlock {
			params, err := p.parseTypeParamList(toks[2].Token)
			if err != nil {
				return false, err
			}
			// Parse the function signature so type params are resolved.
			restore := p.bindTypeParamPlaceholders(params)
			sigEnd := bi
			if sigEnd <= 0 {
				sigEnd = len(toks)
			}
			sigToks := make(Tokens, 0, sigEnd-1)
			sigToks = append(sigToks, toks[:2]...)       // func Name
			sigToks = append(sigToks, toks[3:sigEnd]...) // (params) rettype (skip BracketBlock)
			genType, _, _, gerr := p.parseFuncSig(sigToks)
			restore()
			// Forward reference in the signature (e.g. return type names a
			// not-yet-declared generic): defer via ErrUndefined so the retry
			// loop re-registers this template once the referenced type exists.
			var eu ErrUndefined
			if errors.As(gerr, &eu) {
				return false, gerr
			}
			p.SymSet(p.pkgKey(fname), p.genericFuncSymbol(fname, params, toks, genType))
			return true, nil
		}
		if bi > 0 {
			sigToks = toks[:bi]
		} else {
			sigToks = toks // Body-less function (e.g. runtime-linked).
		}

	case isMethodDecl(toks):
		// Method: func (recv) Name(params) rettype { ... }.
		recvr, err := p.scanBlock(toks[1].Token, false)
		if err != nil {
			return false, nil
		}
		// Generic method: receiver has type params (e.g. Box[T]).
		if baseName, ok := recvGenericBaseName(recvr); ok {
			gs, _, gok := p.symGet(baseName) // symGet, not Symbols.Get: resolves the importingPkg-qualified key for a shim/imported generic type
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
			return false, p.undef(baseName, toks[1])
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
		// A second declaration of this name within the same compilation unit is
		// a redeclaration (gc: "X redeclared in this block"). Erroring here stops
		// Phase 2 from emitting a duplicate function label, whose colliding jump
		// target hangs the VM. A symbol carried over from a prior Eval (REPL) is
		// not in this batch, so it falls through to the existing skip.
		if p.batchFuncDecls[key] {
			nameTok := toks[1]
			if nameTok.Tok == lang.ParenBlock { // method: name follows the receiver
				nameTok = toks[2]
			}
			return false, ErrRedeclared{Name: fname, Loc: p.Sources.FormatPos(nameTok.Pos), Pos: nameTok.Pos}
		}
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
	if p.batchFuncDecls != nil {
		p.batchFuncDecls[key] = true
	}
	return false, nil
}

// ErrRedeclared reports a second top-level declaration of a name within one compilation unit.
type ErrRedeclared struct {
	Name    string
	Loc     string
	Pos     int
	Through string // import path, when the clash is with an import (else "")
}

func (e ErrRedeclared) Error() string {
	msg := e.Name + " redeclared in this block"
	if e.Through != "" {
		msg = e.Name + " redeclared in this block: already declared through import of package " + strconv.Quote(e.Through)
	}
	if e.Loc != "" {
		return e.Loc + ": " + msg
	}
	return msg
}

// redeclaredAsImport reports a top-level decl of name in a file that also
// imports a package as name. Imports are file-scoped, so only the declaring
// file's own aliases count: a sibling file or the enclosing unit importing the
// same name is legal Go (e.g. package blas64 declares var blas64 while the
// test unit imports blas64).
func (p *Parser) redeclaredAsImport(name string, tok Token) error {
	if p.scope != "" {
		return nil
	}
	idx := p.Sources.SourceIndex(tok.Pos)
	if idx < 0 {
		return nil
	}
	if s := p.fileAliases[idx][name]; s != nil {
		return ErrRedeclared{Name: name, Loc: p.Sources.FormatPos(tok.Pos), Pos: tok.Pos, Through: s.PkgPath}
	}
	return nil
}

func (p *Parser) checkDeclNamesVsImport(decl Tokens) error {
	for _, g := range decl.Split(lang.Comma) {
		if len(g) == 0 || g[0].Tok != lang.Ident {
			continue
		}
		if err := p.redeclaredAsImport(g[0].Str, g[0]); err != nil {
			return err
		}
	}
	return nil
}

// ErrPos exposes the source offset so the diagnostic chokepoint can render a snippet.
func (e ErrRedeclared) ErrPos() int { return e.Pos }

func isMethodDecl(toks Tokens) bool {
	return len(toks) >= 4 && toks[1].Tok == lang.ParenBlock &&
		toks[2].Tok == lang.Ident && toks[3].Tok == lang.ParenBlock
}

func recvTypeName(recvr Tokens) string {
	// Walk backwards: last Ident is the type name, preceded by * for pointer receivers.
	for i, v := range slices.Backward(recvr) {
		if v.Tok == lang.Ident {
			if i > 0 && recvr[i-1].Tok == lang.Mul {
				return "*" + v.Str
			}
			return v.Str
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
	for i, name := range slices.Backward(s.OutNames) {
		if name == "" {
			continue
		}
		p.addSymVar(i, len(s.OutNames), p.scopedName(name), s.Type.ReturnType(i), parseTypeOut)
	}
}

// isInitFname reports a rewritten init func name ("#init" + digits).
func isInitFname(name string) bool {
	rest, ok := strings.CutPrefix(name, "#init")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// anonFuncKey returns the symbol-table key for an anonymous closure name,
// package-qualified like the compiler's qualifyLabel so the Label patch in
// generate finds the same Symbol the parser created.
func (p *Parser) anonFuncKey(fname string) string {
	if p.CompilingPkg == "" {
		return fname
	}
	return QualifyName(p.CompilingPkg, fname)
}

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

func (p *Parser) parseFunc(in Tokens) (out Tokens, err error) {
	var fname string

	switch in[1].Tok {
	case lang.Ident:
		// Skip generic function templates - they are instantiated on use.
		if s, _, ok := p.Symbols.Get(in[1].Str, p.scope); ok && s.Kind == symbol.Generic {
			return nil, nil
		}
		fname = in[1].Str
		if fname == "_" {
			return nil, nil // Blank funcs are never called: emit no body.
		}
		if fname == "init" {
			fname = "#init" + strconv.Itoa(p.initNum)
			p.initNum++
			p.InitFuncs = append(p.InitFuncs, fname)
		}
	case lang.ParenBlock:
		// receiver, or anonymous function parameters.
		if in[2].Tok == lang.Ident {
			if !isMethodDecl(in) {
				fname = p.anonFuncName()
				break
			}
			// Method: derive fname from the receiver type name.
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
	// Anon closures are keyed per-package ("<CompilingPkg>.#outer.funcN", see
	// anonFuncKey): same-named outer funcs in two packages produce the same
	// closure name, and a shared bare-key symbol would leak one package's
	// FreeVars into the other's closure. The direct probe keeps retry
	// idempotency without symGet's bare-key fallback. Rewritten init funcs
	// ("#initN") stay bare: interp resolves InitFuncs by that exact key, and
	// initNum is already unique across packages.
	var s *symbol.Symbol
	var ok bool
	if strings.HasPrefix(fname, "#") && !isInitFname(fname) {
		key := p.anonFuncKey(fname)
		if s, ok = p.Symbols[key]; !ok {
			s = &symbol.Symbol{Name: fname, Used: true, Index: symbol.UnsetAddr}
			p.SymSet(key, s)
		}
	} else if s, _, ok = p.symGet(fname); !ok {
		// Phase-2 deferred body of a top-level pkg func: symGet probes the
		// canonical pkgKey ("<CompilingPkg>.<fname>") before falling back to bare.
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
	p.clearDirectLocals(p.funcScope)

	// For methods, register the receiver directly at the function scope using cached info from Phase 1.
	if s.RecvName != "" {
		recvScoped := p.scope + "/" + s.RecvName
		s.FreeVars = []string{recvScoped}
		recvTypName, _, _ := strings.Cut(fname, ".")
		// symGet's CompilingPkg-aware probe finds the method's own pkg's type
		// even when a sibling import has shadowed the bare name.
		if recvTypSym, _, ok := p.symGet(strings.TrimPrefix(recvTypName, "*")); ok && recvTypSym.IsType() {
			recvTyp := recvTypSym.Type
			if strings.HasPrefix(recvTypName, "*") {
				recvTyp = vm.SymPtr(recvTyp)
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
		// This is a real func signature: parseTypeExpr's func case must register
		// the params as locals (it suppresses registration for func TYPES).
		p.regFuncSig = true
		typ, _, err := p.parseTypeExpr(in[:bi])
		p.regFuncSig = false
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
	// Promote captured named returns to heap cells at the prologue so a
	// capturing (deferred) closure shares the slot rather than a snapshot.
	var cellRet []int
	for _, name := range p.namedOut {
		if rs := p.Symbols[name]; rs != nil && rs.NeedsCell() {
			rs.CellSlot = true
			cellRet = append(cellRet, rs.Index)
		}
	}
	// Promote captured params to heap cells at the prologue too.
	var cellParams []int
	if s.Type != nil {
		for _, name := range s.InNames {
			if name == "" {
				continue
			}
			if ps := p.Symbols[p.scopedName(name)]; ps != nil && ps.NeedsCell() && !ps.CellSlot {
				ps.CellSlot = true
				cellParams = append(cellParams, ps.Index)
			}
		}
	}
	out = append(out, newGrow(l, in[0].Pos, cellRet, cellParams))
	// Zero-initialize named-return slots so an unassigned one returns a typed
	// zero, not an invalid Value{} (which breaks interface boxing at the caller).
	if n := len(p.namedOut); n > 0 {
		initVars := make([]string, n)
		initTypes := make([]*vm.Type, n)
		for j, name := range p.namedOut {
			initVars[j] = name
			initTypes[j] = s.Type.Returns[n-1-j]
		}
		out = append(out, p.zeroInitLocals(initVars, initTypes)...)
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
