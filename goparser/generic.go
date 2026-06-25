package goparser

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/scan"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// elemKind classifies a single constraint element.
type elemKind int

const (
	elemAny          elemKind = iota // any / interface{}
	elemComparable                   // built-in comparable
	elemExact                        // arg.Rtype must equal typ.Rtype
	elemInterface                    // arg must Implement typ (method-set interface)
	elemApprox                       // ~T: arg.Kind() must match typ.Kind()
	elemTypeParamRef                 // arg must equal typeArgs[paramRef]
)

// constraintElem is one leaf of a constraint's disjunction.
type constraintElem struct {
	kind     elemKind
	typ      *vm.Type // Exact, Interface, Approx
	paramRef int      // TypeParamRef
}

// tpConstraint is a resolved generic type-parameter constraint. An argument
// satisfies the constraint if it matches any element in elems - a flat
// disjunction. Nested unions (including those embedded inside constraint
// interfaces like cmp.Ordered) are flattened at resolution time.
type tpConstraint struct {
	elems []constraintElem
	pos   int // byte offset of the first constraint token; resolved via p.Sources at error time
}

// typeParam represents a single generic type parameter.
type typeParam struct {
	name       string       // e.g. "T", "K", "V"
	constraint tpConstraint // resolved constraint (kind + payload)
}

// genericTemplate stores a generic function or type definition.
type genericTemplate struct {
	name       string             // original name (e.g. "Max", "Set")
	typeParams []typeParam        // ordered type parameter list
	rawTokens  Tokens             // entire declaration tokens (func or type)
	isFunc     bool               // true for generic functions, false for generic types
	ptrRecv    bool               // method template with pointer receiver (meaningful for methods only)
	methods    []*genericTemplate // methods defined on this generic type
	instances  []genericInstance  // monomorphizations, kept so methods attached after instantiation can still be emitted
	pkgPath    string             // full import path of the package where this template was declared; "" for main/REPL. Used during instantiation to resolve unqualified references in the body against the owning pkg's canonical keys.
}

type genericInstance struct {
	typeArgs []*vm.Type
	mname    string // mangled symbol name used for instance type registration
}

func (p *Parser) genericFuncSymbol(name string, params []typeParam, rawToks Tokens, genType *vm.Type) *symbol.Symbol {
	return &symbol.Symbol{
		Kind: symbol.Generic,
		Name: name,
		Used: true,
		Type: genType,
		Data: &genericTemplate{
			name:       name,
			typeParams: params,
			rawTokens:  rawToks,
			isFunc:     true,
			pkgPath:    p.importingPkg,
		},
	}
}

func beginsTypeConstraint(tok lang.Token, arrayAmbiguous bool) bool {
	switch tok {
	case lang.Ident, lang.Interface, lang.Tilde,
		lang.BracketBlock, lang.Map, lang.Chan, lang.Func, lang.Struct:
		return true
	case lang.Mul:
		return !arrayAmbiguous
	}
	return false
}

func (p *Parser) parseTypeParamList(bt scan.Token, arrayAmbiguous bool) ([]typeParam, error) {
	toks, err := p.scanBlock(bt, false)
	if err != nil {
		return nil, err
	}
	type rawPar struct {
		name  string
		ctoks Tokens
	}
	var raws []rawPar
	for _, seg := range toks.Split(lang.Comma) {
		if len(seg) == 0 {
			continue
		}
		if seg[0].Tok != lang.Ident {
			return nil, p.wrapAt(seg[0], ErrSyntax, "type parameter must start with a name, got %s", seg[0].Describe())
		}
		if len(seg) == 1 {
			// Bare ident shares the constraint with the next segment.
			// Go syntax: [K, V any] means K any, V any.
			raws = append(raws, rawPar{name: seg[0].Str})
			continue
		}
		// Disambiguate from array size expressions like [N + 1]: a constraint
		// begins a type expression, a size expression continues with an operator.
		if !beginsTypeConstraint(seg[1].Tok, arrayAmbiguous) {
			return nil, p.wrapAt(seg[1], ErrSyntax, "expected a type constraint, got %s", seg[1].Describe())
		}
		raws = append(raws, rawPar{name: seg[0].Str, ctoks: seg[1:]})
	}
	if len(raws) == 0 {
		return nil, p.wrapAt(Token{Token: bt}, ErrSyntax, "empty type parameter list")
	}
	// The last param must have an explicit constraint. A bare ident like [d]
	// is not a valid type parameter list (it's an array size expression).
	if raws[len(raws)-1].ctoks == nil {
		return nil, p.wrapAt(Token{Token: bt}, ErrSyntax, "type parameter list must end with a constraint")
	}
	// Propagate constraints backwards for shared-constraint syntax: [K, V any].
	for i := len(raws) - 2; i >= 0; i-- {
		if raws[i].ctoks == nil {
			raws[i].ctoks = raws[i+1].ctoks
		}
	}

	// Build type-param index so constraints referencing other params resolve
	// to a TypeParamRef rather than attempting to lookup the name as a type.
	tpIdx := make(map[string]int, len(raws))
	for i, r := range raws {
		tpIdx[r.name] = i
	}

	// Temporarily install placeholders for each type-param name so that
	// parseTypeExpr can resolve references to them inside composite constraints
	// like "~[]E" -- including when a name collides with a package-level symbol
	// (see bindTypeParamPlaceholders). Restore the prior symbols on exit.
	params := make([]typeParam, len(raws))
	for i, r := range raws {
		params[i].name = r.name
	}
	defer p.bindTypeParamPlaceholders(params)()

	for i, r := range raws {
		c, err := p.resolveConstraint(r.ctoks, tpIdx)
		if err != nil {
			return nil, err
		}
		params[i].constraint = c
	}
	return params, nil
}

