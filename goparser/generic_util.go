package goparser

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/internal/derive"
	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/mtype"
)

func checkConstraintElem(e constraintElem, arg *mtype.Type, typeArgs []*mtype.Type) bool {
	return checkCElem(e, arg, typeArgs, nil)
}

func checkCElem(e constraintElem, arg *mtype.Type, typeArgs []*mtype.Type, seen map[*mtype.Type]bool) bool {
	switch e.kind {
	case elemAny:
		return true
	case elemComparable:
		return arg.IsComparable()
	case elemExact:
		if e.typ == nil || arg.Identical(e.typ) {
			return true
		}
		// A named constraint interface as a union term (e.g. Number in
		// string | bool | Number) contributes its own type elements.
		// Recurse at check time: the term may have been a forward placeholder
		// whose elements were filled in after the template was parsed.
		if e.typ.IsInterface() && len(e.typ.TypeElems) > 0 && !seen[e.typ] {
			if seen == nil {
				seen = map[*mtype.Type]bool{}
			}
			seen[e.typ] = true
			for _, te := range e.typ.TypeElems {
				kind := elemExact
				if te.Approx {
					kind = elemApprox
				}
				if checkCElem(constraintElem{kind: kind, typ: te.Type}, arg, typeArgs, seen) {
					return true
				}
			}
		}
		return false
	// elemInterface is handled by checkConstraint, so it never reaches here.
	case elemApprox:
		return e.typ != nil && arg.Kind() == e.typ.Kind()
	case elemTypeParamRef:
		if e.paramRef < 0 || e.paramRef >= len(typeArgs) {
			return true
		}
		return arg.Identical(typeArgs[e.paramRef])
	}
	return false
}

func isTypeParamLeaf(shape *mtype.Type, tpArgs map[string]*mtype.Type) bool {
	if shape.Name == "" || shape.PkgName != "" {
		return false
	}
	_, ok := tpArgs[shape.Name]
	return ok
}

func argElem(t *mtype.Type) *mtype.Type {
	if t.ElemType != nil {
		return t.ElemType
	}
	if t.Rtype != nil {
		return t.Elem()
	}
	return nil
}

func argKey(t *mtype.Type) *mtype.Type {
	if t.KeyType != nil {
		return t.KeyType
	}
	if t.Rtype != nil {
		return t.Key()
	}
	return nil
}

func shapeContainsTypeParam(shape *mtype.Type, tpArgs map[string]*mtype.Type) bool {
	return shapeContainsTP(shape, tpArgs, nil)
}

func shapeContainsTP(shape *mtype.Type, tpArgs map[string]*mtype.Type, seen map[*mtype.Type]bool) bool {
	if shape == nil || len(tpArgs) == 0 {
		return false
	}
	if isTypeParamLeaf(shape, tpArgs) {
		return true
	}
	if seen[shape] {
		return false
	}
	if seen == nil {
		seen = map[*mtype.Type]bool{}
	}
	seen[shape] = true
	if shapeContainsTP(shape.ElemType, tpArgs, seen) || shapeContainsTP(shape.KeyType, tpArgs, seen) {
		return true
	}
	for _, p := range shape.Params {
		if shapeContainsTP(p, tpArgs, seen) {
			return true
		}
	}
	for _, r := range shape.Returns {
		if shapeContainsTP(r, tpArgs, seen) {
			return true
		}
	}
	return false
}

func coreTypeArgMatches(shape, arg *mtype.Type, tpArgs map[string]*mtype.Type, approx bool) bool {
	return coreTypeArgMatchesSeen(shape, arg, tpArgs, approx, nil)
}

func coreTypeArgMatchesSeen(shape, arg *mtype.Type, tpArgs map[string]*mtype.Type, approx bool, seen map[*mtype.Type]bool) bool {
	if shape == nil || arg == nil {
		return true
	}
	if isTypeParamLeaf(shape, tpArgs) {
		sub := tpArgs[shape.Name]
		if sub == nil {
			return true
		}
		if approx {
			return arg.Kind() == sub.Kind()
		}
		return arg.Identical(sub)
	}
	if shape.Kind() != arg.Kind() {
		return false
	}
	if seen[shape] {
		return true
	}
	if seen == nil {
		seen = map[*mtype.Type]bool{}
	}
	seen[shape] = true
	switch shape.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Chan:
		ae := argElem(arg)
		if ae == nil {
			return true
		}
		return coreTypeArgMatchesSeen(shape.ElemType, ae, tpArgs, approx, seen)
	case reflect.Array:
		ae := argElem(arg)
		if ae == nil {
			return true
		}
		return shape.Len() == arg.Len() && coreTypeArgMatchesSeen(shape.ElemType, ae, tpArgs, approx, seen)
	case reflect.Map:
		ak, ae := argKey(arg), argElem(arg)
		if ak == nil || ae == nil {
			return true
		}
		return coreTypeArgMatchesSeen(shape.KeyType, ak, tpArgs, approx, seen) &&
			coreTypeArgMatchesSeen(shape.ElemType, ae, tpArgs, approx, seen)
	default:
		return arg.Identical(shape)
	}
}

