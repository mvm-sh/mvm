package goparser

import (
	"fmt"
	"reflect"
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
	elemApprox                       // ~T: arg.Rtype.Kind() must match typ.Rtype.Kind()
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
}

func (p *Parser) parseTypeParamList(bt scan.Token) ([]typeParam, error) {
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
		// Disambiguate from array size expressions like [N + 1].
		if seg[1].Tok != lang.Ident && seg[1].Tok != lang.Interface && seg[1].Tok != lang.Tilde {
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
	// parseTypeExpr can resolve references to them inside composite
	// constraints like "~[]E". Restore the prior symbol on exit.
	saved := make(map[string]*symbol.Symbol, len(raws))
	for _, r := range raws {
		saved[r.name] = p.Symbols[r.name]
		p.Symbols[r.name] = &symbol.Symbol{ // mvm:symkey-ok: transient type-param placeholder, restored in defer below
			Kind: symbol.Type, Name: r.name,
			Type: &vm.Type{Name: r.name, Rtype: vm.AnyRtype},
		}
	}
	defer func() {
		for k, v := range saved {
			if v == nil {
				delete(p.Symbols, k)
			} else {
				p.Symbols[k] = v // mvm:symkey-ok: restoring the saved symbol
			}
		}
	}()

	params := make([]typeParam, len(raws))
	for i, r := range raws {
		c, err := p.resolveConstraint(r.ctoks, tpIdx)
		if err != nil {
			return nil, err
		}
		params[i] = typeParam{name: r.name, constraint: c}
	}
	return params, nil
}

func (p *Parser) checkConstraints(tmpl *genericTemplate, typeArgs []*vm.Type) error {
	for i, tp := range tmpl.typeParams {
		if err := p.checkConstraint(tp.constraint, typeArgs[i], typeArgs); err != nil {
			return err
		}
	}
	return nil
}

func (p *Parser) constraintError(c tpConstraint, arg *vm.Type) error {
	return &constraintErr{
		loc: p.Sources.FormatPos(c.pos),
		pos: c.pos,
		msg: fmt.Sprintf("type %s does not satisfy constraint", arg.Rtype),
	}
}

// constraintErr is a positioned constraint-satisfaction failure. ErrPos lets
// the diagnostic chokepoint (interp.Eval) render a source snippet at the
// instantiation site.
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

// checkConstraint passes if any element in c.elems matches arg. typeArgs
// carries the full set of concrete type arguments for the current
// instantiation so that a TypeParamRef element can resolve to its target.
func (p *Parser) checkConstraint(c tpConstraint, arg *vm.Type, typeArgs []*vm.Type) error {
	for _, e := range c.elems {
		// Interface elements (e.g. the `error` in `[E error]`) need a
		// method-set check that sees interpreted methods, which live in
		// vm-level Methods rather than on the reflect Rtype. checkConstraintElem
		// can only reflect, so handle the interface case here where the parser's
		// symbol table is reachable.
		if e.kind == elemInterface {
			if e.typ == nil || p.argImplementsIface(arg, e.typ) {
				return nil
			}
			continue
		}
		if checkConstraintElem(e, arg, typeArgs) {
			return nil
		}
	}
	return p.constraintError(c, arg)
}

// argImplementsIface reports whether type argument arg satisfies the interface
// constraint iface. Native concrete types are decided by reflect; interpreted
// types (whose methods are invisible to reflect.Implements) are checked by
// method name against the parser's registered method symbols.
func (p *Parser) argImplementsIface(arg, iface *vm.Type) bool {
	if arg == nil || arg.Rtype == nil {
		return true
	}
	// Native concrete type vs native interface: reflect can decide.
	if iface.Rtype != nil && iface.Rtype.NumMethod() > 0 && arg.Rtype.Implements(iface.Rtype) {
		return true
	}
	iface.EnsureIfaceMethods()
	if len(iface.IfaceMethods) == 0 {
		return true // empty interface (any), or method set unknown: be lenient.
	}
	// Each required method must be present either as a native method on arg's
	// reflect type (e.g. *fs.PathError.Error) or as a registered interpreted
	// method symbol (e.g. "*E.Error"). A user-defined (interpreted) interface
	// type argument has neither -- its method set lives in IfaceMethods -- and
	// is rejected here: instantiating a cross-package generic with a local
	// interpreted interface type arg is a separate, larger gap (see
	// [[project_generic_iface_constraint]]). Native interface args (e.g.
	// `error`) still pass via their reflect method set.
	recvNames := argRecvTypeNames(arg)
	for _, im := range iface.IfaceMethods {
		if hasNativeMethod(arg.Rtype, im.Name) {
			continue
		}
		if p.hasMethodSym(recvNames, im.Name) {
			continue
		}
		// An interpreted INTERFACE type argument carries its own method set in
		// IfaceMethods (set at parse, invisible to reflect and to method
		// symbols). It satisfies the constraint by method-set containment.
		if ifaceContainsMethod(arg, im.Name) {
			continue
		}
		return false
	}
	return true
}

// ifaceContainsMethod reports whether t is an interface type whose method set
// includes a method named name.
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

// hasNativeMethod reports whether reflect type rt has a method named name in
// its method set.
func hasNativeMethod(rt reflect.Type, name string) bool {
	if rt == nil {
		return false
	}
	_, ok := rt.MethodByName(name)
	return ok
}

// argRecvTypeNames returns the receiver type names whose method symbols make up
// arg's method set. A pointer argument also includes the value-receiver methods
// of its base type (matching Go's method-set rules).
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

// hasMethodSym reports whether any of recvNames has a registered method named
// method (key shape "<recvType>.<method>", e.g. "*E.Error"), resolved through
// symGet so pkg-qualified and pointer-method keys both match.
func (p *Parser) hasMethodSym(recvNames []string, method string) bool {
	for _, rn := range recvNames {
		if s, _, ok := p.symGet(rn + "." + method); ok && s.Kind == symbol.Func {
			return true
		}
	}
	return false
}

// resolveConstraint turns raw constraint tokens into a resolved constraint.
// tpIdx maps names of type parameters in the enclosing list to their index so
// that e.g. "~[]E" with "E" another type param resolves correctly.
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

// resolveConstraintElems returns the flat disjunction of leaf elements that
// satisfy the constraint expressed by toks. Nested unions - including those
// embedded inside constraint interfaces like cmp.Ordered - are flattened.
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

// resolveTypeArgs parses the contents of a bracket block as concrete type arguments.
// E.g. "[int, string]" -> []*vm.Type{intType, stringType}.
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

// bindTypeParams installs a transient Type symbol mapping each type-parameter
// name to its concrete type argument, returning a restore func the caller defers.
// The instantiated body keeps the type-param names (it is re-scanned from block
// text), so every type-position reference resolves to the concrete type and
// carries its identity to the compiler.
//
// A type param shadows a package-level type of the same name (Go scoping: it is
// local to the generic body). symGet, under CompilingPkg/importingPkg, prefers a
// canonical "<pkg>.<name>" type over a bare binding, so the binding is installed
// at the bare name AND at those qualified keys; otherwise an instance of e.g.
// slices' MinFunc[S ~[]E] would resolve S to a test's `type S struct` instead of
// the concrete slice. Save/restore nests safely under recursive instantiation: an
// inner restore re-exposes the outer instance's binding.
func (p *Parser) bindTypeParams(params []typeParam, typeArgs []*vm.Type) func() {
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
	}
	for i, tp := range params {
		if i >= len(typeArgs) {
			break
		}
		ta := typeArgs[i]
		if ta == nil || ta.Rtype == nil {
			continue // unresolved type arg: leave the name to resolve (or error) normally rather than panic in NewValue
		}
		sym := &symbol.Symbol{
			Kind:  symbol.Type,
			Name:  tp.name,
			Index: symbol.UnsetAddr,
			Type:  ta,
			Value: vm.NewValue(ta.Rtype),
			Used:  true,
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
		for i := len(prev) - 1; i >= 0; i-- {
			if s := prev[i]; s.had {
				p.Symbols[s.key] = s.sym // mvm:symkey-ok: restoring the saved symbol
			} else {
				delete(p.Symbols, s.key)
			}
		}
	}
}

// maxInstDepth bounds the nesting depth of in-progress instantiations. Same-type
// recursion terminates via the mname dedup below (the instance symbol is
// registered before its body is parsed); only unbounded type growth - e.g.
// func F[T any]() { F[[]T]() } - produces an endless chain of distinct mangled
// names, which this depth bound turns into an error instead of a hang. Go rejects
// the same program as an "instantiation cycle".
const maxInstDepth = 100

// instantiate produces a concrete (monomorphized) version of a generic template:
// it copies the template tokens, renames the declaration to its mangled name, and
// strips the type-parameter bracket. The body keeps the type-param names, which
// resolve to the concrete type args by identity via bindTypeParams during the
// re-parse.
func (p *Parser) instantiate(tmpl *genericTemplate, typeArgs []*vm.Type, pos Token) (Tokens, string, error) {
	// A partial list (fewer args than params) reaches here only when the
	// caller couldn't infer the rest; a too-long list is always an error.
	if len(typeArgs) != len(tmpl.typeParams) {
		return nil, "", p.wrapAt(pos, ErrSyntax, "generic %s expects %d type argument(s), got %d", tmpl.name, len(tmpl.typeParams), len(typeArgs))
	}

	mname := mangledName(tmpl.name, typeArgs)
	if s, _, ok := p.Symbols.Get(mname, ""); ok && s.Type != nil {
		return nil, mname, nil // Already instantiated.
	}

	if p.instDepth >= maxInstDepth {
		// Report the template name, not mname: an unbounded-growth cycle makes
		// mname a multi-hundred-char mangling of the ever-growing type arg.
		return nil, "", p.wrapAt(pos, ErrSyntax, "instantiation cycle: %s", tmpl.name)
	}

	if err := p.checkConstraints(tmpl, typeArgs); err != nil {
		return nil, "", err
	}

	// Keep the type-param names in the body; they resolve to the concrete type
	// args via the transient bindTypeParams binding active during the re-parse
	// (so identity travels, not name). Copy so the decl rename below does not
	// mutate the template's tokens.
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

// emitInstantiatedMethod instantiates methTmpl for the given type args
// and, if it produced fresh tokens, registers and parses the method,
// pushing the resulting tokens into p.pendingMethodDefs. Returns true if
// a new method was emitted. The caller must have cleared p.scope.
func (p *Parser) emitInstantiatedMethod(tmpl, methTmpl *genericTemplate, typeArgs []*vm.Type) (bool, error) {
	methToks, err := p.instantiateMethod(tmpl, methTmpl, typeArgs)
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
		return false, err
	}
	p.pendingMethodDefs = append(p.pendingMethodDefs, fout...)
	return true, nil
}

// ensureTypeInstantiated resolves type arguments from a bracket block and
// instantiates the generic type template, registering the concrete type.
// Methods known at this point are instantiated inline; methods attached
// to the template after this call are picked up later by
// finalizeGenericMethods.
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
		restore := p.bindTypeParams(tmpl.typeParams, typeArgs)
		defer restore()
		p.instDepth++
		defer func() { p.instDepth-- }()
		_, err = p.parseTypeLine(instToks)
		if err != nil {
			p.scope = savedScope
			return "", err
		}
		tmpl.instances = append(tmpl.instances, genericInstance{typeArgs: typeArgs})
		for _, methTmpl := range tmpl.methods {
			if _, err := p.emitInstantiatedMethod(tmpl, methTmpl, typeArgs); err != nil {
				p.scope = savedScope
				return "", err
			}
		}
		p.scope = savedScope
	}
	return mname, nil
}

