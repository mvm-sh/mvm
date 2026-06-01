package goparser

import (
	"errors"
	"fmt"
	"go/constant"
	"go/token"
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

var nilValue = vm.ValueOf(nil)

func (p *Parser) parseConst(in Tokens) (out Tokens, err error) {
	if len(in) < 2 {
		return out, p.errAt(in[0], "missing expression after const")
	}
	if in[1].Tok != lang.ParenBlock {
		return p.parseConstLine(in[1:])
	}
	if in, err = p.scanBlock(in[1].Token, false); err != nil {
		return out, err
	}

	lines := in.Split(lang.Semicolon)

	// Build expanded lines (apply iota implicit repetition) and record iota values.
	type constLine struct {
		toks Tokens
		iota int64
	}
	pending := make([]constLine, 0, len(lines))
	var prev Tokens
	var iotaIdx int
	for _, lt := range lines {
		if len(lt) == 0 {
			continue
		}
		if len(lt) == 1 && iotaIdx > 0 {
			lt = append(Tokens{lt[0]}, prev...)
		}
		pending = append(pending, constLine{toks: lt, iota: int64(iotaIdx)})
		if len(lt) > 1 {
			prev = lt[1:]
		}
		iotaIdx++
	}

	// Retry until no undefined const remains, or no progress is made, so a const
	// may reference a sibling declared later in the same block.
	return parseDeferring(pending, func(cl constLine) (Tokens, error) {
		p.Symbols["iota"].Cval = constant.Make(cl.iota)
		return p.parseConstLine(cl.toks)
	})
}

// parseDeferring runs parse over each item, deferring any that fail with
// ErrUndefined and retrying until none remain or no progress is made, so a
// declaration may reference a sibling declared later.
func parseDeferring[T any](items []T, parse func(T) (Tokens, error)) (out Tokens, err error) {
	pending := items
	for len(pending) > 0 {
		var retry []T
		var firstErr error
		for _, it := range pending {
			ot, perr := parse(it)
			if perr != nil {
				var eu ErrUndefined
				if errors.As(perr, &eu) {
					retry = append(retry, it)
					if firstErr == nil {
						firstErr = perr
					}
					continue
				}
				return out, perr
			}
			out = append(out, ot...)
		}
		if len(retry) == len(pending) {
			return out, firstErr
		}
		pending = retry
	}
	return out, nil
}

func (p *Parser) parseConstLine(in Tokens) (out Tokens, err error) {
	decl := in
	var assign Tokens
	if i := decl.Index(lang.Assign); i >= 0 {
		assign = decl[i+1:]
		decl = decl[:i]
	}
	var vars []string
	var types []*vm.Type
	if types, vars, _, err = p.parseParamTypes(decl, parseTypeType); err != nil {
		if errors.Is(err, ErrMissingType) {
			for _, lt := range decl.Split(lang.Comma) {
				vars = append(vars, lt[0].Str)
				name := p.pkgKey(lt[0].Str)
				p.SymAdd(symbol.UnsetAddr, name, nilValue, symbol.Const, nil)
			}
		} else {
			return out, err
		}
	}
	values := assign.Split(lang.Comma)
	if len(values) == 1 && len(values[0]) == 0 {
		values = nil
	}
	for i, v := range values {
		if v, err = p.parseExpr(v, ""); err != nil {
			return out, err
		}
		cval, ctyp, _, err := p.evalConstExpr(v)
		if err != nil {
			// Forward references (ErrUndefined) must propagate so the
			// retry loop in parseConst / ParseAll can re-attempt later.
			var eu ErrUndefined
			if errors.As(err, &eu) {
				return out, err
			}
			// Constant overflow (e.g. `const y = int8(200)`) is a hard error:
			// propagate it rather than registering a stub, so the precise
			// message reaches the user instead of a later "undefined".
			var oe ErrConstOverflow
			if errors.As(err, &oe) {
				return out, err
			}
			// For other failures (e.g. referencing a symbol in a stub
			// binary package), register the const name so it is
			// discoverable by tools like extract.
			if i < len(vars) {
				name := p.pkgKey(vars[i])
				var typ *vm.Type
				if i < len(types) {
					typ = types[i]
				}
				p.SymSet(name, &symbol.Symbol{
					Kind:  symbol.Const,
					Index: symbol.UnsetAddr,
					Type:  typ,
					Used:  true,
				})
			}
			continue
		}
		name := p.pkgKey(vars[i])
		var typ *vm.Type
		if i < len(types) {
			typ = types[i]
			if OverflowsType(cval, typ) {
				return out, p.overflowErr(cval, typ, v[len(v)-1])
			}
			cval = constConvert(cval, typ)
		} else if ctyp != nil {
			typ = ctyp
		}
		p.SymSet(name, &symbol.Symbol{
			Kind:  symbol.Const,
			Index: symbol.UnsetAddr,
			Type:  typ,
			Cval:  cval,
			Value: vm.ValueOf(typedConstValue(cval, typ)),
			Used:  true,
		})
	}
	return out, err
}

func (p *Parser) evalConstExpr(in Tokens) (cval constant.Value, ctyp *vm.Type, length int, err error) {
	l := len(in) - 1
	if l < 0 {
		return nil, nil, 0, errors.New("missing argument in constant expression")
	}
	t := in[l]
	id := t.Tok
	switch {
	case id == lang.Period:
		if l < 1 || in[l-1].Tok != lang.Ident {
			return nil, nil, 0, errors.New("invalid package selector")
		}
		pkgName := in[l-1].Str
		s, _, ok := p.Symbols.Get(pkgName, p.scope)
		if !ok || s.Kind != symbol.Pkg {
			return nil, nil, 0, p.undef(pkgName, in[l-1])
		}
		pkg, ok := p.Packages[s.PkgPath]
		if !ok {
			return nil, nil, 0, p.errAt(t, "package not found: %s", s.PkgPath)
		}
		v, ok := pkg.Values[t.Str[1:]]
		if !ok {
			return nil, nil, 0, p.errAt(t, "symbol not found in package %s: %s", s.PkgPath, t.Str[1:])
		}
		cv, ctyp, err := constFromPkgValue(v)
		if err != nil {
			return nil, nil, 0, err
		}
		return cv, ctyp, 2, nil // consumes Ident (pkg) + Period

	case id.IsBinaryOp():
		op2, typ2, l2, err := p.evalConstExpr(in[:l])
		if err != nil {
			return nil, nil, 0, err
		}
		op1, typ1, l1, err := p.evalConstExpr(in[:l-l2])
		if err != nil {
			return nil, nil, 0, err
		}
		length = 1 + l1 + l2
		cv, ctyp, ok := FoldBinary(id, op1, typ1, op2, typ2)
		if !ok {
			return nil, nil, 0, p.errAt(t, "invalid constant operation: %s", id)
		}
		return cv, ctyp, length, err

	case id.IsUnaryOp():
		op1, typ1, l1, err := p.evalConstExpr(in[:l])
		if err != nil {
			return nil, nil, 0, err
		}
		cv, ctyp, _ := FoldUnary(id, op1, typ1)
		return cv, ctyp, 1 + l1, err

	case id.IsLiteral():
		tok := gotok[id]
		if id == lang.String && len(t.Str) > 0 && t.Str[0] == '\'' {
			tok = token.CHAR
		}
		return constant.MakeFromLiteral(t.Str, tok, 0), nil, 1, err

	case id == lang.Ident:
		s, _, ok := p.Symbols.Get(t.Str, p.scope)
		if !ok {
			return nil, nil, 0, p.undef(t.Str, t)
		}
		if s.Kind != symbol.Const {
			// A bridged constant reached through a bare name. Recover it from its reflect value.
			if s.PkgPath != "" && s.Value.IsValid() {
				if cv, ctyp, cerr := constFromPkgValue(s.Value); cerr == nil {
					return cv, ctyp, 1, nil
				}
			}
			return nil, nil, 0, errors.New("symbol is not a constant")
		}
		if s.Cval == nil {
			return nil, nil, 0, p.undef(t.Str, t)
		}
		return s.Cval, s.Type, 1, err

	case id == lang.Call:
		narg := t.Arg[0].(int)
		// unsafe.Offsetof(T{}.F): constant per Go spec. The argument is a field
		// selector on a struct literal and is not itself const-evaluable, so we
		// bypass the generic arg loop and read the type and field from the
		// token pattern directly.
		if narg == 1 && l >= 5 &&
			in[l-5].Tok == lang.Ident && in[l-5].Str == "unsafe" &&
			in[l-4].Tok == lang.Period && in[l-4].Str == ".Offsetof" &&
			in[l-2].Tok == lang.Composite &&
			in[l-1].Tok == lang.Period {
			typeName := in[l-2].Str
			fieldName := in[l-1].Str[1:]
			ts, _, ok := p.Symbols.Get(typeName, p.scope)
			if !ok || ts.Type == nil {
				return nil, nil, 0, p.undef(typeName, in[l-2])
			}
			st := ts.Type
			if st.Kind() == reflect.Pointer {
				st = st.ElemType
			}
			if st == nil || st.Kind() != reflect.Struct {
				return nil, nil, 0, fmt.Errorf("unsafe.Offsetof: %s is not a struct", typeName)
			}
			path := st.FieldIndex(fieldName)
			if path == nil {
				return nil, nil, 0, fmt.Errorf("unsafe.Offsetof: no field %s in %s", fieldName, typeName)
			}
			return constant.MakeUint64(uint64(st.FieldOffset(path))), p.Symbols["uintptr"].Type, 6, nil
		}

		// unsafe.Sizeof / unsafe.Alignof: the argument only contributes its
		// type, so pre-detect common forms whose arg isn't const-evaluable
		// (Var, composite literal, selector) before the generic args loop.
		if narg == 1 {
			if at, op, consumed, err := p.unsafeSizeArg(in, l); op != "" {
				if err != nil {
					return nil, nil, 0, err
				}
				var val uintptr
				if op == "Sizeof" {
					val = at.Size()
				} else {
					val = uintptr(at.Align())
				}
				return constant.MakeUint64(uint64(val)), p.Symbols["uintptr"].Type, consumed, nil
			}
		}

		// len/cap of an array or *array variable (bare or field access) is constant per Go spec.
		if narg == 1 {
			var fname string
			var at *vm.Type
			var n int
			switch {
			case l >= 2 && in[l-1].Tok == lang.Ident && in[l-2].Tok == lang.Ident:
				if s, _, ok := p.Symbols.Get(in[l-1].Str, p.scope); ok && s.Type != nil {
					fname, at, n = in[l-2].Str, s.Type, 3
				}
			case l >= 3 && in[l-1].Tok == lang.Period && in[l-2].Tok == lang.Ident && in[l-3].Tok == lang.Ident:
				if s, _, ok := p.Symbols.Get(in[l-2].Str, p.scope); ok && s.Type != nil {
					bt := s.Type
					if bt.Kind() == reflect.Pointer {
						bt = bt.ElemType
					}
					if bt != nil && bt.Kind() == reflect.Struct {
						if ft := bt.FieldType(in[l-1].Str[1:]); ft != nil {
							fname, at, n = in[l-3].Str, ft, 4
						}
					}
				}
			}
			if at != nil && (fname == "len" || fname == "cap") {
				if at.Kind() == reflect.Pointer {
					at = at.ElemType
				}
				if at != nil && at.Kind() == reflect.Array {
					return constant.MakeInt64(int64(at.Len())), nil, n, nil
				}
			}
		}

		args := make([]constant.Value, narg)
		var arg0Type *vm.Type // only set when i == 0, used by unsafe.Sizeof/Alignof below
		rest := in[:l]
		totalLen := 1 // Call token
		for i := narg - 1; i >= 0; i-- {
			av, at, al, err := p.evalConstExpr(rest)
			if err != nil {
				return nil, nil, 0, err
			}
			args[i] = av
			if i == 0 {
				arg0Type = at
			}
			totalLen += al
			rest = rest[:len(rest)-al]
		}

		// unsafe.Sizeof / unsafe.Alignof: constant when the argument's type is known at compile time (Go spec).
		if narg == 1 && len(rest) >= 2 &&
			rest[len(rest)-1].Tok == lang.Period && rest[len(rest)-2].Tok == lang.Ident &&
			rest[len(rest)-2].Str == "unsafe" {
			fname := rest[len(rest)-1].Str[1:]
			if fname == "Sizeof" || fname == "Alignof" {
				argTyp := arg0Type
				if argTyp == nil {
					argTyp = defaultConstType(args[0], p)
				}
				if argTyp == nil || argTyp.Kind() == reflect.Invalid {
					return nil, nil, 0, fmt.Errorf("unsafe.%s: argument has no type", fname)
				}
				var val uintptr
				if fname == "Sizeof" {
					val = argTyp.Size()
				} else {
					val = uintptr(argTyp.Align())
				}
				return constant.MakeUint64(uint64(val)), p.Symbols["uintptr"].Type, totalLen + 2 /* Ident + Period */, nil
			}
		}
		if len(rest) == 0 || rest[len(rest)-1].Tok != lang.Ident {
			return nil, nil, 0, errors.New("unsupported constant call expression")
		}
		fname := rest[len(rest)-1].Str
		totalLen++
		// Handle builtins before symbol lookup to avoid scope-walk overhead.
		if fname == "len" {
			if narg != 1 {
				return nil, nil, 0, errors.New("len: wrong number of arguments")
			}
			if args[0] != nil && args[0].Kind() == constant.String {
				return constant.MakeInt64(int64(len(constant.StringVal(args[0])))), nil, totalLen, nil
			}
			return nil, nil, 0, errors.New("len: unsupported constant argument type")
		}
		if s, _, ok := p.symGet(fname); ok && s.Kind == symbol.Type {
			if narg != 1 {
				return nil, nil, 0, errors.New("type conversion requires exactly one argument")
			}
			if OverflowsType(args[0], s.Type) {
				return nil, nil, 0, p.overflowErr(args[0], s.Type, in[l])
			}
			return constConvert(args[0], s.Type), s.Type, totalLen, nil
		}
		return nil, nil, 0, fmt.Errorf("unsupported constant call: %s", fname)

	default:
		return nil, nil, 0, p.errAt(in[l], "invalid constant expression")
	}
}

func isUnsignedKind(k reflect.Kind) bool {
	return k >= reflect.Uint && k <= reflect.Uintptr
}

// isBasicKind reports whether k is a non-composite Go basic kind.
func isBasicKind(k reflect.Kind) bool {
	return (k >= reflect.Bool && k <= reflect.Complex128) || k == reflect.String
}

// definedOverNativeComposite reports that t is a defined composite (type ipValue
// net.IP) over a NATIVE underlying with no own symbolic structure -- deferring it
// (vs the eager native Rtype) lets comp materialize from Base post-attach.
func definedOverNativeComposite(t *vm.Type) bool {
	if t.Base == nil || t.Base.Rtype == nil || t.ElemType != nil || t.KeyType != nil || len(t.Fields) != 0 {
		return false
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Chan, reflect.Map, reflect.Struct:
		return true
	}
	return false
}

// definedSymbolicComposite reports that t is a defined slice/array/chan/map
// (type flagVar []string) carrying its own symbolic structure that comp can
// rebuild from ElemType/KeyType. Deferring it (Rtype nil) instead of keeping the
// eager parse-time rtype lets the reserve path give a method-bearing one its own
// identity, so composites capturing it need no swap/cascade. Structs (handled by
// definedOverNativeComposite or the placeholder path) are intentionally excluded.
func definedSymbolicComposite(t *vm.Type) bool {
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Chan:
		return t.ElemType != nil
	case reflect.Map:
		return t.KeyType != nil && t.ElemType != nil
	}
	return false
}