func (p *Parser) checkConstraints(tmpl *genericTemplate, typeArgs []*vm.Type, callPos int) error {
	// name -> concrete arg, for substituting type params in a core-type
	// constraint element (e.g. `interface{ *P }`).
	tpArgs := make(map[string]*vm.Type, len(tmpl.typeParams))
	for i, tp := range tmpl.typeParams {
		if i < len(typeArgs) {
			tpArgs[tp.name] = typeArgs[i]
		}
	}
	for i, tp := range tmpl.typeParams {
		if err := p.checkConstraint(tp.constraint, typeArgs[i], typeArgs, tpArgs, callPos); err != nil {
			return err
		}
	}
	return nil
}

func (p *Parser) constraintError(c tpConstraint, arg *vm.Type, callPos int) error {
	pos := callPos
	if pos == 0 {
		pos = c.pos
	}
	return &constraintErr{
		loc: p.Sources.FormatPos(pos),
		pos: pos,
		msg: fmt.Sprintf("type %s does not satisfy constraint", typeArgName(arg)),
	}
}

type constraintErr struct {
	loc, msg string
	pos      int
}

func (e *constraintErr) Error() string {
	if e.loc != "" {
		return e.loc + ": " + e.msg
	}
	return e.msg
}

func (e *constraintErr) ErrPos() int { return e.pos }

func (p *Parser) checkConstraint(c tpConstraint, arg *vm.Type, typeArgs []*vm.Type, tpArgs map[string]*vm.Type, callPos int) error {
	for _, e := range c.elems {
		// Interface elements need a method-set check that sees interpreted methods,
		// which live in vm-level Methods rather than on the reflect Rtype.
		// checkConstraintElem can only reflect, so handle the interface case here where the parser's
		// symbol table is reachable.
		if e.kind == elemInterface {
			if e.typ == nil || p.argImplementsIface(arg, e.typ) {
				return nil
			}
			continue
		}
		// A core-type element naming a type param (`*P` in `[T interface{ *P }, P any]`)
		// is checked structurally with the param resolved to its arg, not by identity.
		if (e.kind == elemExact || e.kind == elemApprox) && shapeContainsTypeParam(e.typ, tpArgs) {
			if coreTypeArgMatches(e.typ, arg, tpArgs, e.kind == elemApprox) {
				return nil
			}
			continue
		}
		if checkConstraintElem(e, arg, typeArgs) {
			return nil
		}
	}
	return p.constraintError(c, arg, callPos)
}

func (p *Parser) argImplementsIface(arg, iface *vm.Type) bool {
	if arg == nil {
		return true
	}
	// A composite type argument parsed before materialization (e.g. *fs.PathError)
	// has a nil Rtype but carries its components, so derive the rtype now; the
	// reflect method-set checks below need it to see a bridged pointer's methods.
	// bindTypeParams materializes the same arg later, so this only moves it up.
	argRt := arg.Rtype
	if argRt == nil {
		argRt = vm.MaterializeRtype(arg)
	}
	// Native concrete type vs native interface: reflect can decide.
	if iface.Rtype != nil && iface.Rtype.NumMethod() > 0 && argRt != nil && argRt.Implements(iface.Rtype) {
		return true
	}
	iface.EnsureIfaceMethods()
	if len(iface.IfaceMethods) == 0 {
		return true // empty interface (any), or method set unknown: be lenient.
	}
	// Each required method must be present: as a native reflect method, a short-name
	// method symbol (symGet, scope-local), a symbol-table method (MethodByName also
	// resolves a foreign type's pkg-qualified key like "*example.com/msg.T.Tag"), or
	// in an interpreted interface arg's IfaceMethods.
	recvNames := argRecvTypeNames(arg)
	tsym := &symbol.Symbol{Kind: symbol.Type, Name: typeArgName(arg), Type: arg}
	for _, im := range iface.IfaceMethods {
		if argRt != nil && hasNativeMethod(argRt, im.Name) {
			continue
		}
		if p.hasMethodSym(recvNames, im.Name) {
			continue
		}
		if m, _ := p.Symbols.MethodByName(tsym, im.Name, p.Seg); m != nil {
			continue
		}
		if ifaceContainsMethod(arg, im.Name) {
			continue
		}
		return false
	}
	return true
}

func ifaceContainsMethod(t *vm.Type, name string) bool {
	if t == nil {
		return false
	}
	for _, im := range t.IfaceMethods {
		if im.Name == name {
			return true
		}
	}
	return false
}

func hasNativeMethod(rt reflect.Type, name string) bool {
	if rt == nil {
		return false
	}
	_, ok := rt.MethodByName(name)
	return ok
}

func argRecvTypeNames(arg *vm.Type) []string {
	name := typeArgName(arg)
	if name == "" {
		return nil
	}
	names := []string{name}
	if strings.HasPrefix(name, "*") {
		names = append(names, name[1:])
	}
	return names
}

func (p *Parser) hasMethodSym(recvNames []string, method string) bool {
	for _, rn := range recvNames {
		if s, _, ok := p.symGet(rn + "." + method); ok && s.Kind == symbol.Func {
			return true
		}
	}
	return false
}

func (p *Parser) resolveConstraint(toks Tokens, tpIdx map[string]int) (tpConstraint, error) {
	elems, err := p.resolveConstraintElems(toks, tpIdx)
	if err != nil {
		return tpConstraint{}, err
	}
	pos := 0
	if len(toks) > 0 {
		pos = toks[0].Pos
	}
	return tpConstraint{elems: elems, pos: pos}, nil
}

