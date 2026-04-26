package goparser

import (
	"errors"
	"fmt"
	"go/constant"
	"reflect"
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
type ErrUndefined struct{ Name string }

func (e ErrUndefined) Error() string { return "undefined: " + e.Name }

func (p *Parser) resolveEllipsisArray(elemTyp *vm.Type, toks Tokens, braceIdx int) (*vm.Type, error) {
	if braceIdx >= len(toks) || toks[braceIdx].Tok != lang.BraceBlock {
		return nil, errors.New("[...] requires a composite literal")
	}
	tokens, err := p.scanBlock(toks[braceIdx].Token, false)
	if err != nil {
		return nil, err
	}
	idx := 0
	for _, item := range tokens.Split(lang.Comma) {
		if len(item) == 0 {
			continue
		}
		if ci := item.Index(lang.Colon); ci > 0 {
			// Keyed element: evaluate the key as a constant expression.
			// parseExpr converts infix to postfix, which evalConstExpr requires.
			// On failure, fall back to sequential index.
			keyToks, err := p.parseExpr(item[:ci], "")
			if err == nil {
				if cval, _, _, err := p.evalConstExpr(keyToks); err == nil {
					if k, ok := constant.Int64Val(cval); ok {
						idx = int(k)
					}
				}
			}
		}
		idx++
	}
	return vm.ArrayOf(idx, elemTyp), nil
}

func (p *Parser) resolvePkgType(s *symbol.Symbol, name string) (*vm.Type, error) {
	pkg, ok := p.Packages[s.PkgPath]
	if !ok {
		return nil, fmt.Errorf("package not found: %s", s.PkgPath)
	}
	v, ok := pkg.Values[name]
	if !ok {
		if pkg.Bin {
			return &vm.Type{Name: name, Rtype: vm.AnyRtype}, nil
		}
		return nil, ErrUndefined{s.Name + "." + name}
	}
	rt := v.Type()
	if rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	return &vm.Type{Name: rt.Name(), Rtype: rt}, nil
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
			return vm.ArrayOf(size, typ), 1 + i, nil
		}
		return vm.SliceOf(typ), 1 + i, nil

	case lang.Mul:
		typ, i, err := p.parseTypeExpr(in[1:])
		if err != nil {
			return nil, 0, err
		}
		return vm.PointerTo(typ), 1 + i, nil

	case lang.Func:
		// Get argument and return token positions depending on function pattern:
		// method with receiver, named function or anonymous closure.
		var out Tokens
		var indexArgs int
		var recvr string
		switch l, in1 := len(in), in[1]; {
		case l >= 4 && in1.Tok == lang.ParenBlock && in[2].Tok == lang.Ident && in[3].Tok == lang.ParenBlock:
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
		if recvr != "" {
			recvrToks, err := p.scanBlock(in[1].Token, false)
			if err != nil {
				return nil, 0, err
			}
			if _, _, _, err = p.parseParamTypes(recvrToks, parseTypeRecv); err != nil {
				return nil, 0, err
			}
		}
		typ, _, _, err := p.parseFuncParams(in[indexArgs], out)
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
		s, _, ok := p.Symbols.Get(in[0].Str, p.scope)
		if !ok {
			return nil, 0, ErrUndefined{in[0].Str}
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
						return nil, 0, ErrUndefined{mname}
					}
					return s2.Type, 4, nil
				}
			}
			typ, err := p.resolvePkgType(s, in[2].Str)
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
				return nil, 0, ErrUndefined{mname}
			}
			return s2.Type, 2, nil
		}
		if s.Kind != symbol.Type {
			return nil, 0, fmt.Errorf("%w: %s", ErrInvalidType, in[0].Str)
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
			return nil, 0, fmt.Errorf("%w: %s", ErrInvalidType, in[0].Str)
		}
		elemTyp, i, err := p.parseTypeExpr(in[2:])
		if err != nil {
			return nil, 0, err
		}
		return vm.ChanOf(reflect.RecvDir, elemTyp), 2 + i, nil

	case lang.Chan:
		if len(in) < 2 {
			return nil, 0, fmt.Errorf("%w: %s", ErrInvalidType, in[0].Str)
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
		return vm.ChanOf(dir, elemTyp), skip + i, nil

	case lang.Map:
		if len(in) < 3 || in[1].Tok != lang.BracketBlock {
			return nil, 0, fmt.Errorf("%w: %s", ErrInvalidType, in[0].Str)
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
		return vm.MapOf(ktyp, etyp), 2 + i, nil

	case lang.Interface:
		if len(in) < 2 || in[1].Tok != lang.BraceBlock {
			return nil, 0, fmt.Errorf("%w: %v", ErrSyntax, in)
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
		for _, lt := range toks.Split(lang.Semicolon) {
			if len(lt) == 0 || lt[0].Tok == lang.Comment {
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
			if lt[0].Tok != lang.Ident {
				return nil, 0, fmt.Errorf("%w: expected method name in interface", ErrSyntax)
			}
			if len(lt) == 1 || lt[1].Tok != lang.ParenBlock {
				ifaceType, _, err := p.parseTypeExpr(lt)
				if err != nil {
					return nil, 0, err
				}
				if !ifaceType.IsInterface() {
					return nil, 0, fmt.Errorf("%w: %s is not an interface", ErrSyntax, lt[0].Str)
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
			methods = append(methods, vm.IfaceMethod{Name: lt[0].Str, ID: -1, Rtype: methodType.Rtype})
		}
		// Use any as underlying reflect type; method set is tracked in IfaceMethods.
		return &vm.Type{
			Rtype:        vm.AnyRtype,
			IfaceMethods: methods,
			TypeElems:    elems,
		}, 2, nil

	default:
		return nil, 0, fmt.Errorf("%w: %v", ErrNotImplemented, in[0].Name())
	}
}

// parseTypeElems parses a line from an interface body consisting of a type-element
// union (e.g. "~int | ~int8 | ~string"). Each "|"-separated segment may be preceded
// by "~" to indicate an approximate-type constraint.
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
	for i := len(list) - 1; i >= 0; i-- {
		t := list[i]
		if len(t) == 0 {
			continue
		}
		param := ""
		if p.hasFirstParam(t) {
			origName := t[0].Str
			param = p.scopedName(origName)
			t = t[1:]
			if len(t) == 0 {
				if len(types) == 0 {
					// No type seen from the right yet. For output params, a lone ident
					// that is not in the symbol table might be a forward-declared type;
					// return ErrUndefined so the lazy fixpoint can retry.
					if flag == parseTypeOut {
						if _, _, ok := p.Symbols.Get(origName, p.scope); !ok {
							return nil, nil, false, ErrUndefined{origName}
						}
					}
					return nil, nil, false, ErrMissingType
				}
				// Type was omitted, apply the previous one from the right.
				types = append([]*vm.Type{types[0]}, types...)
				p.addSymVar(i, len(list), param, types[0], flag)
				vars = append([]string{param}, vars...)
				continue
			}
		}
		// Detect variadic parameter: ...T becomes []T.
		if len(t) > 0 && t[0].Tok == lang.Ellipsis {
			variadic = true
			t = t[1:]
		}
		typ, _, err := p.parseTypeExpr(t)
		if err != nil {
			return nil, nil, false, err
		}
		if variadic && i == len(list)-1 {
			typ = vm.SliceOf(typ)
		}
		if param != "" {
			p.addSymVar(i, len(list), param, typ, flag)
		}
		types = append([]*vm.Type{typ}, types...)
		vars = append([]string{param}, vars...)
	}
	return types, vars, variadic, err
}

func (p *Parser) addSymVar(index, nparams int, name string, typ *vm.Type, flag typeFlag) {
	if p.typeOnly {
		return
	}
	zv := vm.NewValue(typ.Rtype)
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
	} else if len(out) == 1 && out[0].Tok == lang.ParenBlock {
		if out, err = p.scanBlock(out[0].Token, false); err != nil {
			return nil, nil, nil, err
		}
	}
	ret, retNames, _, err := p.parseParamTypes(out, parseTypeOut)
	if err != nil {
		return nil, nil, nil, err
	}
	return vm.FuncOf(arg, ret, isVariadic), baseNames(argNames), baseNames(retNames), nil
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
	if isPtr {
		return vm.PointerTo(&ft), typ
	}
	return &ft, typ
}

func (p *Parser) hasFirstParam(in Tokens) bool {
	if in[0].Tok != lang.Ident {
		return false
	}
	s, _, ok := p.Symbols.Get(in[0].Str, p.scope)
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
			embedded = append(embedded, vm.EmbeddedField{FieldIdx: len(fields), Type: origType})
			fields = append(fields, f)
			tags = append(tags, tag)
			continue
		}
		types, names, _, err := p.parseParamTypes(lt, parseTypeType)
		if err != nil {
			// A lone ident that failed embedded-field lookup and param-type
			// parsing is likely a forward-declared type. Return ErrUndefined
			// so the lazy fixpoint loop can retry after the type is defined.
			if errors.Is(err, ErrMissingType) && len(lt) == 1 && lt[0].Tok == lang.Ident {
				return nil, ErrUndefined{lt[0].Str}
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
			// A struct field whose type is a placeholder (not yet finalized via SetFields)
			// means the containing struct's size cannot be computed yet. Return ErrUndefined
			// so the retry loop defers this declaration until the placeholder is finalized.
			if types[i].Rtype.Kind() == reflect.Struct && types[i].Placeholder {
				return nil, ErrUndefined{types[i].Name}
			}
			if name == "" {
				// Unnamed field: likely an embedded type not yet defined.
				return nil, ErrUndefined{types[i].Rtype.String()}
			}
			// Copy mvm-level type (preserving Params, IfaceMethods, etc.) and set field name.
			ft := *types[i]
			ft.Name = name
			ft.PkgPath = pkgPath
			// Back-link to the source type so late-bound info (Methods,
			// registered after this copy was taken when a struct is defined
			// before its methods) remains reachable via ft.Base.
			if types[i].Base != nil {
				ft.Base = types[i].Base
			} else {
				ft.Base = types[i]
			}
			fields = append(fields, &ft)
			tags = append(tags, tag)
		}
	}
	return vm.StructOf(fields, embedded, tags), nil
}