// ErrConstOverflow reports a constant that cannot be represented in its type --
// the gc "constant X overflows T" compile error. It is a hard parse error so
// ParseAll does not skip past it (which would otherwise mask it as a later
// "undefined" error). ErrPos lets the diagnostic chokepoint render a snippet.
type ErrConstOverflow struct {
	Value string
	Type  string
	Loc   string
	Pos   int
}

func (e ErrConstOverflow) Error() string {
	msg := "constant " + e.Value + " overflows " + e.Type
	if e.Loc != "" {
		return e.Loc + ": " + msg
	}
	return msg
}

// ErrPos exposes the source offset so the diagnostic chokepoint can render a snippet.
func (e ErrConstOverflow) ErrPos() int { return e.Pos }

func (p *Parser) overflowErr(cv constant.Value, typ *vm.Type, tok Token) ErrConstOverflow {
	return ErrConstOverflow{Value: cv.String(), Type: typ.String(), Loc: p.Sources.FormatPos(tok.Pos), Pos: tok.Pos}
}

// OverflowsType reports whether the integer constant cv cannot be represented in
// the integer type typ -- the Go representability rule the gc compiler enforces
// as a compile error (e.g. `int8(200)` or `const x uint8 = 256`). It is meant to
// be called only at explicit-type points (conversions, typed const declarations);
// untyped constant arithmetic must NOT use it (1<<63 etc. are valid untyped).
// Non-integer constants and non-integer types return false (truncation and float
// range are handled elsewhere / left unconstrained).
func OverflowsType(cv constant.Value, typ *vm.Type) bool {
	if cv == nil || typ == nil {
		return false
	}
	k := typ.Kind()
	signed := k >= reflect.Int && k <= reflect.Int64
	unsigned := isUnsignedKind(k)
	if !signed && !unsigned {
		return false
	}
	i := constant.ToInt(cv)
	if i.Kind() != constant.Int {
		return false // not an integer constant; truncation is a separate concern
	}
	bits := uint(typ.Size()) * 8 // type sizes are small
	if unsigned {
		if constant.Sign(i) < 0 {
			return true
		}
		hiBound := constant.BinaryOp(constant.Shift(constant.MakeInt64(1), token.SHL, bits), token.SUB, constant.MakeInt64(1))
		return constant.Compare(i, token.GTR, hiBound)
	}
	hi := constant.Shift(constant.MakeInt64(1), token.SHL, bits-1)     // 2^(bits-1)
	hiBound := constant.BinaryOp(hi, token.SUB, constant.MakeInt64(1)) // 2^(bits-1)-1
	loBound := constant.UnaryOp(token.SUB, hi, 0)                      // -2^(bits-1)
	return constant.Compare(i, token.LSS, loBound) || constant.Compare(i, token.GTR, hiBound)
}

