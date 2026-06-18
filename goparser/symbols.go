package goparser

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

func (p *Parser) blankName() string {
	n := "_" + strconv.Itoa(p.blankSeq)
	p.blankSeq++
	return n
}

func (p *Parser) addLocalVar(name string) string {
	if name == "_" {
		name = p.blankName()
	}
	scoped := p.scopedName(name)
	p.SymAdd(p.framelen[p.funcScope], scoped, vm.Value{}, symbol.LocalVar, nil)
	if p.inForInit {
		p.Symbols[scoped].LoopVar = true
	}
	p.framelen[p.funcScope]++
	return scoped
}

func (p *Parser) recordDirectLocal(key string) {
	i := strings.LastIndexByte(key, '/')
	if i < 0 {
		return
	}
	if p.directLocals == nil {
		p.directLocals = map[string][]string{}
	}
	scope := key[:i]
	p.directLocals[scope] = append(p.directLocals[scope], key)
}

func (p *Parser) clearDirectLocals(scope string) {
	keys := p.directLocals[scope]
	if len(keys) == 0 {
		return
	}
	for _, k := range keys {
		if s, ok := p.Symbols[k]; ok && s.Kind == symbol.LocalVar {
			delete(p.Symbols, k)
			p.Seg.Del(k)
		}
	}
	p.directLocals[scope] = keys[:0]
}

// PurgeUnitLocals drops every scoped local-variable symbol left over from prior compile units.
func (p *Parser) PurgeUnitLocals() {
	for _, keys := range p.directLocals {
		for _, k := range keys {
			if s, ok := p.Symbols[k]; ok && s.Kind == symbol.LocalVar {
				delete(p.Symbols, k)
				p.Seg.Del(k)
			}
		}
	}
	clear(p.directLocals)
}

func (p *Parser) rebuildDirectLocals() {
	clear(p.directLocals)
	for k, s := range p.Symbols {
		if s.Kind == symbol.LocalVar {
			p.recordDirectLocal(k)
		}
	}
}

func (p *Parser) addOrRebindLocalVar(name string) string {
	if name == "_" {
		return p.addLocalVar(name)
	}
	scoped := p.scopedName(name)
	if s, ok := p.Symbols[scoped]; ok && s.Kind == symbol.LocalVar && s.Index < p.framelen[p.funcScope] {
		return scoped
	}
	return p.addLocalVar(name)
}

func (p *Parser) addPkgVar(name string) string {
	scoped := p.pkgKey(name)
	p.SymAdd(symbol.UnsetAddr, scoped, vm.Value{}, symbol.Var, nil)
	return scoped
}

func (p *Parser) addTempVar(name string) string {
	if p.funcScope != "" {
		return p.addLocalVar(name)
	}
	return p.addPkgVar(name)
}

func (p *Parser) addGlobalVar(name string) string {
	if name == "_" {
		name = p.blankName()
	}
	return p.addPkgVar(name)
}

func (p *Parser) addOrRebindGlobalVar(name string) string {
	if name == "_" {
		return p.addPkgVar(p.blankName())
	}
	scoped := p.pkgKey(name)
	if s, ok := p.Symbols[scoped]; ok && s.Kind == symbol.Var {
		return scoped
	}
	return p.addPkgVar(name)
}

func (p *Parser) setLHSType(i int, t *vm.Type, lhs []Tokens, lhsPositions []int, out Tokens) {
	if t == nil || i >= len(lhs) || len(lhs[i]) != 1 || lhs[i][0].Tok != lang.Ident || lhs[i][0].Str == "_" {
		return
	}
	sym := p.Symbols[out[lhsPositions[i]].Str]
	if sym == nil || sym.Type != nil {
		return
	}
	sym.Type = t
}

func (p *Parser) inferRangeTypes(operand Tokens, lhs []Tokens, lhsPositions []int, out Tokens) {
	rt, _ := p.postfixType(operand)
	if rt == nil {
		return
	}
	setType := func(i int, t *vm.Type) { p.setLHSType(i, t, lhs, lhsPositions, out) }
	switch rt.Kind() {
	case reflect.Slice, reflect.Array, reflect.String:
		setType(0, p.Symbols["int"].Type)
		if rt.Kind() == reflect.String {
			setType(1, p.Symbols["rune"].Type)
		} else {
			setType(1, rt.Elem())
		}
	case reflect.Map:
		setType(0, rt.Key())
		setType(1, rt.Elem())
	case reflect.Chan:
		setType(0, rt.Elem())
	}
}

