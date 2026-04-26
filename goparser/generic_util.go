package goparser

import (
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/vm"
)

// checkConstraintElem reports whether arg satisfies a single constraint element.
// For Approx with composite kinds (slice, map, array, ...), only Kind is checked
// - tightening would require inter-param substitution.
func checkConstraintElem(e constraintElem, arg *vm.Type, typeArgs []*vm.Type) bool {
	switch e.kind {
	case elemAny:
		return true
	case elemComparable:
		return arg.Rtype.Comparable()
	case elemExact:
		return e.typ == nil || arg.Rtype == e.typ.Rtype
	case elemInterface:
		// Mvm-parsed interfaces have Rtype=any so Implements is trivially
		// true - acceptable because their type-element form is already flattened
		// into sibling elems at resolution time.
		return e.typ == nil || arg.Rtype.Implements(e.typ.Rtype)
	case elemApprox:
		return e.typ != nil && arg.Rtype.Kind() == e.typ.Rtype.Kind()
	case elemTypeParamRef:
		if e.paramRef < 0 || e.paramRef >= len(typeArgs) {
			return true
		}
		return arg.Rtype == typeArgs[e.paramRef].Rtype
	}
	return false
}

// tokensSource reconstructs the original source text from tokens; used to
// preserve package qualifiers (e.g. "netip.Prefix") that *vm.Type.Name drops.
func tokensSource(toks Tokens) string {
	if len(toks) == 1 {
		return toks[0].Str
	}
	var sb strings.Builder
	for _, t := range toks {
		sb.WriteString(t.Str)
	}
	return sb.String()
}

// isSimpleIdent reports whether s is a plain Go identifier (letters, digits, underscore).
func isSimpleIdent(s string) bool {
	for _, r := range s {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return len(s) > 0
}

// typeArgName returns the source-level name for a concrete type argument.
// For pointer types, PointerTo stores just the element name (e.g. "int"
// for *int), so we prepend "*" to produce the correct name.
func typeArgName(t *vm.Type) string {
	name := t.Name
	if name == "" {
		return t.Rtype.String()
	}
	if t.IsPtr() {
		return "*" + name
	}
	return name
}

// typeArgSubst returns the text used to substitute a type parameter in the
// template body. Prefers source (preserves package qualifiers like
// "netip.Prefix"); falls back to typeArgName when source is empty.
func typeArgSubst(t *vm.Type, source string) string {
	if source != "" {
		return source
	}
	return typeArgName(t)
}

// mangledName returns the mangled name for a generic instantiation.
// E.g. mangledName("Max", [int]) -> "Max#int".
func mangledName(base string, typeArgs []*vm.Type) string {
	var sb strings.Builder
	sb.WriteString(base)
	for _, t := range typeArgs {
		sb.WriteByte('#')
		sb.WriteString(typeArgName(t))
	}
	return sb.String()
}

// recvGenericBaseName returns the base type name from a generic receiver
// (the Ident immediately preceding the BracketBlock).
func recvGenericBaseName(recvr Tokens) (string, bool) {
	for i, t := range recvr {
		if t.Tok == lang.BracketBlock && i > 0 && recvr[i-1].Tok == lang.Ident {
			return recvr[i-1].Str, true
		}
	}
	return "", false
}

// hasUnboundTypeParam reports whether t mentions any type parameter in tpNames
// that isn't yet in inferred, at any depth (pointer/slice/array/chan/map).
func hasUnboundTypeParam(t *vm.Type, tpNames map[string]bool, inferred map[string]*vm.Type) bool {
	if t == nil {
		return false
	}
	switch t.Rtype.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		return hasUnboundTypeParam(t.ElemType, tpNames, inferred)
	case reflect.Map:
		return hasUnboundTypeParam(t.KeyType, tpNames, inferred) || hasUnboundTypeParam(t.ElemType, tpNames, inferred)
	}
	if !tpNames[t.Name] {
		return false
	}
	_, ok := inferred[t.Name]
	return !ok
}

// unifyTypeParam walks pType (from a generic signature, possibly containing
// type-param idents) and argType in parallel, binding each encountered
// type-param ident to the corresponding sub-type of argType. Returns false
// if the shapes don't match. First-seen binding wins (no conflict checking).
func unifyTypeParam(pType, argType *vm.Type, tpNames map[string]bool, inferred map[string]*vm.Type) bool {
	if pType == nil || argType == nil {
		return false
	}
	// Recurse through composite constructors first: Name may be inherited from
	// the element (PointerTo propagates Name), so we must not leaf-match on
	// Name for a compound shape.
	switch pType.Rtype.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		if argType.Rtype.Kind() != pType.Rtype.Kind() {
			return false
		}
		return unifyTypeParam(pType.ElemType, argType.ElemType, tpNames, inferred)
	case reflect.Map:
		if argType.Rtype.Kind() != reflect.Map {
			return false
		}
		if !unifyTypeParam(pType.KeyType, argType.KeyType, tpNames, inferred) {
			return false
		}
		return unifyTypeParam(pType.ElemType, argType.ElemType, tpNames, inferred)
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

// unpackConstraint tries to extract a concrete type for paramName by matching
// the inferred concrete type against the shape of one of c's approx/exact
// constraint elements. Returns nil if no element pins paramName.
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

// extractFromShape walks `shape` in parallel with `concrete`, returning the
// sub-type of concrete at the position where shape names paramName. E.g.
// shape=[]E, concrete=[]int, paramName=E -> int. Handles slice, array,
// pointer, chan (via ElemType), map (via KeyType + ElemType), and func
// (via Params + Returns). Decomposes before matching by name so that
// composite shapes whose outer Name happens to collide with paramName
// (e.g. PointerTo sets Name=ElemName) don't short-circuit.
func extractFromShape(shape, concrete *vm.Type, paramName string) *vm.Type {
	if shape.Rtype.Kind() == concrete.Rtype.Kind() {
		switch shape.Rtype.Kind() {
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

// funcReturnType returns the first return type of a function type.
func funcReturnType(typ *vm.Type) *vm.Type {
	if len(typ.Returns) > 0 {
		return typ.Returns[0]
	}
	if typ.Rtype.Kind() == reflect.Func && typ.Rtype.NumOut() > 0 {
		out := typ.Rtype.Out(0)
		return &vm.Type{Name: out.Name(), Rtype: out}
	}
	return nil
}