// unsafeSizeArg recognizes postfix forms of unsafe.Sizeof / unsafe.Alignof
// whose argument isn't const-evaluable but has a compile-time type:
//
//	[unsafe][.Sizeof|.Alignof][x][Call]             bare ident
//	[unsafe][.Sizeof|.Alignof][T][Composite][Call]  composite literal T{}
//	[unsafe][.Sizeof|.Alignof][x][.f][Call]         selector x.f
//
// Returns the argument's reflect.Type, the op name ("Sizeof"/"Alignof"),
// tokens-consumed-including-Call, and any lookup/resolution error. An empty
// op means no form matched; the caller falls through to the generic path.
func (p *Parser) unsafeSizeArg(in Tokens, l int) (*vm.Type, string, int, error) {
	// Locate [unsafe][.Sizeof|.Alignof] ending at either l-3 or l-4.
	var opIdx int
	switch {
	case l >= 4 && in[l-4].Tok == lang.Ident && in[l-4].Str == "unsafe" &&
		in[l-3].Tok == lang.Period && (in[l-3].Str == ".Sizeof" || in[l-3].Str == ".Alignof"):
		opIdx = l - 3
	case l >= 3 && in[l-3].Tok == lang.Ident && in[l-3].Str == "unsafe" &&
		in[l-2].Tok == lang.Period && (in[l-2].Str == ".Sizeof" || in[l-2].Str == ".Alignof"):
		opIdx = l - 2
	default:
		return nil, "", 0, nil
	}
	op := in[opIdx].Str[1:] // strip leading "."

	symType := func(tok Token, name string) (*vm.Type, error) {
		s, _, ok := p.symGet(name)
		if !ok || s.Type == nil || s.Type.Kind() == reflect.Invalid {
			return nil, p.undef(name, tok)
		}
		return s.Type, nil
	}

	if opIdx == l-3 { // composite or selector shape (5 tokens total)
		switch in[l-1].Tok {
		case lang.Composite:
			t, err := symType(in[l-1], in[l-1].Str)
			if err != nil {
				return nil, op, 0, err
			}
			return t, op, 5, nil
		case lang.Period:
			if in[l-2].Tok != lang.Ident {
				return nil, "", 0, nil
			}
			base, err := symType(in[l-2], in[l-2].Str)
			if err != nil {
				return nil, op, 0, err
			}
			bt := base
			if bt.Kind() == reflect.Pointer {
				bt = bt.ElemType
			}
			if bt == nil || bt.Kind() != reflect.Struct {
				return nil, op, 0, fmt.Errorf("unsafe.%s: %s is not a struct", op, in[l-2].Str)
			}
			field := in[l-1].Str[1:]
			_, ft := bt.FieldLookup(field)
			if ft == nil {
				return nil, op, 0, fmt.Errorf("unsafe.%s: no field %s in %s", op, field, in[l-2].Str)
			}
			return ft, op, 5, nil
		}
		return nil, "", 0, nil
	}
	// opIdx == l-2: bare ident shape (4 tokens total)
	if in[l-1].Tok != lang.Ident {
		return nil, "", 0, nil
	}
	t, err := symType(in[l-1], in[l-1].Str)
	if err != nil {
		return nil, op, 0, err
	}
	return t, op, 4, nil
}