func typeArgName(t *mtype.Type) string {
	if t.Name != "" {
		if t.IsPtr() {
			return "*" + t.Name
		}
		return t.Name
	}
	switch t.Kind() {
	case reflect.Pointer:
		if t.ElemType != nil {
			return "*" + typeArgName(t.ElemType)
		}
	case reflect.Slice:
		if t.ElemType != nil {
			return "[]" + typeArgName(t.ElemType)
		}
	case reflect.Array:
		if t.ElemType != nil {
			return "[" + strconv.Itoa(t.Len()) + "]" + typeArgName(t.ElemType)
		}
	case reflect.Map:
		if t.KeyType != nil && t.ElemType != nil {
			return "map[" + typeArgName(t.KeyType) + "]" + typeArgName(t.ElemType)
		}
	}
	return t.String()
}

func typeArgComposite(t *mtype.Type, renderLeaf func(*mtype.Type) string) string {
	return typeArgCompositeSeen(t, renderLeaf, nil)
}

func typeArgCompositeSeen(t *mtype.Type, renderLeaf func(*mtype.Type) string, seen map[*mtype.Type]bool) string {
	if seen[t] {
		return renderLeaf(t)
	}
	if seen == nil {
		seen = map[*mtype.Type]bool{}
	}
	seen[t] = true
	defer delete(seen, t)
	switch t.Kind() {
	case reflect.Pointer:
		if t.ElemType != nil {
			return "*" + typeArgCompositeSeen(t.ElemType, renderLeaf, seen)
		}
	case reflect.Slice:
		if t.ElemType != nil {
			return "[]" + typeArgCompositeSeen(t.ElemType, renderLeaf, seen)
		}
	case reflect.Array:
		if t.ElemType != nil {
			return "[" + strconv.Itoa(t.Len()) + "]" + typeArgCompositeSeen(t.ElemType, renderLeaf, seen)
		}
	case reflect.Map:
		if t.KeyType != nil && t.ElemType != nil {
			return "map[" + typeArgCompositeSeen(t.KeyType, renderLeaf, seen) + "]" + typeArgCompositeSeen(t.ElemType, renderLeaf, seen)
		}
	}
	return renderLeaf(t)
}