func (p *Parser) resolveConstraintElems(toks Tokens, tpIdx map[string]int) ([]constraintElem, error) {
	if len(toks) == 0 {
		return nil, fmt.Errorf("%w: empty constraint", ErrSyntax)
	}

	// Top-level union "A | B | C": concatenate each side's elements.
	if toks.Index(lang.Or) >= 0 {
		var out []constraintElem
		for _, seg := range toks.Split(lang.Or) {
			es, err := p.resolveConstraintElems(seg, tpIdx)
			if err != nil {
				return nil, err
			}
			out = append(out, es...)
		}
		return out, nil
	}

	// Approximate "~T": T must be a concrete type (single elemExact).
	if toks[0].Tok == lang.Tilde {
		inner, err := p.resolveConstraintElems(toks[1:], tpIdx)
		if err != nil {
			return nil, err
		}
		if len(inner) != 1 || inner[0].kind != elemExact {
			loc := p.Sources.FormatPos(toks[0].Pos)
			if loc == "" {
				return nil, fmt.Errorf("%w: ~ must prefix a type", ErrSyntax)
			}
			return nil, fmt.Errorf("%w: ~ must prefix a type (%s)", ErrSyntax, loc)
		}
		return []constraintElem{{kind: elemApprox, typ: inner[0].typ}}, nil
	}

	// Well-known identifier or type-param reference.
	if len(toks) == 1 && toks[0].Tok == lang.Ident {
		switch toks[0].Str {
		case "any":
			return []constraintElem{{kind: elemAny}}, nil
		case "comparable":
			return []constraintElem{{kind: elemComparable}}, nil
		}
		if idx, ok := tpIdx[toks[0].Str]; ok {
			return []constraintElem{{kind: elemTypeParamRef, paramRef: idx}}, nil
		}
	}

	// Type expression. A constraint interface with type elements (e.g.
	// cmp.Ordered) contributes one elem per member.
	typ, _, err := p.parseTypeExpr(toks)
	if err != nil {
		return nil, err
	}
	if typ.IsInterface() {
		if len(typ.TypeElems) > 0 {
			out := make([]constraintElem, len(typ.TypeElems))
			for i, e := range typ.TypeElems {
				kind := elemExact
				if e.Approx {
					kind = elemApprox
				}
				out[i] = constraintElem{kind: kind, typ: e.Type}
			}
			return out, nil
		}
		// A pure `comparable` constraint interface (no methods) checks the
		// built-in comparability. With methods (e.g. `interface { comparable;
		// error }`) the method-set interface element subsumes it; mvm's iface
		// constraints are checked leniently, so the comparable conjunct is not
		// separately enforced.
		if typ.Comparable && len(typ.IfaceMethods) == 0 {
			return []constraintElem{{kind: elemComparable}}, nil
		}
		return []constraintElem{{kind: elemInterface, typ: typ}}, nil
	}
	return []constraintElem{{kind: elemExact, typ: typ}}, nil
}

func (p *Parser) resolveTypeArgs(bt scan.Token) ([]*vm.Type, error) {
	toks, err := p.scanBlock(bt, false)
	if err != nil {
		return nil, err
	}
	var types []*vm.Type
	for _, seg := range toks.Split(lang.Comma) {
		if len(seg) == 0 {
			continue
		}
		typ, _, err := p.parseTypeExpr(seg)
		if err != nil {
			return nil, err
		}
		types = append(types, typ)
	}
	return types, nil
}

func (p *Parser) bindTypeParamSyms(params []typeParam, mk func(i int, tp typeParam) *symbol.Symbol) func() {
	type saved struct {
		key string
		sym *symbol.Symbol
		had bool
	}
	var prev []saved
	set := func(key string, sym *symbol.Symbol) {
		old, had := p.Symbols[key]
		prev = append(prev, saved{key, old, had})
		p.Symbols[key] = sym // mvm:symkey-ok: transient type-param binding, restored by the returned func
		p.Seg.Add(key)
	}
	for i, tp := range params {
		sym := mk(i, tp)
		if sym == nil {
			continue
		}
		set(tp.name, sym)
		if p.CompilingPkg != "" {
			set(QualifyName(p.CompilingPkg, tp.name), sym)
		}
		if p.importingPkg != "" {
			set(QualifyName(p.importingPkg, tp.name), sym)
		}
	}
	return func() {
		for _, s := range slices.Backward(prev) {
			if s.had {
				p.Symbols[s.key] = s.sym // mvm:symkey-ok: restoring the saved symbol
			} else {
				delete(p.Symbols, s.key)
				p.Seg.Del(s.key)
			}
		}
	}
}

func (p *Parser) bindTypeParams(params []typeParam, typeArgs []*vm.Type) func() {
	return p.bindTypeParamSyms(params, func(i int, tp typeParam) *symbol.Symbol {
		if i >= len(typeArgs) || typeArgs[i] == nil {
			return nil // missing/unresolved type arg: leave the name to resolve (or error) normally
		}
		ta := typeArgs[i]
		return &symbol.Symbol{
			Kind:  symbol.Type,
			Name:  tp.name,
			Index: symbol.UnsetAddr,
			Type:  ta,
			Value: typeTokenValue(ta),
			Used:  true,
		}
	})
}

func (p *Parser) bindTypeParamPlaceholders(params []typeParam) func() {
	return p.bindTypeParamSyms(params, func(_ int, tp typeParam) *symbol.Symbol {
		return &symbol.Symbol{Kind: symbol.Type, Name: tp.name, Type: &vm.Type{Name: tp.name, Rtype: vm.AnyRtype}}
	})
}

// maxInstDepth bounds the nesting depth of in-progress instantiations.
const maxInstDepth = 100