func constValue(c constant.Value) any {
	switch c.Kind() {
	case constant.Bool:
		return constant.BoolVal(c)
	case constant.String:
		return constant.StringVal(c)
	case constant.Int:
		v, _ := constant.Int64Val(c)
		return int(v)
	case constant.Float:
		v, _ := constant.Float64Val(c)
		return v
	case constant.Complex:
		re, _ := constant.Float64Val(constant.Real(c))
		im, _ := constant.Float64Val(constant.Imag(c))
		return complex(re, im)
	}
	return nil
}

func defaultConstType(c constant.Value, p *Parser) *vm.Type {
	return DefaultConstType(c, p.Symbols)
}

// DefaultConstType returns the default type of an untyped constant (Go spec
// "Constants"): int, float64, string or bool, resolved through the given symbol
// table. Shared by the const-decl evaluator and the compiler's expression folder.
func DefaultConstType(c constant.Value, syms symbol.SymMap) *vm.Type {
	if c == nil {
		return nil
	}
	var name string
	switch c.Kind() {
	case constant.Int:
		name = "int"
	case constant.Float:
		name = "float64"
	case constant.Complex:
		name = "complex128"
	case constant.String:
		name = "string"
	case constant.Bool:
		name = "bool"
	default:
		return nil
	}
	if s, ok := syms[name]; ok {
		return s.Type
	}
	return nil
}

func typedConstValue(c constant.Value, typ *vm.Type) any {
	v := constValue(c)
	if typ == nil || v == nil {
		return v
	}
	// A materialized named type keeps the reflect.Convert path so the value
	// carries that type. Symbolic types (Rtype nil until comp) convert off the
	// kind, which already enumerates every basic type -- no reflect.Type needed.
	if typ.Rtype != nil {
		return reflect.ValueOf(v).Convert(typ.Rtype).Interface()
	}
	return basicConst(v, typ.Kind())
}

// basicConst converts a folded constant (bool/string/int/float64/complex128, as
// returned by constValue) to the Go value of basic kind k, mirroring
// reflect.Value.Convert without materializing a reflect.Type.
func basicConst(v any, k reflect.Kind) any {
	switch k {
	case reflect.Bool:
		return v
	case reflect.String:
		if i, ok := v.(int); ok {
			return string(rune(i))
		}
		return v
	case reflect.Int:
		return int(asInt64(v))
	case reflect.Int8:
		return int8(asInt64(v))
	case reflect.Int16:
		return int16(asInt64(v))
	case reflect.Int32:
		return int32(asInt64(v))
	case reflect.Int64:
		return asInt64(v)
	case reflect.Uint:
		return uint(asInt64(v))
	case reflect.Uint8:
		return uint8(asInt64(v))
	case reflect.Uint16:
		return uint16(asInt64(v))
	case reflect.Uint32:
		return uint32(asInt64(v))
	case reflect.Uint64:
		return uint64(asInt64(v))
	case reflect.Uintptr:
		return uintptr(asInt64(v))
	case reflect.Float32:
		return float32(asFloat64(v))
	case reflect.Float64:
		return asFloat64(v)
	case reflect.Complex64:
		return complex64(asComplex128(v))
	case reflect.Complex128:
		return asComplex128(v)
	}
	return v
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}

