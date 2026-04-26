package goparser

import (
	"reflect"
	"strconv"

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

// addTempVar adds a temporary variable appropriate for the current scope.
// At function scope it creates a local variable; at top level it creates a
// global variable whose slot is allocated later by allocGlobalSlots.
func (p *Parser) addTempVar(name string) string {
	if p.funcScope != "" {
		return p.addLocalVar(name)
	}
	scoped := p.scopedName(name)
	p.SymAdd(symbol.UnsetAddr, scoped, vm.Value{}, symbol.Var, nil)
	return scoped
}

func (p *Parser) addGlobalVar(name string) string {
	if name == "_" {
		name = p.blankName()
	}
	scoped := p.scopedName(name)
	p.SymAdd(symbol.UnsetAddr, scoped, vm.Value{}, symbol.Var, nil)
	return scoped
}

// inferRangeTypes populates .Type for range LHS symbols using the range
// operand's postfix tokens. Without this, range vars stay Type=nil at parse
// time, which breaks generic type inference for calls like cmp.Compare(v, w)
// inside a generic body where v comes from `for _, v := range s`.
func (p *Parser) inferRangeTypes(operand Tokens, lhs []Tokens, lhsPositions []int, out Tokens) {
	rt, _ := p.postfixType(operand)
	if rt == nil {
		return
	}
	setType := func(i int, t *vm.Type) {
		if t == nil || i >= len(lhs) || len(lhs[i]) != 1 || lhs[i][0].Tok != lang.Ident || lhs[i][0].Str == "_" {
			return
		}
		sym := p.Symbols[out[lhsPositions[i]].Str]
		if sym == nil || sym.Type != nil {
			return
		}
		sym.Type = t
	}
	switch rt.Rtype.Kind() {
	case reflect.Slice, reflect.Array, reflect.String:
		setType(0, p.Symbols["int"].Type)
		if rt.Rtype.Kind() == reflect.String {
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
	// Check for &T{} (Addr at end, Composite before it) or T{} (Composite at end).
	hasAddr := rhs[n-1].Tok == lang.Addr
	compositeIdx := n - 1
	if hasAddr {
		compositeIdx = n - 2
	}
	if compositeIdx < 0 || rhs[compositeIdx].Tok != lang.Composite || rhs[compositeIdx].Str == "" {
		return
	}
	s, _, ok := p.Symbols.Get(rhs[compositeIdx].Str, p.scope)
	if !ok || s.Kind != symbol.Type || s.Type == nil {
		return
	}
	if hasAddr {
		sym.Type = vm.PointerTo(s.Type)
	} else {
		sym.Type = s.Type
	}
}

func (p *Parser) rollbackSymTracker() {
	for _, k := range p.symTracker {
		delete(p.Symbols, k)
	}
	p.symTracker = nil
}