func (p *Parser) inferDefineType(rhs Tokens, scopedName string) {
	sym := p.Symbols[scopedName]
	if sym == nil || sym.Type != nil {
		return // not found, or type already set
	}
	n := len(rhs)
	if n == 0 {
		return
	}
	// Check for &T{} (Addr at end, Composite before it) or T{} (Composite at end);
	// resolve via the named type so a forward-declared type postfixType can't yet
	// see still works.
	hasAddr := rhs[n-1].Tok == lang.Addr
	compositeIdx := n - 1
	if hasAddr {
		compositeIdx = n - 2
	}
	if compositeIdx >= 0 && rhs[compositeIdx].Tok == lang.Composite && rhs[compositeIdx].Str != "" {
		if s, _, ok := p.Symbols.Get(rhs[compositeIdx].Str, p.scope); ok && s.Kind == symbol.Type && s.Type != nil {
			if hasAddr {
				sym.Type = vm.SymPtr(s.Type)
			} else {
				sym.Type = s.Type
			}
			return
		}
	}
	// General fallback: type the RHS expression itself (make/new, slice-expr,
	// index, etc.) so a later generic call can infer its type params from this
	// local. postfixType is pure - rhs is already-parsed postfix.
	if t, _ := p.postfixType(rhs); t != nil {
		sym.Type = t
	}
}

func (p *Parser) inferCallDefineTypes(rhs Tokens, lhs []Tokens, lhsPositions []int, out Tokens) {
	ft := p.callFuncType(rhs)
	if ft == nil {
		// Builtin or conversion call (make([]T, n), T(x), ...): no func symbol to
		// read a return tuple from. Type a single LHS local from the whole
		// expression so a later generic call can infer its type params.
		if len(lhs) == 1 {
			if t, _ := p.postfixType(rhs); t != nil {
				p.setLHSType(0, t, lhs, lhsPositions, out)
			}
		}
		return
	}
	for i := 0; i < len(lhs) && i < ft.NumOut(); i++ {
		p.setLHSType(i, ft.ReturnType(i), lhs, lhsPositions, out)
	}
}

func (p *Parser) callFuncType(in Tokens) *vm.Type {
	l := len(in) - 1
	if l < 0 || in[l].Tok != lang.Call {
		return nil
	}
	narg := in[l].Arg[0].(int)
	rest := in[:l]
	for range narg {
		_, al := p.postfixType(rest)
		if al == 0 {
			return nil
		}
		rest = rest[:len(rest)-al]
	}
	if len(rest) == 0 {
		return nil
	}
	callee := rest[len(rest)-1]
	switch callee.Tok {
	case lang.Ident:
		// Vtype derives the func type from the reflect Value for bridged/dot-
		// imported symbols, whose Type field is nil but Value holds the func.
		if s, _, ok := p.Symbols.Get(callee.Str, p.scope); ok {
			if ft := symbol.Vtype(s); ft.IsFunc() {
				return ft
			}
		}
	case lang.Period:
		// pkg-qualified call `pkg.Func(...)`: callee is the Period selector,
		// preceded by the package identifier.
		if len(rest) < 2 || rest[len(rest)-2].Tok != lang.Ident {
			return nil
		}
		pre := rest[len(rest)-2]
		ps := p.Symbols[pre.Str]
		if as, _, ok := p.pkgAlias(pre.Str, pre.Pos); ok {
			ps = as
		}
		if ps == nil || ps.Kind != symbol.Pkg {
			return nil
		}
		member := callee.Str[1:] // strip leading "."
		if pkg := p.Packages[ps.PkgPath]; pkg != nil {
			if v, ok := pkg.Values[member]; ok {
				if rv := v.Reflect(); rv.IsValid() && rv.Kind() == reflect.Func {
					return &vm.Type{Rtype: rv.Type()}
				}
			}
		}
		if qs, ok := p.Symbols[ps.PkgPath+"."+member]; ok {
			if ft := symbol.Vtype(qs); ft.IsFunc() {
				return ft
			}
		}
	}
	return nil
}

func (p *Parser) rollbackSymTracker() {
	for _, k := range p.symTracker {
		delete(p.Symbols, k)
		p.Seg.Del(k)
	}
	p.symTracker = nil
}