func asFloat64(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

func asComplex128(v any) complex128 {
	switch x := v.(type) {
	case int:
		return complex(float64(x), 0)
	case float64:
		return complex(x, 0)
	case complex128:
		return x
	}
	return 0
}

func constConvert(cv constant.Value, typ *vm.Type) constant.Value {
	k := typ.Kind()
	switch {
	case k >= reflect.Int && k <= reflect.Int64:
		if cv.Kind() == constant.Float {
			f, _ := constant.Float64Val(cv)
			return constant.MakeInt64(int64(f))
		}
		return constant.ToInt(cv)
	case isUnsignedKind(k):
		if cv.Kind() == constant.Float {
			f, _ := constant.Float64Val(cv)
			return constant.MakeUint64(uint64(f))
		}
		// go/constant has no ToUint; extract int64 bits for correct wraparound.
		v, _ := constant.Int64Val(constant.ToInt(cv))
		return constant.MakeUint64(uint64(v)) // intentional wraparound
	case k == reflect.Float32 || k == reflect.Float64:
		return constant.ToFloat(cv)
	case k == reflect.Complex64 || k == reflect.Complex128:
		return constant.ToComplex(cv)
	case k == reflect.String:
		if cv.Kind() == constant.Int {
			v, _ := constant.Int64Val(cv)
			return constant.MakeString(string(rune(v))) // intentional int-to-rune conversion
		}
		return cv
	}
	return cv
}

func vmValueToConst(v vm.Value) (constant.Value, error) {
	k := v.Kind()
	switch {
	case k == reflect.Bool:
		return constant.MakeBool(v.Bool()), nil
	case k >= reflect.Int && k <= reflect.Int64:
		return constant.MakeInt64(v.Int()), nil
	case isUnsignedKind(k):
		return constant.MakeUint64(v.Uint()), nil
	case k == reflect.Float32 || k == reflect.Float64:
		return constant.MakeFloat64(v.Float()), nil
	case k == reflect.String:
		return constant.MakeString(v.Reflect().String()), nil
	}
	return nil, fmt.Errorf("cannot use package value of kind %s as constant", k)
}

// constFromPkgValue folds an imported package value into a constant, preserving
// a named type (e.g. time.Duration) so typed-constant arithmetic keeps the
// right result type. Reached both via a package selector (pkg.X) and a
// dot-imported bare name.
func constFromPkgValue(v vm.Value) (constant.Value, *vm.Type, error) {
	cv, err := vmValueToConst(v)
	if err != nil {
		return nil, nil, err
	}
	var ctyp *vm.Type
	if rt := v.Type(); rt.PkgPath() != "" {
		ctyp = &vm.Type{Name: rt.Name(), Rtype: rt}
	}
	return cv, ctyp, nil
}

// Correspondence between language independent mvm tokens and Go stdlib tokens,
// To enable the use of the Go constant expression evaluator.
var gotok = map[lang.Token]token.Token{
	lang.Char:         token.CHAR,
	lang.Imag:         token.IMAG,
	lang.Int:          token.INT,
	lang.Float:        token.FLOAT,
	lang.String:       token.STRING,
	lang.Add:          token.ADD,
	lang.Sub:          token.SUB,
	lang.Mul:          token.MUL,
	lang.Quo:          token.QUO,
	lang.Rem:          token.REM,
	lang.And:          token.AND,
	lang.Or:           token.OR,
	lang.Xor:          token.XOR,
	lang.Shl:          token.SHL,
	lang.Shr:          token.SHR,
	lang.AndNot:       token.AND_NOT,
	lang.Equal:        token.EQL,
	lang.Greater:      token.GTR,
	lang.Less:         token.LSS,
	lang.GreaterEqual: token.GEQ,
	lang.LessEqual:    token.LEQ,
	lang.NotEqual:     token.NEQ,
	lang.Plus:         token.ADD,
	lang.Minus:        token.SUB,
	lang.BitComp:      token.XOR,
	lang.Not:          token.NOT,
}

// FoldBinary folds a binary constant operation following Go's untyped-constant
// rules, delegating to go/constant. xtyp/ytyp are the operand types (nil for
// untyped) and drive the unsigned right-shift reinterpretation and the result
// type. ok is false when the operation is not a valid constant operation
// (invalid shift count, or integer/float division or remainder by zero); the
// caller then falls back to a runtime op (compiler) or reports an error
// (const-decl evaluator). Shared so both paths fold identically.
func FoldBinary(op lang.Token, x constant.Value, xtyp *vm.Type, y constant.Value, ytyp *vm.Type) (constant.Value, *vm.Type, bool) {
	// && and || have no gotok entry (the compiler lowers them to jumps); decline
	// so go/constant is never asked to compare with token.ILLEGAL.
	if op.IsLogicalOp() {
		return nil, nil, false
	}
	tok := gotok[op]
	if op.IsBoolOp() {
		return constant.MakeBool(constant.Compare(x, tok, y)), nil, true
	}
	if op == lang.Shl || op == lang.Shr {
		// constant.Shift requires an integer left operand and a representable
		// non-negative count; otherwise decline so the caller emits a runtime op.
		if k := x.Kind(); k != constant.Int && k != constant.Unknown {
			return nil, nil, false
		}
		s, ok := constant.Uint64Val(y)
		if !ok {
			return nil, nil, false
		}
		cv := constant.Shift(x, tok, uint(s))
		// go/constant uses arithmetic right-shift, which sign-extends negative
		// values produced by unary ^ on unsigned constants. Reinterpret as unsigned.
		if op == lang.Shr && xtyp != nil && isUnsignedKind(xtyp.Kind()) {
			v, _ := constant.Int64Val(cv)
			cv = constant.MakeUint64(uint64(v)) // reinterpret signed bits as unsigned
		}
		return cv, xtyp, true
	}
	// Division or remainder by a zero constant is not a constant operation:
	// go/constant.BinaryOp would panic. Decline so the caller emits a runtime
	// op (which panics with Go's runtime error) or reports a compile error.
	if op == lang.Quo || op == lang.Rem {
		if k := y.Kind(); (k == constant.Int || k == constant.Float) && constant.Sign(y) == 0 {
			return nil, nil, false
		}
	}
	resTyp := xtyp
	if resTyp == nil {
		resTyp = ytyp
	}
	if tok == token.QUO && x.Kind() == constant.Int && y.Kind() == constant.Int {
		tok = token.QUO_ASSIGN // Force int result, see https://pkg.go.dev/go/constant#BinaryOp
	}
	return constant.BinaryOp(x, tok, y), resTyp, true
}

// FoldUnary folds a unary constant operation (+, -, !, ^) via go/constant.
// xtyp drives the width-limited complement for typed unsigned constants. ok is
// currently always true; it is returned for symmetry with FoldBinary.
func FoldUnary(op lang.Token, x constant.Value, xtyp *vm.Type) (constant.Value, *vm.Type, bool) {
	cv := constant.UnaryOp(gotok[op], x, 0)
	// go/constant has no unsigned integer kind: ^ on 0 gives -1 (arbitrary
	// precision), not the width-limited complement Go requires for typed
	// unsigned constants. Recompute using the correct bit width.
	if op == lang.BitComp && xtyp != nil && isUnsignedKind(xtyp.Kind()) {
		v, _ := constant.Uint64Val(x)
		bits := xtyp.Size() * 8
		mask := ^uint64(0) >> (64 - bits)
		cv = constant.MakeUint64(^v & mask)
	}
	return cv, xtyp, true
}

// ConstConvert converts a constant to the representation of typ (Go's typed
// constant conversion rules). Exported for the compiler's expression folder.
func ConstConvert(cv constant.Value, typ *vm.Type) constant.Value { return constConvert(cv, typ) }

// TypedConstValue materializes a constant into a Go value of typ (or its default
// kind when typ is nil). Exported for the compiler's expression folder.
func TypedConstValue(cv constant.Value, typ *vm.Type) any { return typedConstValue(cv, typ) }

// ConstFromExact reconstructs a constant from the exact textual form produced by
// go/constant.Value.ExactString(): a decimal integer, a float literal, or a
// "num/den" rational. It is the inverse used to recover high-precision bridged
// constants (see stdlib.ConstValues). Returns nil if s is not parseable.
func ConstFromExact(s string) constant.Value {
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	var cv constant.Value
	if i := strings.IndexByte(s, '/'); i >= 0 {
		num := constant.MakeFromLiteral(s[:i], token.INT, 0)
		den := constant.MakeFromLiteral(s[i+1:], token.INT, 0)
		if num.Kind() == constant.Unknown || den.Kind() == constant.Unknown {
			return nil
		}
		cv = constant.BinaryOp(num, token.QUO, den)
	} else {
		tok := token.INT
		if strings.ContainsAny(s, ".eEpP") {
			tok = token.FLOAT
		}
		cv = constant.MakeFromLiteral(s, tok, 0)
	}
	if cv.Kind() == constant.Unknown {
		return nil
	}
	if neg {
		cv = constant.UnaryOp(token.SUB, cv, 0)
	}
	return cv
}

func (p *Parser) parseImports(in Tokens) (out Tokens, err error) {
	if p.fname != "" {
		return out, p.errAt(in[0], "unexpected import inside function body")
	}
	if len(in) < 2 {
		return out, p.errAt(in[0], "missing import path after import")
	}
	if in[1].Tok != lang.ParenBlock {
		return p.parseImportLine(in[1:])
	}
	if in, err = p.scanBlock(in[1].Token, false); err != nil {
		return out, err
	}
	for _, li := range in.Split(lang.Semicolon) {
		ot, err := p.parseImportLine(li)
		if err != nil {
			return out, err
		}
		out = append(out, ot...)
	}
	return out, err
}

func (p *Parser) parseImportLine(in Tokens) (out Tokens, err error) {
	l := len(in)
	if l == 0 {
		return out, errors.New("empty import declaration")
	}
	// Find the import path string.
	si := l - 1
	for si >= 0 && in[si].Tok != lang.String {
		si--
	}
	if si < 0 {
		return out, p.errAt(in[0], "expected import path string, got %s", in[0].Tok)
	}
	l = si + 1 // effective length up to and including the string token
	pp := in[si].Block()
	pkg, ok := p.Packages[pp]
	if !ok {
		if err = p.importSrc(pp); err != nil {
			return out, err
		}
		pkg = p.Packages[pp]
	}
	n := in[0].Str
	if l == 1 {
		n = PackageName(pp)
	}
	if n == "." {
		// Import package symbols in the current scope.
		for k, v := range pkg.Values {
			if rtype, ok := v.UnwrapType(); ok {
				nv := vm.NewValue(rtype)
				p.SymSet(k, &symbol.Symbol{Index: symbol.UnsetAddr, Name: k, Kind: symbol.Type, PkgPath: pp, Value: nv, Type: &vm.Type{Name: rtype.Name(), Rtype: rtype}}) // mvm:symkey-ok: dot-import binds bare names
			} else if v.IsValid() {
				// Mirror the pkg-qualified value-load path (compiler.go's Period
				// handler) which tags the Symbol with its rtype, so a method
				// expression like `CommandLine.Parse(...)` finds the receiver type.
				rt := v.Type()
				p.SymSet(k, &symbol.Symbol{Index: symbol.UnsetAddr, Name: k, Kind: symbol.Value, PkgPath: pp, Value: v, Type: &vm.Type{Name: rt.Name(), Rtype: rt}}) // mvm:symkey-ok: dot-import binds bare names
			}
		}
	} else if n != "_" {
		// pkgKey-qualify so two pkgs both `import "x/y"` keep distinct alias
		// entries instead of clobbering the bare `y` key.
		// Blank-import (`import _ "path"`) is side-effect-only per Go spec and
		// must not bind a name -- registering "_" as a Pkg symbol would shadow
		// the blank identifier in tuple-assign LHS lookups.
		p.SymSet(p.pkgKey(n), &symbol.Symbol{Kind: symbol.Pkg, PkgPath: pp, Index: symbol.UnsetAddr, Name: n})
	}
	return out, err
}

func (p *Parser) parsePackageDecl(in Tokens) (out Tokens, err error) {
	if len(in) != 2 {
		return out, p.errAt(in[0], "package declaration takes one identifier")
	}
	if in[1].Tok != lang.Ident {
		return out, p.errAt(in[1], "expected package name, got %s", in[1].Tok)
	}
	// X and its external test package X_test share a parser unit under
	// `mvm test .`; accept that transition so each file's types keep their own
	// PkgPath. Any other mismatch is a genuine two-packages-in-a-dir error.
	if p.pkgName != "" && p.pkgName != in[1].Str && !isTestPkgPair(p.pkgName, in[1].Str) {
		return out, p.errAt(in[1], "package %s; expected %s", in[1].Str, p.pkgName)
	}
	p.pkgName = in[1].Str
	p.backfillPlaceholderPkgPath()
	return out, err
}

// isTestPkgPair reports whether one of a, b is the other plus "_test".
func isTestPkgPair(a, b string) bool {
	return a == b+"_test" || b == a+"_test"
}

// backfillPlaceholderPkgPath sets the PkgPath of every still-empty type
// placeholder to p.pkgName. preRegisterTypes runs before the package decl is
// parsed, so its placeholders are created with an empty PkgPath; we backfill
// here (and at the implicit "main" default in ParseDecl / parseStmt) before
// any type-body parse reads them, so generic instance mangled names stay
// consistent and fmt %T renders <pkg>.<Name> for forward-declared types.
func (p *Parser) backfillPlaceholderPkgPath() {
	if p.pkgName == "" {
		return
	}
	for _, sym := range p.Symbols {
		if sym == nil || sym.Kind != symbol.Type || sym.Type == nil {
			continue
		}
		if sym.Type.Placeholder && sym.Type.PkgPath == "" {
			sym.Type.PkgPath = p.pkgName
		}
	}
}

func (p *Parser) parseType(in Tokens) (out Tokens, err error) {
	if len(in) < 2 {
		return out, ErrMissingType
	}
	if in[1].Tok != lang.ParenBlock {
		return p.parseTypeLine(in[1:])
	}
	if in, err = p.scanBlock(in[1].Token, false); err != nil {
		return out, err
	}
	var lines []Tokens
	for _, lt := range in.Split(lang.Semicolon) {
		if len(lt) > 0 {
			lines = append(lines, lt)
		}
	}
	// Retry until no undefined type remains, or no progress is made, so a type
	// may reference a sibling declared later in the same group (issue #18).
	return parseDeferring(lines, p.parseTypeLine)
}

func (p *Parser) parseTypeLine(in Tokens) (out Tokens, err error) {
	if len(in) < 2 {
		return out, ErrMissingType
	}
	if in[0].Tok != lang.Ident {
		return out, p.errAt(in[0], "expected type name, got %s", in[0].Tok)
	}
	isAlias := in[1].Tok == lang.Assign
	toks := in[1:]
	if isAlias {
		toks = toks[1:]
	}

	// Generic type declaration: type Name[T any] struct { ... }
	// Disambiguated from array types (type T [3]int) by parseTypeParamList
	// which requires each segment to have an identifier constraint.
	if !isAlias && len(toks) > 0 && toks[0].Tok == lang.BracketBlock {
		if params, err := p.parseTypeParamList(toks[0].Token); err == nil {
			p.SymSet(p.pkgKey(in[0].Str), &symbol.Symbol{
				Kind: symbol.Generic,
				Name: in[0].Str,
				Used: true,
				Data: &genericTemplate{
					name:       in[0].Str,
					typeParams: params,
					rawTokens:  in,
					isFunc:     false,
					pkgPath:    p.importingPkg,
				},
			})
			return out, nil
		}
	}

	// For struct and interface types, use a forward-declared placeholder to
	// enable self-references and mutual references between types.
	// The key is the canonical pkgKey: "<pkgPath>.<name>" (no cross-pkg collisions).
	name := p.pkgKey(in[0].Str)
	var placeholder *vm.Type
	if !isAlias && len(toks) > 0 {
		switch toks[0].Tok {
		case lang.Struct:
			placeholder = p.registerStructPlaceholder(name, in[0].Str)
		case lang.Interface:
			placeholder = p.registerInterfacePlaceholder(name, in[0].Str)
		}
	}

	typ, _, err := p.parseTypeExpr(toks)
	if err != nil {
		return out, err
	}

	switch {
	case placeholder != nil:
		if placeholder.Kind() == reflect.Interface {
			placeholder.IfaceMethods = typ.IfaceMethods
			placeholder.TypeElems = typ.TypeElems
			placeholder.Comparable = typ.Comparable
			placeholder.Placeholder = false
		} else {
			placeholder.SetFields(typ)
		}
		// Func-local types miss backfillPlaceholderPkgPath (it runs at the
		// package decl, before any local type exists); set PkgPath here so %T
		// renders <pkg>.<Name>. Guard on empty preserves backfilled top-level
		// types and a reused placeholder.
		if placeholder.PkgPath == "" {
			placeholder.PkgPath = p.pkgName
		}
		if s, ok := p.Symbols[name]; ok {
			s.Value = typeTokenValue(placeholder)
		}
	case isAlias:
		// `type X = T` aliases share identity with T.
		p.SymAdd(symbol.UnsetAddr, name, typeTokenValue(typ), symbol.Type, typ)
	default:
		// `type X T` defines a new named type.
		// Clone so we don't mutate the source type's Name/Methods.
		nt := *typ
		nt.Name = in[0].Str
		nt.Methods = nil
		nt.Placeholder = false
		if isBasicKind(nt.Kind()) {
			// Stay symbolic: comp materializes from Base, attach adds the methods.
			nt.Rtype = nil
		}
		if nt.PkgPath == "" {
			nt.PkgPath = p.pkgName
		}
		if typ.Base != nil {
			nt.Base = typ.Base
		} else {
			nt.Base = typ
		}
		if definedOverNativeComposite(&nt) || definedSymbolicComposite(&nt) {
			nt.CaptureKind() // Kind() must survive the Rtype nil
			nt.Rtype = nil   // defer; comp materializes from the symbolic graph
		}
		p.SymAdd(symbol.UnsetAddr, name, typeTokenValue(&nt), symbol.Type, &nt)
	}
	return out, err
}

func (p *Parser) parseVar(in Tokens) (out Tokens, err error) {
	lines, err := p.varLines(in)
	if err != nil {
		return out, err
	}
	for _, lt := range lines {
		if lt, err = p.parseVarLine(lt); err != nil {
			return out, err
		}
		out = append(out, lt...)
	}
	return out, err
}

func (p *Parser) zeroInitLocals(vars []string, types []*vm.Type) (out Tokens) {
	for i, v := range vars {
		typ := types[i]
		typName := typ.Name
		if typName == "" {
			typName = typ.String()
		}
		if typ.Kind() == reflect.Pointer {
			typName = "*" + typName // Distinguish "*T" from "T".
		}
		// Resolve a symbol-table key whose Type Symbol denotes typ.
		// Identity matters: multiple pkgs can declare the same short name (e.g.
		// internal/language.Tag vs language.Tag); picking a sibling pkg's
		// same-named type would emit Fnew of the wrong type and trip reflect.Set
		// in the SetLocal below. Prefer *Type pointer identity (survives nil
		// Rtype before materialization), falling back to rtype identity for
		// native types that share a *Type clone but carry the same rtype.
		matches := func(s *symbol.Symbol) bool {
			if s == nil || s.Kind != symbol.Type || s.Type == nil {
				return false
			}
			if s.Type == typ {
				return true
			}
			return s.Type.Rtype != nil && s.Type.Rtype == typ.Rtype
		}
		typKey := ""
		if sym, sc, ok := p.symGet(typName); ok && matches(sym) {
			if sc != "" {
				typKey = sc + "/" + typName
			} else {
				typKey = typName
				for _, pfx := range [...]string{p.importingPkg, p.CompilingPkg} {
					if pfx == "" {
						continue
					}
					q := pfx + "." + typName
					if matches(p.Symbols[q]) {
						typKey = q
					}
				}
			}
		}
		if typKey == "" {
			// Fall back to any registered Type Symbol whose Rtype matches.
			// Any matching key compiles to the same global slot (typeSym
			// dedupes by Rtype pointer), so map-order non-determinism here
			// does not affect emitted bytecode.
			for k, s := range p.Symbols {
				if matches(s) {
					typKey = k
					break
				}
			}
		}
		if typKey == "" {
			// Type not yet in the symbol table; register it now at the
			// canonical pkgKey (qualified for imported pkgs, bare for main/REPL).
			typKey = p.pkgKey(typName)
			p.SymAdd(symbol.UnsetAddr, typKey, typeTokenValue(typ), symbol.Type, typ)
		}
		out = append(out, newIdent(v, 0))
		// Carry the resolved type so the compiler resolves the zero-init by
		// identity (the type's slot) rather than re-looking up typKey at compile.
		out = append(out, newIdent(typKey, 0, typ))
		out = append(out, newToken(lang.Assign, "", 0, 1))
	}
	return out
}

func (p *Parser) parseVarLine(in Tokens) (out Tokens, err error) {
	decl := in
	var assign Tokens
	if i := decl.Index(lang.Assign); i >= 0 {
		assign = decl[i+1:]
		decl = decl[:i]
	}
	var vars []string
	var types []*vm.Type
	var undefinedType bool
	if types, vars, _, err = p.parseParamTypes(decl, parseTypeVar); err != nil {
		if errors.Is(err, ErrMissingType) {
			undefinedType = true
			for _, lt := range decl.Split(lang.Comma) {
				rawName := lt[0].Str
				if rawName == "_" {
					rawName = p.blankName()
				}
				name := p.pkgKey(rawName)
				vars = append(vars, name)
				if p.funcScope == "" {
					if s, _, ok := p.symGet(lt[0].Str); !ok || s.Index == symbol.UnsetAddr {
						p.SymAdd(symbol.UnsetAddr, name, nilValue, symbol.Var, nil)
					}
					continue
				}
				p.SymAdd(p.framelen[p.funcScope], name, nilValue, symbol.LocalVar, nil)
				p.framelen[p.funcScope]++
			}
		} else {
			return out, err
		}
	}
	if len(in) > 0 && len(vars) == 0 {
		return out, p.errAt(in[0], "missing variable name in var declaration")
	}
	values := assign.Split(lang.Comma)
	if len(values) == 1 {
		if len(values[0]) == 0 {
			// No initializer: emit zero-init for typed local vars.
			if !undefinedType && p.funcScope != "" {
				out = append(out, p.zeroInitLocals(vars, types)...)
			}
			return out, err
		}
		for _, v := range vars {
			out = append(out, newIdent(v, 0))
		}
		toks, err := p.parseExpr(values[0], "")
		if err != nil {
			return out, err
		}
		out = append(out, toks...)
		if undefinedType {
			out = append(out, newToken(lang.Define, "", 0, len(vars)))
			if p.funcScope != "" && len(vars) == 1 {
				p.inferDefineType(toks, vars[0])
			}
		} else {
			out = append(out, newToken(lang.Assign, "", 0, len(vars)))
		}
		return out, err
	}
	if len(vars) != len(values) {
		return out, p.errAt(in[0], "assignment mismatch: %d variables but %d values", len(vars), len(values))
	}
	for i, v := range values {
		if v, err = p.parseExpr(v, ""); err != nil {
			return out, err
		}
		out = append(out, newIdent(vars[i], 0))
		out = append(out, v...)
		if undefinedType {
			out = append(out, newToken(lang.Define, "", 0, 1))
		} else {
			out = append(out, newToken(lang.Assign, "", 0, 1))
		}
	}
	return out, err
}