func sanitizeMangled(s string) string {
	ok := func(b byte) bool {
		return b == '_' || b == '#' ||
			(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
	}
	clean := true
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	b := []byte(s)
	for i := range b {
		if !ok(b[i]) {
			b[i] = '_'
		}
	}
	return string(b)
}

func mangledTypeArgName(t *mtype.Type) string {
	return typeArgComposite(t, func(leaf *mtype.Type) string {
		if leaf.Name == "" {
			return leaf.String()
		}
		if leaf.PkgName != "" {
			return leaf.PkgName + "." + leaf.Name
		}
		return leaf.Name
	})
}

func mangledName(base string, typeArgs []*mtype.Type) string {
	var sb strings.Builder
	sb.WriteString(base)
	for _, t := range typeArgs {
		sb.WriteByte('#')
		sb.WriteString(sanitizeMangled(mangledTypeArgName(t)))
	}
	return sb.String()
}

func recvGenericBaseName(recvr Tokens) (string, bool) {
	for i, t := range recvr {
		if t.Tok == lang.BracketBlock && i > 0 && recvr[i-1].Tok == lang.Ident {
			return recvr[i-1].Str, true
		}
	}
	return "", false
}

func isGenericInstance(t *mtype.Type) bool {
	return t != nil && strings.IndexByte(t.Name, '#') >= 0
}

func mangledBase(name string) string {
	base, _, _ := strings.Cut(name, "#")
	return base
}

func hasUnboundTypeParam(t *mtype.Type, tpNames map[string]bool, inferred map[string]*mtype.Type) bool {
	return hasUnboundTP(t, tpNames, inferred, nil)
}

func hasUnboundTP(t *mtype.Type, tpNames map[string]bool, inferred map[string]*mtype.Type, seen map[*mtype.Type]bool) bool {
	if t == nil {
		return false
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		return hasUnboundTP(t.ElemType, tpNames, inferred, seen)
	case reflect.Map:
		return hasUnboundTP(t.KeyType, tpNames, inferred, seen) || hasUnboundTP(t.ElemType, tpNames, inferred, seen)
	case reflect.Func:
		// Type params can be nested in a func-typed parameter.
		for _, pt := range t.Params {
			if hasUnboundTP(pt, tpNames, inferred, seen) {
				return true
			}
		}
		for _, rt := range t.Returns {
			if hasUnboundTP(rt, tpNames, inferred, seen) {
				return true
			}
		}
		return false
	case reflect.Struct:
		// Walk struct fields to surface any unbound param. seen guards self-referential shapes.
		if !isGenericInstance(t) {
			break
		}
		if seen[t] {
			return false
		}
		if seen == nil {
			seen = map[*mtype.Type]bool{}
		}
		seen[t] = true
		for _, f := range t.Fields {
			if hasUnboundTP(f, tpNames, inferred, seen) {
				return true
			}
		}
		return false
	}
	if !tpNames[t.Name] || t.PkgName != "" {
		return false
	}
	_, ok := inferred[t.Name]
	return !ok
}

func unifyTypeParam(pType, argType *mtype.Type, tpNames map[string]bool, inferred map[string]*mtype.Type) bool {
	return unifyTP(pType, argType, tpNames, inferred, nil)
}

// elemOf and keyOf return the element/key type, deriving it from Rtype when the
// symbolic field is unset (a generic-instance param can carry a concrete Rtype
// like []string with a nil ElemType, which would block element-type inference).
func elemOf(t *mtype.Type) *mtype.Type {
	if t == nil {
		return nil
	}
	if t.ElemType == nil && t.Rtype != nil {
		return t.Elem()
	}
	return t.ElemType
}

func keyOf(t *mtype.Type) *mtype.Type {
	if t == nil {
		return nil
	}
	if t.KeyType == nil && t.Rtype != nil {
		return t.Key()
	}
	return t.KeyType
}

func isUntypedConstArg(argExpr Tokens) bool {
	if len(argExpr) != 1 {
		return false
	}
	switch argExpr[0].Tok {
	case lang.Int, lang.Float, lang.Char, lang.String:
		return true
	}
	return false
}

func unifyTP(pType, argType *mtype.Type, tpNames map[string]bool, inferred map[string]*mtype.Type, seen map[*mtype.Type]bool) bool {
	if pType == nil || argType == nil {
		return false
	}
	// Recurse through composite constructors first: Name may be inherited from
	// the element (PointerTo propagates Name), so we must not leaf-match on
	// Name for a compound shape.
	switch pType.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		if argType.Kind() != pType.Kind() {
			return false
		}
		return unifyTP(pType.ElemType, elemOf(argType), tpNames, inferred, seen)
	case reflect.Map:
		if argType.Kind() != reflect.Map {
			return false
		}
		if !unifyTP(pType.KeyType, keyOf(argType), tpNames, inferred, seen) {
			return false
		}
		return unifyTP(pType.ElemType, elemOf(argType), tpNames, inferred, seen)
	case reflect.Func:
		if argType.Kind() != reflect.Func {
			return false
		}
		// ParamType/ReturnType fall back to reflect when argType is a reflect-
		// derived bridge type whose Params/Returns slices are unpopulated (e.g.
		// the return of a native stdlib func), so nested type params still unify.
		for i := range pType.Params {
			at := argType.ParamType(i)
			if at == nil {
				break
			}
			unifyTP(pType.Params[i], at, tpNames, inferred, seen)
		}
		for i := range pType.Returns {
			at := argType.ReturnType(i)
			if at == nil {
				break
			}
			unifyTP(pType.Returns[i], at, tpNames, inferred, seen)
		}
		return true
	case reflect.Struct:
		// A named generic struct instantiation (e.g. node[T]) keeps its type args
		// in its fields, so unify field-by-field against the same-shaped argument
		// struct. Both sides come from one template, so fields are parallel; the
		// base-name check rejects an unrelated struct of equal arity, and seen
		// breaks self-referential shapes (node has children []*node[T]).
		if !isGenericInstance(pType) {
			break
		}
		if argType.Kind() != reflect.Struct || len(pType.Fields) != len(argType.Fields) ||
			mangledBase(pType.Name) != mangledBase(argType.Name) {
			return false
		}
		if seen[pType] {
			return true
		}
		if seen == nil {
			seen = map[*mtype.Type]bool{}
		}
		seen[pType] = true
		ok := true
		for i := range pType.Fields {
			if !unifyTP(pType.Fields[i], argType.Fields[i], tpNames, inferred, seen) {
				ok = false
			}
		}
		return ok
	}
	// Leaf: bind if this is a type-param ident; otherwise a concrete leaf
	// with no binding to make. A pkg-qualified named type is never a type
	// param even when its bare name collides (e.g. testing.T vs param T).
	if tpNames[pType.Name] && pType.PkgName == "" {
		if _, ok := inferred[pType.Name]; !ok {
			inferred[pType.Name] = argType
		}
	}
	return true
}

