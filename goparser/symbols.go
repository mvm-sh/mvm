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

// clearDirectLocals removes LocalVar entries at scope's direct level (keys
// "scope/<name>" with no further "/"). Used at parseFunc entry to drop leftovers
// from a prior parse that wrote to the same funcScope key -- see the call site.
func (p *Parser) clearDirectLocals(scope string) {
	prefix := scope + "/"
	for k, s := range p.Symbols {
		if s.Kind != symbol.LocalVar || !strings.HasPrefix(k, prefix) {
			continue
		}
		if strings.IndexByte(k[len(prefix):], '/') >= 0 {
			continue
		}
		delete(p.Symbols, k)
	}
}

// addOrRebindLocalVar returns the scoped key for a `:=` LHS ident, preserving
// an existing same-scope LocalVar (named return, param, or prior `:=`) instead
// of overwriting it with a fresh Type=nil entry. Go's short-var-decl rebinds
// existing names when at least one LHS ident is new in the same block.
//
// `Index < framelen` rejects a stale entry left by a prior parse of a
// different package's same-named method: funcScope is the bare function
// name, so cross-pkg method namesakes share scoped keys for their locals.
// Valid rebinds (named return, param, prior `:=` in this parse) have
// Index < framelen by construction.
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

// setLHSType assigns t to the i-th `:=` LHS local when it is a fresh, named,
// non-blank, currently-untyped define target. Shared by the range- and
// call-define inference paths so the (subtle) skip rules live in one place.
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
				sym.Type = vm.PointerTo(s.Type)
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

// inferCallDefineTypes sets the static Type of freshly-defined `:=` LHS locals
// from the return tuple of a call RHS, e.g. `bad, good := DumpTables(p)` types
// both as []int. Without this, locals bound from a call have a nil Type and
// later generic type inference (inferExprType) can't resolve them.
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
	for i := 0; i < len(lhs) && i < ft.Rtype.NumOut(); i++ {
		p.setLHSType(i, ft.ReturnType(i), lhs, lhsPositions, out)
	}
}

// callFuncType returns the function type invoked by a postfix expression
// ending in a Call token, or nil if the callee isn't a resolvable function
// identifier (e.g. a type conversion, builtin, or pkg-qualified selector).
// It mirrors postfixType's Call arg-walking to locate the callee token.
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
		ps := p.Symbols[rest[len(rest)-2].Str]
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
	}
	p.symTracker = nil
}