func (p *Parser) instantiate(tmpl *genericTemplate, typeArgs []*vm.Type, pos Token) (Tokens, string, error) {
	// A partial list (fewer args than params) reaches here only when the
	// caller couldn't infer the rest; a too-long list is always an error.
	if len(typeArgs) != len(tmpl.typeParams) {
		return nil, "", p.wrapAt(pos, ErrSyntax, "generic %s expects %d type argument(s), got %d", tmpl.name, len(tmpl.typeParams), len(typeArgs))
	}

	mname, reuse := p.instanceName(tmpl, typeArgs)
	if reuse {
		return nil, mname, nil // Already instantiated for this exact type set.
	}

	if p.instDepth >= maxInstDepth {
		// Report the template name, not mname: an unbounded-growth cycle makes
		// mname a multi-hundred-char mangling of the ever-growing type arg.
		return nil, "", p.wrapAt(pos, ErrSyntax, "instantiation cycle: %s", tmpl.name)
	}

	if err := p.checkConstraints(tmpl, typeArgs, pos.Pos); err != nil {
		return nil, "", err
	}

	out := append(Tokens(nil), tmpl.rawTokens...)

	// Rename the declaration and remove the type parameter bracket block.
	// Token index offset: func tokens have a leading `func` keyword.
	offset := 0
	if tmpl.isFunc {
		offset = 1
	}
	for i := range out {
		if i == offset && out[i].Tok == lang.Ident && out[i].Str == tmpl.name {
			out[i].Str = mname
		}
	}
	// Remove the BracketBlock at offset+1.
	if offset+1 < len(out) && out[offset+1].Tok == lang.BracketBlock {
		out = append(out[:offset+1], out[offset+2:]...)
	}

	return out, mname, nil
}

func (p *Parser) instanceName(tmpl *genericTemplate, typeArgs []*vm.Type) (name string, reuse bool) {
	base := mangledName(tmpl.name, typeArgs)
	if !tmpl.isFunc {
		// Generic types keep the plain name; instantiateMethod recomputes it
		// without a suffix, so a suffix here would orphan attached methods.
		if s, _, ok := p.Symbols.Get(base, ""); ok && s.Type != nil {
			// Reuse a finalized instance, or a still-placeholder one only while
			// its own body parses (self-ref). A placeholder left by a FAILED
			// instantiation must be rebuilt on retry, not reused.
			if !s.Type.Placeholder || p.instantiating[base] {
				return base, true
			}
		}
		return base, false
	}
	name = base
	for suffix := 0; ; suffix++ {
		if suffix > 0 {
			name = base + "$" + strconv.Itoa(suffix)
		}
		// Symbol existence (not the args map) gates reuse, so a rolled-back
		// instance is re-emitted despite its stale args entry.
		s, _, ok := p.Symbols.Get(name, "")
		if !ok || s.Type == nil {
			break
		}
		if prev, recorded := p.funcInstArgs[name]; recorded && !identicalTypeArgs(prev, typeArgs) {
			continue // same name, different type set
		}
		return name, true
	}
	if p.funcInstArgs == nil {
		p.funcInstArgs = map[string][]*vm.Type{}
	}
	p.funcInstArgs[name] = typeArgs
	return name, false
}