// instantiatePendingMethods walks the generic type templates once and
// emits any (instance x method) pair that has not been instantiated yet.
// Returns progress=true if at least one new method was emitted. The
// symbol guard in instantiateMethod makes it safe to call repeatedly.
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
				emitted, err := p.emitInstantiatedMethod(tmpl, methTmpl, inst.typeArgs)
				if err != nil {
					return progress, err
				}
				if emitted {
					progress = true
				}
			}
		}
	}
	return progress, nil
}

// instantiateMethod creates a concrete version of a generic method template
// by substituting type parameter names with concrete types in the token stream.
// The receiver block is rewritten from e.g. (b Box[T]) to (b Box#int).
// Returns nil if the method is already instantiated.
func (p *Parser) instantiateMethod(typeTmpl, methTmpl *genericTemplate, typeArgs []*vm.Type) (Tokens, error) {
	mTypeName := mangledName(typeTmpl.name, typeArgs)
	methFullName := mTypeName + "." + methTmpl.name
	// Pointer-receiver methods are stored by registerFunc under "*T.method";
	// align the guard key so the instance is not re-emitted forever.
	if methTmpl.ptrRecv {
		methFullName = "*" + methFullName
	}

	// Guard: already instantiated.
	if _, _, ok := p.Symbols.Get(methFullName, ""); ok {
		return nil, nil
	}

	// Keep the type-param names in the body; they resolve via bindTypeParams
	// during the re-parse. Copy so the receiver rewrite below does not mutate
	// the template's tokens.
	out := append(Tokens(nil), methTmpl.rawTokens...)

	// Collapse TypeName[Args] into the mangled name in the receiver ParenBlock
	// (the first ParenBlock, at index 1 after the func keyword).
	if len(out) > 1 && out[1].Tok == lang.ParenBlock {
		out[1].Str = p.stripRecvTypeParams(out[1].Str, typeTmpl.name, mTypeName)
	}

	return out, nil
}

