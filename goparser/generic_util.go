package goparser

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/vm"
)

func checkConstraintElem(e constraintElem, arg *vm.Type, typeArgs []*vm.Type) bool {
	switch e.kind {
	case elemAny:
		return true
	case elemComparable:
		return arg.Rtype.Comparable()
	case elemExact:
		return e.typ == nil || arg.Rtype == e.typ.Rtype
	// elemInterface is handled by checkConstraint (it needs the parser's symbol
	// table to see interpreted method sets), so it never reaches here.
	case elemApprox:
		return e.typ != nil && arg.Kind() == e.typ.Kind()
	case elemTypeParamRef:
		if e.paramRef < 0 || e.paramRef >= len(typeArgs) {
			return true
		}
		return arg.Rtype == typeArgs[e.paramRef].Rtype
	}
	return false
}

func typeArgName(t *vm.Type) string {
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
			return "[" + strconv.Itoa(t.Rtype.Len()) + "]" + typeArgName(t.ElemType)
		}
	case reflect.Map:
		if t.KeyType != nil && t.ElemType != nil {
			return "map[" + typeArgName(t.KeyType) + "]" + typeArgName(t.ElemType)
		}
	}
	return t.Rtype.String()
}

func typeArgComposite(t *vm.Type, renderLeaf func(*vm.Type) string) string {
	switch t.Kind() {
	case reflect.Pointer:
		if t.ElemType != nil {
			return "*" + typeArgComposite(t.ElemType, renderLeaf)
		}
	case reflect.Slice:
		if t.ElemType != nil {
			return "[]" + typeArgComposite(t.ElemType, renderLeaf)
		}
	case reflect.Array:
		if t.ElemType != nil {
			return "[" + strconv.Itoa(t.Rtype.Len()) + "]" + typeArgComposite(t.ElemType, renderLeaf)
		}
	case reflect.Map:
		if t.KeyType != nil && t.ElemType != nil {
			return "map[" + typeArgComposite(t.KeyType, renderLeaf) + "]" + typeArgComposite(t.ElemType, renderLeaf)
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

func mangledTypeArgName(t *vm.Type) string {
	return typeArgComposite(t, func(leaf *vm.Type) string {
		if leaf.Name == "" {
			return leaf.Rtype.String()
		}
		if leaf.PkgPath != "" {
			return leaf.PkgPath + "." + leaf.Name
		}
		return leaf.Name
	})
}

func mangledName(base string, typeArgs []*vm.Type) string {
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

func hasUnboundTypeParam(t *vm.Type, tpNames map[string]bool, inferred map[string]*vm.Type) bool {
	if t == nil {
		return false
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		return hasUnboundTypeParam(t.ElemType, tpNames, inferred)
	case reflect.Map:
		return hasUnboundTypeParam(t.KeyType, tpNames, inferred) || hasUnboundTypeParam(t.ElemType, tpNames, inferred)
	case reflect.Func:
		// Type params can be nested in a func-typed parameter, e.g.
		// slices.Collect[E](seq iter.Seq[E]) where iter.Seq[E] is
		// func(func(E) bool).
		for _, pt := range t.Params {
			if hasUnboundTypeParam(pt, tpNames, inferred) {
				return true
			}
		}
		for _, rt := range t.Returns {
			if hasUnboundTypeParam(rt, tpNames, inferred) {
				return true
			}
		}
		return false
	}
	if !tpNames[t.Name] {
		return false
	}
	_, ok := inferred[t.Name]
	return !ok
}

func unifyTypeParam(pType, argType *vm.Type, tpNames map[string]bool, inferred map[string]*vm.Type) bool {
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
		return unifyTypeParam(pType.ElemType, argType.ElemType, tpNames, inferred)
	case reflect.Map:
		if argType.Kind() != reflect.Map {
			return false
		}
		if !unifyTypeParam(pType.KeyType, argType.KeyType, tpNames, inferred) {
			return false
		}
		return unifyTypeParam(pType.ElemType, argType.ElemType, tpNames, inferred)
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
			unifyTypeParam(pType.Params[i], at, tpNames, inferred)
		}
		for i := range pType.Returns {
			at := argType.ReturnType(i)
			if at == nil {
				break
			}
			unifyTypeParam(pType.Returns[i], at, tpNames, inferred)
		}
		return true
	}
	// Leaf: bind if this is a type-param ident; otherwise a concrete leaf
	// with no binding to make.
	if tpNames[pType.Name] {
		if _, ok := inferred[pType.Name]; !ok {
			inferred[pType.Name] = argType
		}
	}
	return true
}

func unpackConstraint(c tpConstraint, paramName string, concrete *vm.Type) *vm.Type {
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

func extractFromShape(shape, concrete *vm.Type, paramName string) *vm.Type {
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

func funcReturnType(typ *vm.Type) *vm.Type {
	if len(typ.Returns) > 0 {
		return typ.Returns[0]
	}
	if typ.Kind() == reflect.Func && typ.Rtype.NumOut() > 0 {
		out := typ.Rtype.Out(0)
		return &vm.Type{Name: out.Name(), Rtype: out}
	}
	return nil
}