func identicalTypeArgs(a, b []*vm.Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameInstanceType(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameInstanceType(a, b *vm.Type) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Rtype != nil && b.Rtype != nil {
		return a.Rtype == b.Rtype
	}
	if a.Kind() != b.Kind() {
		return false
	}
	if a.Name != "" || b.Name != "" {
		return false
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Chan:
		return sameInstanceType(a.ElemType, b.ElemType)
	case reflect.Array:
		return a.Len() == b.Len() && sameInstanceType(a.ElemType, b.ElemType)
	case reflect.Map:
		return sameInstanceType(a.KeyType, b.KeyType) && sameInstanceType(a.ElemType, b.ElemType)
	}
	return true // structurally identical unnamed basic kinds
}

func (p *Parser) emitInstantiatedMethod(tmpl, methTmpl *genericTemplate, typeArgs []*vm.Type, mTypeName string) (bool, error) {
	methToks, err := p.instantiateMethod(tmpl, methTmpl, mTypeName)
	if err != nil || methToks == nil {
		return false, err
	}
	restore := p.bindTypeParams(tmpl.typeParams, typeArgs)
	defer restore()
	if _, err := p.registerFunc(methToks); err != nil {
		return false, err
	}
	fout, err := p.parseFunc(methToks)
	if err != nil {
		// Body parse failed (a deferred forward ref), but registerFunc already
		// published the symbol instantiateMethod's guard keys on. Drop it so the
		// retry re-parses; else the guard skips it forever -> method has no code.
		mik := methodInstanceKey(mTypeName, methTmpl)
		delete(p.Symbols, mik)
		p.Seg.Del(mik)
		return false, err
	}
	p.instanceDecls = append(p.instanceDecls, DeferredDecl{PkgPath: tmpl.pkgPath, Toks: fout})
	return true, nil
}

func (p *Parser) ensureTypeInstantiated(tmpl *genericTemplate, bt scan.Token) (string, error) {
	typeArgs, err := p.resolveTypeArgs(bt)
	if err != nil {
		return "", err
	}
	instToks, mname, err := p.instantiate(tmpl, typeArgs, Token{Token: bt})
	if err != nil {
		return "", err
	}
	if instToks != nil {
		savedScope := p.scope
		p.scope = ""
		// Resolve the body's package-qualified field types (e.g. unsafe.Pointer) against the template's owning package.
		// The func-body path gets this via its deferred-decl PkgPath tag.
		savedCompiling := p.CompilingPkg
		p.CompilingPkg = tmpl.pkgPath
		defer func() { p.CompilingPkg = savedCompiling }()
		restore := p.bindTypeParams(tmpl.typeParams, typeArgs)
		defer restore()
		p.instDepth++
		defer func() { p.instDepth-- }()
		// Mark in-progress so a self-ref in the body reuses the placeholder; see
		// instanceName.
		if p.instantiating == nil {
			p.instantiating = map[string]bool{}
		}
		p.instantiating[mname] = true
		defer delete(p.instantiating, mname)
		_, err = p.parseTypeLine(instToks)
		if err != nil {
			p.scope = savedScope
			return "", err
		}
		tmpl.instances = append(tmpl.instances, genericInstance{typeArgs: typeArgs, mname: mname})
		for _, methTmpl := range tmpl.methods {
			if _, err := p.emitInstantiatedMethod(tmpl, methTmpl, typeArgs, mname); err != nil {
				p.scope = savedScope
				return "", err
			}
		}
		p.scope = savedScope
	}
	return mname, nil
}

func (p *Parser) instantiatePendingMethods() (progress bool, err error) {
	savedScope := p.scope
	p.scope = ""
	defer func() { p.scope = savedScope }()
	for _, sym := range p.Symbols {
		if sym.Kind != symbol.Generic {
			continue
		}
		tmpl, ok := sym.Data.(*genericTemplate)
		if !ok || tmpl.isFunc {
			continue
		}
		for _, inst := range tmpl.instances {
			for _, methTmpl := range tmpl.methods {
				emitted, merr := p.emitInstantiatedMethod(tmpl, methTmpl, inst.typeArgs, inst.mname)
				if merr != nil {
					// A forward ref (ErrUndefined) is retryable: keep emitting other
					// pairs this pass, retry on the next. Other errors abort.
					var eu ErrUndefined
					if errors.As(merr, &eu) {
						if err == nil {
							err = merr
						}
						continue
					}
					return progress, merr
				}
				if emitted {
					progress = true
				}
			}
		}
	}
	return progress, nil
}

func methodInstanceKey(mTypeName string, methTmpl *genericTemplate) string {
	key := mTypeName + "." + methTmpl.name
	if methTmpl.ptrRecv {
		key = "*" + key
	}
	return key
}

func (p *Parser) instantiateMethod(typeTmpl, methTmpl *genericTemplate, mTypeName string) (Tokens, error) {
	// Guard: already instantiated.
	if _, _, ok := p.Symbols.Get(methodInstanceKey(mTypeName, methTmpl), ""); ok {
		return nil, nil
	}

	out := append(Tokens(nil), methTmpl.rawTokens...)

	// Collapse TypeName[Args] into the mangled name in the receiver ParenBlock
	// (the first ParenBlock, at index 1 after the func keyword).
	if len(out) > 1 && out[1].Tok == lang.ParenBlock {
		out[1].Str = p.stripRecvTypeParams(out[1].Str, typeTmpl.name, mTypeName)
	}

	return out, nil
}

func (p *Parser) stripRecvTypeParams(blockStr, origName, mangledName string) string {
	// Scan the full block string - expect a single ParenBlock.
	outerToks, err := p.Scan(blockStr, false)
	if err != nil || len(outerToks) != 1 || outerToks[0].Tok != lang.ParenBlock {
		return blockStr
	}

	paren := outerToks[0]
	inner := paren.Block()

	innerToks, err := p.Scan(inner, false)
	if err != nil {
		return blockStr
	}

	// Find origName Ident followed by BracketBlock and replace.
	var sb strings.Builder
	prev := 0
	for i, t := range innerToks {
		if t.Tok == lang.Ident && t.Str == origName && i+1 < len(innerToks) && innerToks[i+1].Tok == lang.BracketBlock {
			sb.WriteString(inner[prev:t.Pos])
			sb.WriteString(mangledName)
			bracketTok := innerToks[i+1]
			prev = bracketTok.Pos + len(bracketTok.Str)
		}
	}
	if prev == 0 {
		return blockStr // No change needed.
	}
	sb.WriteString(inner[prev:])

	// Reconstruct with outer parens.
	return blockStr[:paren.Beg] + sb.String() + blockStr[len(blockStr)-paren.End:]
}

func (p *Parser) inferTypeArgs(tmpl *genericTemplate, genSym *symbol.Symbol, callArgs scan.Token, prefix []*vm.Type) ([]*vm.Type, error) {
	argToks, err := p.scanBlock(callArgs, false)
	if err != nil {
		return nil, err
	}
	args := argToks.Split(lang.Comma)

	tpNames := make(map[string]bool, len(tmpl.typeParams))
	for _, tp := range tmpl.typeParams {
		tpNames[tp.name] = true
	}

	posErr := func(format string, a ...any) error {
		return p.errAt(Token{Token: callArgs}, format, a...)
	}

	if genSym.Type == nil {
		// Pre-registered by name (preRegisterGenericFuncs) but its signature is not
		// parsed yet. Defer via ErrUndefined so the call retries once it is, rather
		// than compiling a bare reference to the codeless template.
		return nil, p.undef(tmpl.name, Token{Token: callArgs})
	}

	params := genSym.Type.Params
	isVariadic := genSym.Type.IsVariadic() && len(params) > 0
	spread := len(argToks) > 0 && argToks[len(argToks)-1].Tok == lang.Ellipsis
	inferred := make(map[string]*vm.Type, len(tmpl.typeParams))
	for i, t := range prefix {
		if i < len(tmpl.typeParams) {
			inferred[tmpl.typeParams[i].name] = t
		}
	}
	// Untyped-constant args (e.g. slices.Index(b, '&')) are deferred: Go infers
	// type params from typed args and constraints first, using a constant's
	// default type only as a fallback. Binding E from '&' (default rune) up front
	// would shadow E=byte from the S ~[]E constraint and fail the check.
	type deferredArg struct {
		pType *vm.Type
		expr  Tokens
	}
	var deferred []deferredArg
	for i, argExpr := range args {
		if len(argExpr) == 0 {
			continue
		}
		var pType *vm.Type
		switch {
		case i < len(params)-1, !isVariadic && i < len(params):
			pType = params[i]
		case isVariadic:
			pType = params[len(params)-1]
			if !spread && pType.ElemType != nil {
				pType = pType.ElemType
			}
		default:
			continue
		}
		if !hasUnboundTypeParam(pType, tpNames, inferred) {
			continue
		}
		if isUntypedConstArg(argExpr) {
			deferred = append(deferred, deferredArg{pType, argExpr})
			continue
		}
		var argTyp *vm.Type
		if argExpr[0].Tok == lang.Func && argExpr[len(argExpr)-1].Tok == lang.BraceBlock {
			argTyp, _, _, _ = p.parseFuncSig(argExpr[:len(argExpr)-1])
		} else {
			argTyp = p.inferExprType(argExpr)
		}
		if argTyp == nil {
			// Can't type this arg (e.g. an untyped local func value); skip it
			// and let other args bind the type params. The final pass below
			// reports any param that stays unbound.
			continue
		}
		unifyTypeParam(pType, argTyp, tpNames, inferred)
	}

	// Second pass: for type parameters that never appear directly as a
	// parameter type (e.g. E in Equal[S ~[]E, E comparable](s1, s2 S)),
	// unpack any sibling's composite approx-constraint (~[]E, ~map[K]V)
	// against its inferred concrete type. Iterated to a fixed point so
	// that chains like [A ~[]B, B ~[]C, C any] resolve.
	for progress := len(inferred) < len(tmpl.typeParams); progress; {
		progress = false
		for _, tp := range tmpl.typeParams {
			if _, done := inferred[tp.name]; done {
				continue
			}
			for _, other := range tmpl.typeParams {
				if other.name == tp.name {
					continue
				}
				ot, ok := inferred[other.name]
				if !ok {
					continue
				}
				if t := unpackConstraint(other.constraint, tp.name, ot); t != nil {
					inferred[tp.name] = t
					progress = true
					break
				}
			}
		}
	}

	// Third pass: constraint type inference. A param that appears only in its own
	// constraint's core type (e.g. P in [T any, P *T], or P ObjectMarshalerPtr[T]
	// = interface{ *T; ... }) is constructed by substituting the inferred params
	// into that core type. Iterated to a fixed point for chained constraints.
	tpSet := make(map[string]*vm.Type, len(tmpl.typeParams))
	for _, tp := range tmpl.typeParams {
		tpSet[tp.name] = nil
	}
	for progress := len(inferred) < len(tmpl.typeParams); progress; {
		progress = false
		for _, tp := range tmpl.typeParams {
			if _, done := inferred[tp.name]; done {
				continue
			}
			shape := coreConstraintShape(tp.constraint)
			if shape == nil {
				continue
			}
			if t := constructFromShape(shape, tpSet, inferred); t != nil {
				inferred[tp.name] = t
				progress = true
			}
		}
	}

	// Fallback: any type param still unbound after the constraint pass takes a
	// deferred untyped-constant arg's default type (Go's rule when no typed arg
	// or constraint fixes it, e.g. F(5) -> int).
	for _, d := range deferred {
		if !hasUnboundTypeParam(d.pType, tpNames, inferred) {
			continue
		}
		if argTyp := p.inferExprType(d.expr); argTyp != nil {
			unifyTypeParam(d.pType, argTyp, tpNames, inferred)
		}
	}

	// Build ordered type args matching tmpl.typeParams.
	typeArgs := make([]*vm.Type, len(tmpl.typeParams))
	for i, tp := range tmpl.typeParams {
		t, ok := inferred[tp.name]
		if !ok {
			return nil, posErr("cannot infer type parameter %s", tp.name)
		}
		typeArgs[i] = t
	}
	return typeArgs, nil
}

func (p *Parser) inferExprType(toks Tokens) *vm.Type {
	postfix, err := p.parseExpr(toks, "")
	if err != nil || len(postfix) == 0 {
		return nil
	}
	typ, _ := p.postfixType(postfix)
	return typ
}

func (p *Parser) postfixType(in Tokens) (*vm.Type, int) {
	l := len(in) - 1
	if l < 0 {
		return nil, 0
	}
	t := in[l]
	id := t.Tok

	switch {
	case id == lang.Period:
		// Package-qualified value `pkg.Name` (not a call): type it so a bare
		// reference like sha256.New can be unified in generic inference.
		if l >= 1 && in[l-1].Tok == lang.Ident {
			ps := p.Symbols[in[l-1].Str]
			if as, _, ok := p.pkgAlias(in[l-1].Str, in[l-1].Pos); ok {
				ps = as
			}
			if ps != nil && ps.Kind == symbol.Pkg {
				member := t.Str[1:] // strip leading "."
				if mt := p.pkgMemberType(ps.PkgPath, member); mt != nil {
					return mt, 2
				}
				return nil, 0
			}
		}
		// Field selector: result type is the field type.
		leftTyp, ln := p.postfixType(in[:l])
		if leftTyp == nil {
			return nil, 0
		}
		fieldName := t.Str[1:] // Strip leading ".".
		// Auto-dereference pointer types for field access (Go: s.F works for *T).
		structTyp := leftTyp
		if structTyp.Kind() == reflect.Pointer {
			structTyp = structTyp.Elem()
		}
		if structTyp.Kind() == reflect.Struct {
			if ft := structTyp.FieldType(fieldName); ft != nil {
				return ft, 1 + ln
			}
		}
		if ms, _ := p.Symbols.MethodByName(&symbol.Symbol{Kind: symbol.Type, Name: leftTyp.Name, Type: leftTyp}, fieldName, p.Seg); ms != nil {
			return ms.Type, 1 + ln
		}
		// Interface receiver: methods live in IfaceMethods, not as method decls,
		// so MethodByName misses them. Return the declared method signature so a
		// method value `iface.M` types as its func -- callFuncType reads its return
		// tuple for a multi-value `a, b := iface.M()` define. Mirrors the Call case.
		if structTyp.IsInterface() {
			structTyp.EnsureIfaceMethods()
			for _, im := range structTyp.IfaceMethods {
				if im.Name != fieldName {
					continue
				}
				if im.Sig != nil {
					return im.Sig, 1 + ln
				}
				if im.Rtype != nil {
					return &vm.Type{Rtype: im.Rtype}, 1 + ln
				}
				break
			}
		}
		return nil, 0

	case id.IsBinaryOp():
		typ2, l2 := p.postfixType(in[:l])
		typ1, l1 := p.postfixType(in[:l-l2])
		if id.IsBoolOp() {
			return p.Symbols["bool"].Type, 1 + l1 + l2
		}
		// Arithmetic / bitwise: result type follows from operands. An untyped
		// literal adopts the other operand's type (`2 * x` is float64 when x
		// is), except shifts, whose type is always the left operand's.
		lit1 := l1 == 1 && in[l-l2-1].Tok.IsLiteral()
		lit2 := l2 == 1 && in[l-1].Tok.IsLiteral()
		if id != lang.Shl && id != lang.Shr && typ2 != nil && lit1 && !lit2 {
			return typ2, 1 + l1 + l2
		}
		if typ1 != nil {
			return typ1, 1 + l1 + l2
		}
		return typ2, 1 + l1 + l2

	case id.IsUnaryOp():
		inner, ln := p.postfixType(in[:l])
		if inner == nil {
			return nil, 0
		}
		switch id {
		case lang.Not:
			return p.Symbols["bool"].Type, 1 + ln
		case lang.Addr:
			return vm.SymPtr(inner), 1 + ln
		case lang.Deref:
			if inner.Kind() == reflect.Pointer {
				return inner.Elem(), 1 + ln
			}
		case lang.Arrow:
			if inner.Kind() == reflect.Chan {
				return inner.Elem(), 1 + ln
			}
		}
		return inner, 1 + ln

	case id.IsLiteral():
		switch id {
		case lang.Int:
			return p.Symbols["int"].Type, 1
		case lang.Float:
			return p.Symbols["float64"].Type, 1
		case lang.String:
			return p.Symbols["string"].Type, 1
		case lang.Char:
			return p.Symbols["int32"].Type, 1
		}
		return nil, 1

	case id == lang.Len:
		// Synthetic len(container) emitted for an omitted slice bound (a[r:]).
		return p.Symbols["int"].Type, 1

	case id == lang.Ident:
		// Consume the entire block as one operand, else a right-to-left arg walk derails on the body tokens.
		if l >= 1 && in[l-1].Tok == lang.Label && in[l-1].Str == t.Str+"_end" {
			endName := in[l-1].Str
			j := l - 1
			for j >= 0 && (in[j].Tok != lang.Goto || in[j].Str != endName) {
				j--
			}
			if j >= 0 {
				var ct *vm.Type
				if s, _, ok := p.Symbols.Get(t.Str, p.scope); ok {
					ct = symbol.Vtype(s)
				} else if s, ok := p.Symbols[p.anonFuncKey(t.Str)]; ok {
					// Anon closures are keyed per-package (anonFuncKey).
					ct = symbol.Vtype(s)
				}
				return ct, l - j + 1
			}
		}
		s, _, ok := p.Symbols.Get(t.Str, p.scope)
		if !ok {
			return nil, 1
		}
		return symbol.Vtype(s), 1

	case id == lang.Call:
		narg := t.Arg[0].(int)
		rest := in[:l]
		totalLen := 1
		var firstArgType *vm.Type // leftmost arg: the type for make/new
		for range narg {
			at, al := p.postfixType(rest)
			if al == 0 {
				return nil, 0
			}
			firstArgType = at
			totalLen += al
			rest = rest[:len(rest)-al]
		}
		// The function/type token precedes the arguments.
		if len(rest) == 0 {
			return nil, 0
		}
		fnTok := rest[len(rest)-1]
		totalLen++
		switch fnTok.Tok {
		case lang.Ident:
			switch fnTok.Str {
			case "make", "append":
				// make(T, ...) and append(s, ...) both yield their leftmost
				// arg's type (the type expr / the appended-to slice).
				return firstArgType, totalLen
			case "new":
				if firstArgType == nil {
					return nil, 0
				}
				return vm.SymPtr(firstArgType), totalLen
			case "len", "cap", "copy":
				return p.Symbols["int"].Type, totalLen
			case "min", "max":
				// min/max yield their operands' (shared) type.
				return firstArgType, totalLen
			case "real", "imag":
				// real/imag of a complex yield the matching float type.
				if firstArgType == nil {
					return nil, 0
				}
				switch firstArgType.Kind() {
				case reflect.Complex64:
					return p.Symbols["float32"].Type, totalLen
				case reflect.Complex128:
					return p.Symbols["float64"].Type, totalLen
				}
				return nil, 0
			case "complex":
				// complex of floats yields the matching complex type.
				if firstArgType == nil {
					return nil, 0
				}
				switch firstArgType.Kind() {
				case reflect.Float32:
					return p.Symbols["complex64"].Type, totalLen
				case reflect.Float64:
					return p.Symbols["complex128"].Type, totalLen
				}
				return nil, 0
			}
			s, _, ok := p.Symbols.Get(fnTok.Str, p.scope)
			if !ok {
				return nil, 0
			}
			if s.Kind == symbol.Type {
				return s.Type, totalLen
			}
			if s.Type != nil {
				return funcReturnType(s.Type), totalLen
			}
		case lang.Period:
			// Count the receiver/pkg-qualifier token before the selector, else the
			// enclosing operand split misaligns and binds a wrong type. See callFuncType.
			if len(rest) < 2 {
				break
			}
			member := fnTok.Str[1:] // strip leading "."
			if pre := rest[len(rest)-2]; pre.Tok == lang.Ident {
				ps := p.Symbols[pre.Str]
				if as, _, ok := p.pkgAlias(pre.Str, pre.Pos); ok {
					ps = as
				}
				if ps != nil && ps.Kind == symbol.Pkg {
					totalLen++ // the package ident token
					// Generic members stay unresolved: typing a nested generic
					// result mis-succeeds inference -> bad codegen / hang.
					if ft := p.pkgMemberType(ps.PkgPath, member); ft != nil && ft.IsFunc() {
						return funcReturnType(ft), totalLen
					}
					return nil, totalLen
				}
			}
			// Method call: count the receiver expr; resolve its return type if known.
			recvTyp, rl := p.postfixType(rest[:len(rest)-1])
			if rl == 0 {
				break
			}
			totalLen += rl
			if recvTyp != nil {
				st := recvTyp
				if st.Kind() == reflect.Pointer {
					st = st.Elem()
				}
				// Probe the pointer-receiver key (*T.m) too: a pointer receiver and an
				// addressable value both expose pointer methods, keyed "*T.m". MethodByName
				// falls back from *T to T, so this still finds value-receiver methods.
				lookupName := st.Name
				if lookupName != "" {
					lookupName = "*" + lookupName
				}
				if ms, _ := p.Symbols.MethodByName(&symbol.Symbol{Kind: symbol.Type, Name: lookupName, Type: st}, member, p.Seg); ms != nil && ms.Type != nil {
					return funcReturnType(ms.Type), totalLen
				}
				// Interface receiver: the method set lives in IfaceMethods, not as
				// method decls, so MethodByName misses it. Read the declared return
				// type from there (cmp.Compare(a.ID(), b.ID()) with a/b interface).
				if st.IsInterface() {
					st.EnsureIfaceMethods()
					for _, im := range st.IfaceMethods {
						if im.Name != member {
							continue
						}
						if im.Sig != nil {
							if rt := funcReturnType(im.Sig); rt != nil {
								return rt, totalLen
							}
						}
						if im.Rtype != nil && im.Rtype.NumOut() >= 1 {
							return &vm.Type{Rtype: im.Rtype.Out(0)}, totalLen
						}
						break
					}
				}
				// Native/bridged method: read the return type from the rtype, so a
				// chain like sig.Params().Variables() types as iter.Seq[*Var] and a
				// generic call (slices.Collect) can infer from it.
				if recvTyp.Rtype != nil {
					if m, ok := recvTyp.Rtype.MethodByName(member); ok && m.Type.NumOut() >= 1 {
						return &vm.Type{Rtype: m.Type.Out(0)}, totalLen
					}
				}
			}
			return nil, totalLen
		}
		return nil, totalLen

	case id == lang.Index:
		_, il := p.postfixType(in[:l]) // index expression
		containerTyp, cl := p.postfixType(in[:l-il])
		if containerTyp == nil {
			return nil, 0
		}
		switch containerTyp.Kind() {
		case reflect.Slice, reflect.Array, reflect.Map:
			return containerTyp.Elem(), 1 + il + cl
		case reflect.String:
			return p.Symbols["uint8"].Type, 1 + il + cl
		}
		return nil, 0

	case id == lang.Slice:
		// x[lo:hi(:max)]: slicing a slice/string keeps x's type; an array yields
		// []elem. Operands precede the op as [container, lo, hi(, max)].
		nIdx := 2
		if three, _ := t.Arg[0].(bool); three {
			nIdx = 3
		}
		rest := in[:l]
		total := 1
		for range nIdx {
			_, al := p.postfixType(rest)
			if al == 0 {
				return nil, 0
			}
			total += al
			rest = rest[:len(rest)-al]
		}
		containerTyp, cl := p.postfixType(rest)
		if containerTyp == nil {
			return nil, 0
		}
		total += cl
		switch containerTyp.Kind() {
		case reflect.Slice, reflect.String:
			return containerTyp, total
		case reflect.Array:
			return vm.SymSlice(containerTyp.Elem()), total
		}
		return nil, 0

	case id == lang.TypeAssert:
		// Type assertion: x.(T). The asserted type is stored in Arg[1].
		_, el := p.postfixType(in[:l]) // consume the expression being asserted
		if typ, ok := t.Arg[1].(*vm.Type); ok {
			return typ, 1 + el
		}
		return nil, 0

	case id == lang.Composite:
		// Composite literal: type is encoded in the token.
		typeName := t.Str
		if s, _, ok := p.Symbols.Get(typeName, p.scope); ok && s.Type != nil {
			return s.Type, 1
		}
		return nil, 1
	}
	return nil, 0
}

func (p *Parser) pkgMemberType(pkgPath, member string) *vm.Type {
	// Qualified symbol first: a package published by PublishCompiledPackage
	// stores an interpreted func's Value as its code address (an int), so the
	// Values branch below would mistype it.
	if qs, ok := p.Symbols[pkgPath+"."+member]; ok && qs.Kind != symbol.Generic {
		if t := symbol.Vtype(qs); t != nil {
			return t
		}
	}
	if pkg := p.Packages[pkgPath]; pkg != nil {
		if v, ok := pkg.Values[member]; ok {
			if rv := v.Reflect(); rv.IsValid() {
				return &vm.Type{Rtype: rv.Type()}
			}
		}
	}
	return nil
}