// stripRecvTypeParams rewrites a receiver block string by replacing
// TypeName[...] with the mangled name. For example:
//
//	"(b Box[int])"   -> "(b Box#int)"
//	"(b *Box[int])"  -> "(b *Box#int)"
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

// inferTypeArgs infers concrete type arguments for a generic function call
// by examining the call argument expressions and matching them against the
// template's type parameters through the function signature (stored in genSym.Type).
// prefix supplies explicit leading type args (partial type-argument lists, e.g.
// Grow[S](nil, n)); the remaining params are inferred from the call args.
func (p *Parser) inferTypeArgs(tmpl *genericTemplate, genSym *symbol.Symbol, callArgs scan.Token, prefix []*vm.Type) ([]*vm.Type, error) {
	argToks, err := p.scanBlock(callArgs, false)
	if err != nil {
		return nil, err
	}
	args := argToks.Split(lang.Comma)

	// Build set of type parameter names.
	tpNames := make(map[string]bool, len(tmpl.typeParams))
	for _, tp := range tmpl.typeParams {
		tpNames[tp.name] = true
	}

	// errAt-style wrapper so inference failures carry the call site's source
	// position; `mvm test`'s drop-on-compile-error retry needs it to attribute
	// the error to a file and skip it.
	posErr := func(format string, a ...any) error {
		return p.errAt(Token{Token: callArgs}, format, a...)
	}

	// Generic templates whose signature failed to parse cleanly leave Type nil;
	// surface an inference error rather than nil-deref.
	if genSym.Type == nil {
		return nil, posErr("cannot infer type parameters for %s: signature unresolved", tmpl.name)
	}

	// Match each argument to the corresponding parameter type from the parsed signature.
	// If the parameter type name is a type parameter, infer it from the argument.
	params := genSym.Type.Params
	isVariadic := genSym.Type.Rtype.IsVariadic() && len(params) > 0
	// A spread call `f(s...)` passes the whole slice in the variadic slot, so it
	// matches the variadic param `[]T` directly; per-element calls `f(a, b)` do
	// not. Go forbids mixing spread with extra variadic elements, so a spread
	// call has exactly one slice argument in that slot.
	spread := len(argToks) > 0 && argToks[len(argToks)-1].Tok == lang.Ellipsis
	inferred := make(map[string]*vm.Type, len(tmpl.typeParams))
	// Seed explicit leading type args so pass 1 skips already-bound params and
	// pass 2 can unpack their constraints (e.g. ~map[K]V) for the rest.
	for i, t := range prefix {
		if i < len(tmpl.typeParams) {
			inferred[tmpl.typeParams[i].name] = t
		}
	}
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
			// Without spread, the argument is a single element - descend into
			// ElemType so unification sees `T`, not `[]T`. With spread the
			// argument IS the whole slice, matching the variadic param `[]T`.
			if !spread && pType.ElemType != nil {
				pType = pType.ElemType
			}
		default:
			continue
		}
		// Skip when every type param in pType is already bound: avoids a
		// redundant inferExprType (which can fail legitimately on later
		// args - e.g. slices.Equal's second []E with a shape inferExprType
		// can't currently type).
		if !hasUnboundTypeParam(pType, tpNames, inferred) {
			continue
		}
		var argTyp *vm.Type
		if argExpr[0].Tok == lang.Func && argExpr[len(argExpr)-1].Tok == lang.BraceBlock {
			// Plain func-literal argument: type it from its signature alone.
			// inferExprType would parseFunc the whole closure, instantiating any
			// generic call in its body into a throwaway buffer - the emitted body
			// is then lost, leaving an empty func slot that nil-derefs at runtime
			// (e.g. slices.SortFunc(s, func(a,b T) int { return cmp.Compare(a,b) })).
			// The closure's type is fixed by its signature, so the body is moot here.
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

// inferExprType determines the type of an infix token expression by first
// parsing it into postfix form (reusing parseExpr), then walking the postfix
// tokens right-to-left following the same pattern as evalConstExpr.
func (p *Parser) inferExprType(toks Tokens) *vm.Type {
	postfix, err := p.parseExpr(toks, "")
	if err != nil || len(postfix) == 0 {
		return nil
	}
	typ, _ := p.postfixType(postfix)
	return typ
}

// postfixType walks postfix tokens right-to-left (like evalConstExpr) and
// returns the result type and the number of tokens consumed.
func (p *Parser) postfixType(in Tokens) (*vm.Type, int) {
	l := len(in) - 1
	if l < 0 {
		return nil, 0
	}
	t := in[l]
	id := t.Tok

	switch {
	case id == lang.Period:
		// Field selector: result type is the field type.
		leftTyp, ln := p.postfixType(in[:l])
		if leftTyp == nil {
			return nil, 0
		}
		fieldName := t.Str[1:] // Strip leading ".".
		// Auto-dereference pointer types for field access (Go: s.F works for *T).
		structTyp := leftTyp
		if structTyp.Rtype.Kind() == reflect.Pointer {
			structTyp = structTyp.Elem()
		}
		if structTyp.Rtype.Kind() == reflect.Struct {
			if ft := structTyp.FieldType(fieldName); ft != nil {
				return ft, 1 + ln
			}
		}
		// Method: look up in symbol table.
		if ms, _ := p.Symbols.MethodByName(&symbol.Symbol{Kind: symbol.Type, Name: leftTyp.Name, Type: leftTyp}, fieldName); ms != nil {
			return ms.Type, 1 + ln
		}
		return nil, 0

	case id.IsBinaryOp():
		typ2, l2 := p.postfixType(in[:l])
		typ1, l1 := p.postfixType(in[:l-l2])
		if id.IsBoolOp() {
			return p.Symbols["bool"].Type, 1 + l1 + l2
		}
		// Arithmetic / bitwise: result type follows from operands.
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
			return vm.PointerTo(inner), 1 + ln
		case lang.Deref:
			if inner.Rtype.Kind() == reflect.Pointer {
				return inner.Elem(), 1 + ln
			}
		case lang.Arrow:
			if inner.Rtype.Kind() == reflect.Chan {
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
				return vm.PointerTo(firstArgType), totalLen
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
			// pkg-qualified callee `pkg.Func(...)`: the package ident precedes
			// the Period selector. Resolve the func's return type so inference
			// can type an arg like `Or(strings.Compare(a, b))`. Generic members
			// are intentionally NOT resolved here: typing a nested generic
			// call's result lets inference wrongly succeed and reach bad
			// nested-generic codegen (a known hang). Mirrors callFuncType.
			if len(rest) < 2 || rest[len(rest)-2].Tok != lang.Ident {
				break
			}
			ps := p.Symbols[rest[len(rest)-2].Str]
			if ps == nil || ps.Kind != symbol.Pkg {
				break
			}
			member := fnTok.Str[1:] // strip leading "."
			var ft *vm.Type
			if pkg := p.Packages[ps.PkgPath]; pkg != nil {
				if v, ok := pkg.Values[member]; ok {
					if rv := v.Reflect(); rv.IsValid() && rv.Kind() == reflect.Func {
						ft = &vm.Type{Rtype: rv.Type()}
					}
				}
			}
			if ft == nil {
				if qs, ok := p.Symbols[ps.PkgPath+"."+member]; ok && qs.Kind != symbol.Generic {
					if t := symbol.Vtype(qs); t.IsFunc() {
						ft = t
					}
				}
			}
			if ft != nil {
				totalLen++ // account for the package ident token
				return funcReturnType(ft), totalLen
			}
		}
		return nil, totalLen

	case id == lang.Index:
		_, il := p.postfixType(in[:l]) // index expression
		containerTyp, cl := p.postfixType(in[:l-il])
		if containerTyp == nil {
			return nil, 0
		}
		switch containerTyp.Rtype.Kind() {
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
		switch containerTyp.Rtype.Kind() {
		case reflect.Slice, reflect.String:
			return containerTyp, total
		case reflect.Array:
			return vm.SliceOf(containerTyp.Elem()), total
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
