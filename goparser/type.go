package goparser

import (
	"errors"
	"fmt"
	"go/constant"
	"reflect"
	"slices"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

type typeFlag int

const (
	parseTypeIn typeFlag = iota
	parseTypeOut
	parseTypeVar
	parseTypeType
	parseTypeRecv // method receiver: Heap[0], not a stack param
)

// Type parsing error definitions.
var (
	ErrEllipsisArray  = errors.New("[...] array")
	ErrFuncType       = errors.New("invalid function type")
	ErrInvalidType    = errors.New("invalid type")
	ErrMissingType    = errors.New("missing type")
	ErrSize           = errors.New("invalid size")
	ErrSyntax         = errors.New("syntax error")
	ErrNotImplemented = errors.New("not implemented")
)

// ErrUndefined is returned during parsing when a referenced symbol is not yet defined.
// It is retryable: the lazy fixpoint loop in interp.Eval defers the declaration and retries
// after other declarations have been processed.
type ErrUndefined struct {
	Name string
	Loc  string // optional "file:line:col" source position
	Pos  int    // optional global source offset, for snippet rendering
}

func (e ErrUndefined) Error() string {
	if e.Loc != "" {
		return e.Loc + ": undefined: " + e.Name
	}
	return "undefined: " + e.Name
}

// ErrPos exposes the source offset so a diagnostic chokepoint (interp.Eval)
// can render a source snippet. Returns 0 when no position was attached.
func (e ErrUndefined) ErrPos() int { return e.Pos }

// undef builds an ErrUndefined positioned at tok, so the message carries
// "file:line:col" and callers can render a source snippet.
func (p *Parser) undef(name string, tok Token) ErrUndefined {
	return ErrUndefined{Name: name, Loc: p.Sources.FormatPos(tok.Pos), Pos: tok.Pos}
}

func (p *Parser) resolveEllipsisArray(elemTyp *vm.Type, toks Tokens, braceIdx int) (*vm.Type, error) {
	if braceIdx >= len(toks) || toks[braceIdx].Tok != lang.BraceBlock {
		return nil, errors.New("[...] requires a composite literal")
	}
	tokens, err := p.scanBlock(toks[braceIdx].Token, false)
	if err != nil {
		return nil, err
	}
	idx, maxLen := 0, 0
	for _, item := range tokens.Split(lang.Comma) {
		if len(item) == 0 {
			continue
		}
		if ci := item.Index(lang.Colon); ci > 0 {
			if k, ok := p.constIntKey(item[:ci]); ok {
				idx = k
			}
		}
		idx++
		if idx > maxLen {
			maxLen = idx
		}
	}
	return vm.SymArray(maxLen, elemTyp), nil
}

// constIntKey evaluates `keyToks` as a constant integer expression
// (composite-literal index key). Returns false on parse / non-int error;
// callers should fall back to sequential indexing.
func (p *Parser) constIntKey(keyToks Tokens) (int, bool) {
	toks, err := p.parseExpr(keyToks, "")
	if err != nil {
		return 0, false
	}
	cval, _, _, err := p.evalConstExpr(toks)
	if err != nil {
		return 0, false
	}
	k, ok := constant.Int64Val(cval)
	if !ok {
		return 0, false
	}
	return int(k), true
}

func (p *Parser) resolvePkgType(s *symbol.Symbol, name string, tok Token) (*vm.Type, error) {
	// Prefer the qualified-alias symbol registered by importSrc, which carries
	// full mvm-level type info (Methods, ElemType, Fields, ...). Falling back
	// to pkg.Values would synthesize a stripped Type{Name, Rtype} that loses
	// method reachability for source-defined types whose Rtype is the
	// underlying primitive (e.g. UUID's Rtype is [16]uint8 with no .Name()).
	if sym, ok := p.Symbols[s.PkgPath+"."+name]; ok {
		if sym.Kind == symbol.Type && sym.Type != nil {
			return sym.Type, nil
		}
		if sym.Kind != symbol.Type {
			// A non-type member (func/var/const) is not a type expression; fail
			// cleanly so e.g. `*pkg.Fn()` parses as a deref, not a (*T)(...) conv.
			// (Its value slot may also be an unfilled zero Value here.)
			return nil, p.undef(s.Name+"."+name, tok)
		}
	}
	pkg, ok := p.Packages[s.PkgPath]
	if !ok {
		return nil, fmt.Errorf("package not found: %s", s.PkgPath)
	}
	v, ok := pkg.Values[name]
	if !ok || !v.IsValid() {
		if pkg.Bin {
			return &vm.Type{Name: name, Rtype: vm.OpaqueRtype}, nil
		}
		return nil, p.undef(s.Name+"."+name, tok)
	}
	rt := v.Type()
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	return &vm.Type{Name: rt.Name(), PkgPath: rt.PkgPath(), Rtype: rt}, nil
}

func (p *Parser) parseTypeExpr(in Tokens) (typ *vm.Type, n int, err error) {
	if len(in) == 0 {
		return nil, 0, ErrMissingType
	}
	switch in[0].Tok {
	case lang.BracketBlock:
		typ, i, err := p.parseTypeExpr(in[1:])
		if err != nil {
			return nil, 0, err
		}
		if b := in[0].Block(); len(b) > 0 {
			x, err := p.scanBlock(in[0].Token, false)
			if err != nil {
				return nil, 0, err
			}
			// [...]T syntax: size is resolved by the caller from the composite literal.
			if len(x) == 1 && x[0].Tok == lang.Ellipsis {
				return typ, 1 + i, ErrEllipsisArray
			}
			if x, err = p.parseExpr(x, ""); err != nil {
				return nil, 0, err
			}
			cval, _, _, err := p.evalConstExpr(x)
			if err != nil {
				return nil, 0, err
			}
			size, ok := constValue(cval).(int)
			if !ok {
				return nil, 0, ErrSize
			}
			return vm.SymArray(size, typ), 1 + i, nil
		}
		return vm.SymSlice(typ), 1 + i, nil

	case lang.Mul:
		typ, i, err := p.parseTypeExpr(in[1:])
		if err != nil {
			return nil, 0, err
		}
		return vm.SymPtr(typ), 1 + i, nil

	case lang.Func:
		// Get argument and return token positions depending on function pattern:
		// method with receiver, named function or anonymous closure.
		var out Tokens
		var indexArgs int
		var recvr string
		switch l, in1 := len(in), in[1]; {
		case isMethodDecl(in):
			recvr = in1.Block()
			indexArgs, out = 3, in[4:]
		case l >= 3 && in1.Tok == lang.Ident:
			indexArgs, out = 2, in[3:]
		case l >= 2 && in1.Tok == lang.ParenBlock:
			indexArgs, out = 1, in[2:]
		default:
			return nil, 0, ErrFuncType
		}

		// We can now parse function input and output parameter types.
		// Input parameters are always enclosed by parenthesis.
		// For methods, parse the receiver separately as Heap[0] (not a stack param),
		// so explicit params get the correct frame indices (-2, -3, ...).
		regParams := p.regFuncSig
		p.regFuncSig = false
		saveTypeOnly := p.typeOnly
		if !regParams {
			p.typeOnly = true
		}
		var recvErr error
		if recvr != "" {
			var recvrToks Tokens
			if recvrToks, recvErr = p.scanBlock(in[1].Token, false); recvErr == nil {
				_, _, _, recvErr = p.parseParamTypes(recvrToks, parseTypeRecv)
			}
		}
		typ, _, _, err := p.parseFuncParams(in[indexArgs], out)
		p.typeOnly = saveTypeOnly
		if recvErr != nil {
			return nil, 0, recvErr
		}
		if err != nil {
			return nil, 0, err
		}
		// Count return type tokens so the caller can advance past them.
		// parseFuncParams consumes out as return types (unless out starts with a
		// BraceBlock, which is a function body rather than a return type).
		nRet := 0
		if len(out) > 0 && out[0].Tok != lang.BraceBlock {
			if out[0].Tok == lang.ParenBlock {
				nRet = 1 // parenthesized return list, e.g. (int, error)
			} else {
				// Bare return type: measure token count via parseTypeExpr.
				// Use typeOnly to avoid registering symbols as a side effect.
				save := p.typeOnly
				p.typeOnly = true
				_, nRet, _ = p.parseTypeExpr(out)
				p.typeOnly = save
			}
		}
		return typ, 1 + indexArgs + nRet, nil

	case lang.Ident:
		s, _, ok := p.symGet(in[0].Str)
		if as, _, aok := p.pkgAlias(in[0].Str, in[0].Pos); aok {
			s, ok = as, true
		}
		if !ok {
			return nil, 0, p.undef(in[0].Str, in[0])
		}
		if s.Kind == symbol.Pkg && len(in) >= 3 && in[1].Tok == lang.Period {
			// Package-qualified generic type: pkg.Type[T].
			if len(in) >= 4 && in[3].Tok == lang.BracketBlock {
				qualifiedName := s.PkgPath + "." + in[2].Str
				if gs, ok := p.Symbols[qualifiedName]; ok && gs.Kind == symbol.Generic {
					tmpl := gs.Data.(*genericTemplate)
					mname, err := p.ensureTypeInstantiated(tmpl, in[3].Token)
					if err != nil {
						return nil, 0, err
					}
					s2, _, ok := p.Symbols.Get(mname, "")
					if !ok || s2.Type == nil {
						return nil, 0, p.undef(mname, in[0])
					}
					return s2.Type, 4, nil
				}
			}
			typ, err := p.resolvePkgType(s, in[2].Str, in[2])
			if err != nil {
				return nil, 0, err
			}
			return typ, 3, nil
		}
		if s.Kind == symbol.Generic && len(in) >= 2 && in[1].Tok == lang.BracketBlock {
			tmpl := s.Data.(*genericTemplate)
			mname, err := p.ensureTypeInstantiated(tmpl, in[1].Token)
			if err != nil {
				return nil, 0, err
			}
			s2, _, ok := p.Symbols.Get(mname, "")
			if !ok || s2.Type == nil {
				return nil, 0, p.undef(mname, in[0])
			}
			return s2.Type, 2, nil
		}
		if s.Kind != symbol.Type {
			// An auto-import binding must yield to a package-level type of the same name.
			// Defer as a forward reference so the fixpoint retries.
			if s.Kind == symbol.Pkg && s.AutoImport {
				return nil, 0, p.undef(in[0].Str, in[0])
			}
			return nil, 0, p.wrapAt(in[0], ErrInvalidType, "%s is not a type", in[0].Str)
		}
		return s.Type, 1, nil

	case lang.Struct:
		typ, err := p.parseStructType(in)
		if err != nil {
			return nil, 0, err
		}
		return typ, 2, nil

	case lang.Arrow:
		// "<-chan T" is recv-only; require chan keyword next.
		if len(in) < 3 || in[1].Tok != lang.Chan {
			return nil, 0, p.wrapAt(in[0], ErrInvalidType, "expected 'chan' after '<-'")
		}
		elemTyp, i, err := p.parseTypeExpr(in[2:])
		if err != nil {
			return nil, 0, err
		}
		return vm.SymChan(reflect.RecvDir, elemTyp), 2 + i, nil

	case lang.Chan:
		if len(in) < 2 {
			return nil, 0, p.wrapAt(in[0], ErrInvalidType, "missing element type after 'chan'")
		}
		dir := reflect.BothDir
		rest, skip := in[1:], 1
		if len(rest) > 0 && rest[0].Tok == lang.Arrow {
			dir, rest, skip = reflect.SendDir, rest[1:], 2 // chan<-: send-only, skip both chan and <- tokens
		}
		elemTyp, i, err := p.parseTypeExpr(rest)
		if err != nil {
			return nil, 0, err
		}
		return vm.SymChan(dir, elemTyp), skip + i, nil

	case lang.Map:
		if len(in) < 3 || in[1].Tok != lang.BracketBlock {
			return nil, 0, p.wrapAt(in[0], ErrInvalidType, "expected 'map[KeyType]', got %s", in[0].Describe())
		}
		kin, err := p.scanBlock(in[1].Token, false)
		if err != nil {
			return nil, 0, err
		}
		ktyp, _, err := p.parseTypeExpr(kin) // Key type
		if err != nil {
			return nil, 0, err
		}
		etyp, i, err := p.parseTypeExpr(in[2:]) // Element type
		if err != nil {
			return nil, 0, err
		}
		return vm.SymMap(ktyp, etyp), 2 + i, nil

	case lang.Interface:
		if len(in) < 2 || in[1].Tok != lang.BraceBlock {
			return nil, 0, p.wrapAt(in[0], ErrSyntax, "expected 'interface{...}', got %s", in[0].Describe())
		}
		if strings.TrimSpace(in[1].Block()) == "" {
			// Empty interface (equivalent to any).
			return &vm.Type{Rtype: vm.AnyRtype}, 2, nil
		}
		toks, err := p.scanBlock(in[1].Token, false)
		if err != nil {
			return nil, 0, err
		}
		var methods []vm.IfaceMethod
		var elems []vm.TypeElem
		hasComparable := false
		for _, lt := range toks.Split(lang.Semicolon) {
			if len(lt) == 0 {
				continue
			}
			// Constraint type element(s): leading "~" or a union (contains "|").
			if lt[0].Tok == lang.Tilde || lt.Index(lang.Or) >= 0 {
				es, err := p.parseTypeElems(lt)
				if err != nil {
					return nil, 0, err
				}
				elems = append(elems, es...)
				continue
			}
			// Built-in `comparable` as an embedded constraint element (e.g.
			// `interface { comparable; error }`). It is not an ordinary type, so
			// record it rather than resolving it via parseTypeExpr (which fails).
			if len(lt) == 1 && lt[0].Tok == lang.Ident && lt[0].Str == "comparable" {
				hasComparable = true
				continue
			}
			if lt[0].Tok != lang.Ident {
				// A composite type term (*P, []T, map[K]V, ...): a core-type
				// constraint element, not a method.
				es, err := p.parseTypeElems(lt)
				if err != nil {
					return nil, 0, err
				}
				elems = append(elems, es...)
				continue
			}
			if len(lt) == 1 || lt[1].Tok != lang.ParenBlock {
				ifaceType, _, err := p.parseTypeExpr(lt)
				if err != nil {
					return nil, 0, err
				}
				if !ifaceType.IsInterface() {
					return nil, 0, p.wrapAt(lt[0], ErrSyntax, "%s is not an interface", lt[0].Str)
				}
				// A forward-declared embedded iface (cross-file: Banded embeds
				// Matrix with band.go parsed first) has no methods to copy yet:
				// defer this decl so the fixpoint retries after the target parses.
				if ifaceType.Placeholder {
					return nil, 0, p.undef(lt[0].Str, lt[0])
				}
				ifaceType.EnsureIfaceMethods()
				methods = append(methods, ifaceType.IfaceMethods...)
				continue
			}
			p.typeOnly = true
			methodType, _, _, err := p.parseFuncParams(lt[1], lt[2:])
			p.typeOnly = false
			if err != nil {
				return nil, 0, err
			}
			// Sig carries the symbolic signature; Rtype is left nil and filled by
			// comp.materializeIfaceMethods after the method pre-pass. Materializing
			// here would eagerly stamp a referenced named type (e.g. a struct in the
			// signature) methodless before its method table is populated, so the
			// reserve gate would miss it and give it a methodless identity.
			methods = append(methods, vm.IfaceMethod{Name: lt[0].Str, ID: -1, Sig: methodType})
		}
		// Use any as underlying reflect type; method set is tracked in IfaceMethods.
		return &vm.Type{
			Rtype:        vm.AnyRtype,
			IfaceMethods: methods,
			TypeElems:    elems,
			Comparable:   hasComparable,
		}, 2, nil

	default:
		return nil, 0, p.wrapAt(in[0], ErrNotImplemented,
			"cannot parse type starting with %s", in[0].Describe())
	}
}

// parseTypeElems parses a line from an interface body consisting of a type-element
// union (e.g. "~int | ~int8 | ~string").
func (p *Parser) parseTypeElems(lt Tokens) ([]vm.TypeElem, error) {
	var out []vm.TypeElem
	for _, seg := range lt.Split(lang.Or) {
		if len(seg) == 0 {
			continue
		}
		approx := false
		if seg[0].Tok == lang.Tilde {
			approx = true
			seg = seg[1:]
		}
		if len(seg) == 0 {
			return nil, fmt.Errorf("%w: empty type element", ErrSyntax)
		}
		typ, _, err := p.parseTypeExpr(seg)
		if err != nil {
			return nil, err
		}
		out = append(out, vm.TypeElem{Approx: approx, Type: typ})
	}
	return out, nil
}

// parseParamTypes parses a list of comma separated typed parameters and returns a list of
// runtime types. Implicit parameter names and types are supported.
func (p *Parser) parseParamTypes(in Tokens, flag typeFlag) (types []*vm.Type, vars []string, variadic bool, err error) {
	// Parse from right to left, to allow multiple comma separated parameters of the same type.
	list := in.Split(lang.Comma)
	// sawTypeOnly tracks whether a previously-parsed (rightward) param had no
	// name. Mixed `(name type, type)` is invalid in Go: once a type-only param
	// appears, the rest of the list must be type-only too. This prevents a
	// forward-declared type Ident on the left from being misclassified as a
	// param name with shared type from the right (e.g. `(UUID, error)` where
	// UUID is defined in another file not yet parsed).
	sawTypeOnly := false
	sawNamed := false
	for i, v := range slices.Backward(list) {
		t := v
		if len(t) == 0 {
			continue
		}
		param := ""
		// Once a named param/field appears to the right, a lone ident that
		// also names a type (so hasFirstParam reports "type-only") must still
		// be a NAME sharing the trailing type, not an embedded type.
		treatAsParam := p.hasFirstParam(t)
		if !treatAsParam && sawNamed && !sawTypeOnly && len(t) == 1 && t[0].Tok == lang.Ident {
			treatAsParam = true
		}
		if treatAsParam {
			origName := t[0].Str
			// Uniquify blank params so multiple `_` results don't collide on a
			// single "scope/_" symbol key. The collision corrupts bare-return
			// zero-init -- each blank result slot needs its own type, else a
			// struct zero lands in a sibling (e.g. bool) slot. Mirrors
			// addLocalVar's blankName handling for `_` locals.
			if origName == "_" {
				origName = p.blankName()
			}
			if flag == parseTypeVar {
				// Top-level vars want the canonical pkgKey; pkgKey itself
				// falls through to scopedName for nested (in-function) vars.
				param = p.pkgKey(origName)
			} else {
				param = p.scopedName(origName)
			}
			t = t[1:]
			if len(t) == 0 {
				if len(types) == 0 {
					// In a func signature (param/result) a lone undefined ident is a forward-declared type.
					if flag == parseTypeOut || flag == parseTypeIn {
						if _, _, ok := p.symGet(origName); !ok {
							return nil, nil, false, p.undef(origName, v[0])
						}
					}
					return nil, nil, false, ErrMissingType
				}
				// Once a rightward param is type-only, the list is a type list:
				// this lone Ident is a (possibly forward-declared) type, not a
				// param name to inherit the right-side type.
				if sawTypeOnly {
					if _, _, ok := p.Symbols.Get(origName, p.scope); !ok {
						return nil, nil, false, p.undef(origName, v[0])
					}
					// Restore the full token and treat it as a type expression below.
					t = v
					param = ""
				} else {
					// Type was omitted, apply the previous one from the right.
					types = append([]*vm.Type{types[0]}, types...)
					p.addSymVar(i, len(list), param, types[0], flag)
					vars = append([]string{param}, vars...)
					sawNamed = true
					continue
				}
			}
		}
		// Detect variadic parameter: ...T becomes []T.
		ellipsis := false
		if len(t) > 0 && t[0].Tok == lang.Ellipsis {
			variadic = true
			ellipsis = true
			t = t[1:]
		}
		// typeOnly: a func-typed param's result names must not leak into the
		// enclosing function's p.namedOut.
		save := p.typeOnly
		p.typeOnly = true
		typ, _, err := p.parseTypeExpr(t)
		p.typeOnly = save
		if err != nil {
			return nil, nil, false, err
		}
		// Index can't be used here: a trailing comma adds an empty last element.
		if ellipsis {
			typ = vm.SymSlice(typ)
		}
		if param != "" {
			p.addSymVar(i, len(list), param, typ, flag)
		}
		types = append([]*vm.Type{typ}, types...)
		vars = append([]string{param}, vars...)
		if param == "" {
			sawTypeOnly = true
		} else {
			sawNamed = true
		}
	}
	return types, vars, variadic, err
}

// typeTokenValue mints the zero-value descriptor stored in a symbol's Value,
// or an invalid Value when the type carries no rtype yet. The rtype is
// materialized later at comp, which re-derives the descriptor from the
// symbol's *Type; the parse-time Value is a convenience, not load-bearing.
func typeTokenValue(typ *vm.Type) vm.Value {
	if typ == nil || typ.Rtype == nil {
		return vm.Value{}
	}
	return vm.NewValue(typ.Rtype)
}

func (p *Parser) addSymVar(index, nparams int, name string, typ *vm.Type, flag typeFlag) {
	if p.typeOnly {
		return
	}
	zv := typeTokenValue(typ)
	switch flag {
	case parseTypeRecv:
		// Receiver lives in Heap[0] of the method closure, not on the call stack.
		// Index is irrelevant; the compiler emits HeapGet 0 via FreeVars.
		p.SymSet(name, &symbol.Symbol{
			Kind: symbol.LocalVar, Name: name, Index: symbol.UnsetAddr,
			Captured: true, Used: true,
			Type: typ, Value: zv,
		})
	case parseTypeIn:
		p.SymAdd(index-nparams-2, name, zv, symbol.LocalVar, typ)
	case parseTypeOut:
		p.SymAdd(p.framelen[p.funcScope], name, zv, symbol.LocalVar, typ)
		p.framelen[p.funcScope]++
		if name != "" {
			p.namedOut = append(p.namedOut, name)
		}
	case parseTypeVar:
		if p.funcScope == "" {
			if s, ok := p.Symbols[name]; ok && s.Index != symbol.UnsetAddr {
				// Preserve pre-assigned index from allocGlobalSlots.
				s.Type = typ
				s.Value = zv
			} else {
				p.SymAdd(symbol.UnsetAddr, name, zv, symbol.Var, typ)
			}
			break
		}
		p.SymAdd(p.framelen[p.funcScope], name, zv, symbol.LocalVar, typ)
		p.framelen[p.funcScope]++
	}
}

func (p *Parser) parseFuncParams(argBlock Token, out Tokens) (typ *vm.Type, inNames, outNames []string, err error) {
	iargs, err := p.scanBlock(argBlock.Token, false)
	if err != nil {
		return nil, nil, nil, err
	}
	arg, argNames, isVariadic, err := p.parseParamTypes(iargs, parseTypeIn)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(out) > 0 && out[0].Tok == lang.BraceBlock {
		// BraceBlock at start of out is a function body or composite literal, not a return type.
		out = nil
	} else if len(out) > 0 && out[0].Tok == lang.ParenBlock {
		if out, err = p.scanBlock(out[0].Token, false); err != nil {
			return nil, nil, nil, err
		}
	}
	ret, retNames, _, err := p.parseParamTypes(out, parseTypeOut)
	if err != nil {
		return nil, nil, nil, err
	}
	return vm.SymFunc(arg, ret, isVariadic), baseNames(argNames), baseNames(retNames), nil
}

func (p *Parser) parseFuncSig(in Tokens) (typ *vm.Type, inNames, outNames []string, err error) {
	if len(in) == 0 || in[0].Tok != lang.Func {
		return nil, nil, nil, ErrFuncType
	}
	var out Tokens
	var indexArgs int
	switch l, in1 := len(in), in[1]; {
	case l >= 3 && in1.Tok == lang.Ident:
		indexArgs, out = 2, in[3:]
	case l >= 2 && in1.Tok == lang.ParenBlock:
		indexArgs, out = 1, in[2:]
	default:
		return nil, nil, nil, ErrFuncType
	}
	p.typeOnly = true
	typ, inNames, outNames, err = p.parseFuncParams(in[indexArgs], out)
	p.typeOnly = false
	return
}

func baseNames(scoped []string) []string {
	var raw []string
	for i, s := range scoped {
		if s == "" {
			continue
		}
		if raw == nil {
			raw = make([]string, len(scoped))
		}
		if j := strings.LastIndex(s, "/"); j >= 0 {
			raw[i] = s[j+1:]
		} else {
			raw[i] = s
		}
	}
	return raw
}

func (p *Parser) parseEmbeddedField(lt Tokens) (fieldType, origType *vm.Type) {
	isPtr := false
	toks := lt
	if len(toks) >= 2 && toks[0].Tok == lang.Mul {
		isPtr = true
		toks = toks[1:]
	}

	// Determine the embedded field name: the last Ident before an optional
	// BracketBlock (generic instantiation). Supported shapes:
	//   T, T[Args], pkg.T, pkg.T[Args]
	var name string
	var typeToks Tokens
	switch {
	case len(toks) == 1 && toks[0].Tok == lang.Ident:
		name, typeToks = toks[0].Str, toks
	case len(toks) == 2 && toks[0].Tok == lang.Ident && toks[1].Tok == lang.BracketBlock:
		name, typeToks = toks[0].Str, toks
	case len(toks) == 3 && toks[0].Tok == lang.Ident && toks[1].Tok == lang.Period && toks[2].Tok == lang.Ident:
		name, typeToks = toks[2].Str, toks
	case len(toks) == 4 && toks[0].Tok == lang.Ident && toks[1].Tok == lang.Period && toks[2].Tok == lang.Ident && toks[3].Tok == lang.BracketBlock:
		name, typeToks = toks[2].Str, toks
	default:
		return nil, nil
	}

	typ, _, err := p.parseTypeExpr(typeToks)
	if err != nil {
		return nil, nil
	}
	ft := *typ
	ft.Name = name
	// The clone is a field reference, not a forward declaration: clear Placeholder
	// so MaterializeRtype resolves the field via its Base instead of bailing.
	ft.Placeholder = false
	ft.Defined = false // a field clone resolves to Base, unlike a defined type
	// reflect.StructField.PkgPath must be empty for exported fields and the
	// owning package's path for unexported ones.
	if IsExported(name) {
		ft.PkgPath = ""
	} else {
		ft.PkgPath = p.pkgName
	}
	ft.Base = typ
	if isPtr {
		// The ptr wrapper carries the field name: materialize reads it from
		// the field type (SymPtr itself leaves Name empty).
		pt := vm.SymPtr(&ft)
		pt.Name = name
		pt.PkgPath = ft.PkgPath
		return pt, typ
	}
	return &ft, typ
}

func (p *Parser) hasFirstParam(in Tokens) bool {
	if in[0].Tok != lang.Ident {
		return false
	}
	s, _, ok := p.symGet(in[0].Str)
	if as, _, aok := p.pkgAlias(in[0].Str, in[0].Pos); aok {
		s, ok = as, true
	}
	if ok && s.Kind == symbol.Pkg {
		// Only treat as a qualified type expression (pkg.Type) if followed by '.'.
		// Otherwise, the ident is a parameter name that shadows the package
		// (e.g. `func f(time string)` where `time` is a param, not pkg qualifier).
		if len(in) > 1 && in[1].Tok == lang.Period {
			return false
		}
		return true
	}
	if !ok || (s.Kind != symbol.Type && s.Kind != symbol.Generic) {
		// Forward-declared generic type: an unknown ident followed by [args]
		// with nothing else is likely `UnknownType[args]`, not `name type`.
		// Return false so parseTypeExpr can emit ErrUndefined for the retry loop.
		if !ok && len(in) == 2 && in[1].Tok == lang.BracketBlock {
			return false
		}
		return true
	}
	// A generic type followed by [args] is a type instantiation (e.g. Viewer[int]),
	// not a "name type" pair - so the ident is the type, not a param name.
	if s.Kind == symbol.Generic && len(in) > 1 && in[1].Tok == lang.BracketBlock {
		return false
	}
	// The first ident is a known type name. If followed by tokens that start
	// a type expression, treat the ident as a field/param name (e.g. rune [N]T).
	if len(in) > 1 {
		switch in[1].Tok {
		case lang.BracketBlock, lang.Mul, lang.Func, lang.Ident, lang.Struct, lang.Map, lang.Interface, lang.Chan:
			return true
		}
	}
	return false
}

// placeholderStructElem returns the forward placeholder struct t is, or is an
// array (possibly nested) of, else nil.
func placeholderStructElem(t *vm.Type) *vm.Type {
	for t != nil {
		if t.Kind() == reflect.Struct && t.Placeholder {
			return t
		}
		if t.Kind() != reflect.Array {
			return nil
		}
		t = t.ElemType
	}
	return nil
}

func (p *Parser) parseStructType(in Tokens) (*vm.Type, error) {
	if len(in) < 2 || in[1].Tok != lang.BraceBlock {
		return nil, fmt.Errorf("%w: %v", ErrSyntax, in)
	}
	fieldToks, err := p.scanBlock(in[1].Token, false)
	if err != nil {
		return nil, err
	}
	var fields []*vm.Type
	var tags []string
	var embedded []vm.EmbeddedField
	for _, lt := range fieldToks.Split(lang.Semicolon) {
		if len(lt) == 0 {
			continue
		}
		// Strip trailing struct tag (a raw string literal), e.g. `json:"name"`.
		var tag string
		if len(lt) >= 2 && lt[len(lt)-1].Tok == lang.String {
			tag = lt[len(lt)-1].Block()
			lt = lt[:len(lt)-1]
		}
		if f, origType := p.parseEmbeddedField(lt); f != nil {
			// Keep a by-value embedded forward placeholder symbolically; don't defer.
			embedded = append(embedded, vm.EmbeddedField{FieldIdx: len(fields), Type: origType})
			fields = append(fields, f)
			tags = append(tags, tag)
			continue
		}
		types, names, _, err := p.parseParamTypes(lt, parseTypeType)
		if err != nil {
			// A lone ident that failed lookup and param-type parsing is likely a forward-declared type.
			// Return ErrUndefined so the lazy fixpoint loop can retry after the type is defined.
			if errors.Is(err, ErrMissingType) && len(lt) == 1 && lt[0].Tok == lang.Ident {
				return nil, p.undef(lt[0].Str, lt[0])
			}
			return nil, err
		}
		for i, name := range names {
			if j := strings.LastIndex(name, "/"); j >= 0 {
				name = name[j+1:]
			}
			pkgPath := ""
			if !IsExported(name) {
				pkgPath = p.pkgName
			}
			// A placeholder struct field (or an array of one) lacks a final layout,
			// so the struct's size is unknown: defer via ErrUndefined and retry.
			if ph := placeholderStructElem(types[i]); ph != nil {
				return nil, p.undef(ph.Name, lt[0])
			}
			if name == "" {
				// Unnamed field: likely an embedded type not yet defined.
				return nil, p.undef(types[i].String(), lt[0])
			}
			// Copy mvm-level type (preserving Params, IfaceMethods, etc.) and set field name.
			ft := *types[i]
			ft.Name = name
			ft.PkgPath = pkgPath
			ft.Defined = false // a field clone resolves to Base, unlike a defined type
			// Back-link to the source type so methods registered on it after
			// this clone was taken (typical: struct decl precedes method decls,
			// or named types whose Methods are populated by the compiler later)
			// remain reachable via MethodByName's ft.Base walk.
			ft.Base = types[i]
			fields = append(fields, &ft)
			tags = append(tags, tag)
		}
	}
	return vm.SymStruct(fields, embedded, tags), nil
}