func unpackConstraint(c tpConstraint, paramName string, concrete *mtype.Type) *mtype.Type {
	for _, e := range c.elems {
		if (e.kind != elemApprox && e.kind != elemExact) || e.typ == nil {
			continue
		}
		if t := extractFromShape(e.typ, concrete, paramName); t != nil {
			return t
		}
	}
	return nil
}

func extractFromShape(shape, concrete *mtype.Type, paramName string) *mtype.Type {
	if shape.Kind() == concrete.Kind() {
		switch shape.Kind() {
		case reflect.Map:
			if shape.KeyType != nil {
				if t := extractFromShape(shape.KeyType, concrete.Key(), paramName); t != nil {
					return t
				}
			}
			if shape.ElemType != nil {
				if t := extractFromShape(shape.ElemType, concrete.Elem(), paramName); t != nil {
					return t
				}
			}
		case reflect.Func:
			for i, p := range shape.Params {
				if i >= len(concrete.Params) {
					break
				}
				if t := extractFromShape(p, concrete.Params[i], paramName); t != nil {
					return t
				}
			}
			for i, r := range shape.Returns {
				if i >= len(concrete.Returns) {
					break
				}
				if t := extractFromShape(r, concrete.Returns[i], paramName); t != nil {
					return t
				}
			}
		default:
			if shape.ElemType != nil {
				if t := extractFromShape(shape.ElemType, concrete.Elem(), paramName); t != nil {
					return t
				}
			}
		}
	}
	if shape.Name == paramName && shape.ElemType == nil && shape.KeyType == nil && len(shape.Params) == 0 && len(shape.Returns) == 0 {
		return concrete
	}
	return nil
}

func coreConstraintShape(c tpConstraint) *mtype.Type {
	var shape *mtype.Type
	for _, e := range c.elems {
		if e.kind != elemExact && e.kind != elemApprox {
			continue
		}
		if e.typ == nil || shape != nil {
			return nil // a second type element: no single core type
		}
		shape = e.typ
	}
	return shape
}

func constructFromShape(shape *mtype.Type, tpSet, inferred map[string]*mtype.Type) *mtype.Type {
	if shape == nil {
		return nil
	}
	if isTypeParamLeaf(shape, tpSet) {
		return inferred[shape.Name] // nil until that param is inferred
	}
	if shape.Kind() == reflect.Pointer {
		if elem := constructFromShape(shape.ElemType, tpSet, inferred); elem != nil {
			return derive.PointerTo(elem)
		}
		return nil
	}
	// No type-param leaves remain: the shape is already a concrete type.
	if !shapeContainsTypeParam(shape, tpSet) {
		return shape
	}
	return nil
}

func funcReturnType(typ *mtype.Type) *mtype.Type {
	if len(typ.Returns) > 0 {
		return typ.Returns[0]
	}
	if typ.Kind() == reflect.Func && typ.Rtype != nil && typ.Rtype.NumOut() > 0 {
		out := typ.Rtype.Out(0)
		return &mtype.Type{Name: out.Name(), Rtype: out}
	}
	return nil
}

// boundMethodType drops the receiver (In(0)) from a method-expression rtype.
func boundMethodType(mt reflect.Type) reflect.Type {
	if mt == nil || mt.Kind() != reflect.Func || mt.NumIn() == 0 {
		return mt
	}
	in := make([]reflect.Type, 0, mt.NumIn()-1)
	for i := 1; i < mt.NumIn(); i++ {
		in = append(in, mt.In(i))
	}
	out := make([]reflect.Type, mt.NumOut())
	for i := range out {
		out[i] = mt.Out(i)
	}
	return reflect.FuncOf(in, out, mt.IsVariadic())
}
