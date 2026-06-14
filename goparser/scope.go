package goparser

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// fileScopedAliasKey is the symbol-table key for an import alias scoped to the
// source file at idx. '@' cannot appear in a Go identifier, so the key never
// collides with a real name or a generic-mangled name.
func fileScopedAliasKey(name string, idx int) string { return name + "@" + strconv.Itoa(idx) }

// pkgAlias reports a file-scoped package alias ONLY when the package-wide
// lookup would resolve name to a DIFFERENT package than the source file at pos
// imported it as -- i.e. a sibling file aliased the same name elsewhere and the
// shared bare key now points at the wrong package. In that case it returns the
// file's own Pkg symbol and the file-scoped key to bind it under. In every other
// case (no collision, single importer, pseudo-packages) it returns false, so the
// caller's existing resolution runs unchanged.
func (p *Parser) pkgAlias(name string, pos int) (sym *symbol.Symbol, key string, ok bool) {
	idx := p.Sources.SourceIndex(pos)
	if idx < 0 {
		return nil, "", false
	}
	s := p.fileAliases[idx][name]
	if s == nil {
		return nil, "", false
	}
	if bare, _, found := p.symGet(name); found && bare == s {
		return nil, "", false // no collision: shared key already resolves here
	}
	return s, fileScopedAliasKey(name, idx), true
}

func (p *Parser) scopedName(name string) string {
	return strings.TrimPrefix(p.scope+"/"+name, "/")
}

// pkgKey returns the canonical symbol-table key for a top-level name in the
// current parser context. While parseSrc is running for an imported package
// (importingPkg set) at top level, returns "<importingPkg>.<name>". Otherwise
// falls back to scopedName, which yields the bare name at top level (main pkg
// / REPL, where there's no package qualifier) or "<scope>/<name>" inside a
// function/block. Type symbols use this so each pkg's top-level types live
// under their own canonical key from definition time and aren't clobbered by
// sibling imports' bare-key writes (the long-standing "bare-key fragility"
// problem; see [project_canonical_pkg_qualified_symbols] memory). Predeclared
// names (int, string, error, ...) stay at their bare key in the main pkg /
// REPL path -- pkgKey is a no-op there. Generic instances (mangled names
// containing '#', e.g. "Seq2#int#E") also stay bare: the mangling already
// makes them globally unique and they are intentionally deduped cross-pkg
// (see generic.go's `Symbols.Get(mname, "")` guard).
func (p *Parser) pkgKey(name string) string {
	if p.importingPkg != "" && p.scope == "" && !strings.ContainsRune(name, '#') {
		return QualifyName(p.importingPkg, name)
	}
	return p.scopedName(name)
}

// pkgShortName returns the declared (short) name of the package at full import
// path, falling back to the last path segment when the package isn't loaded.
func (p *Parser) pkgShortName(path string) string {
	if pkg, ok := p.Packages[path]; ok && pkg.Name != "" {
		return pkg.Name
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// typeBelongsTo reports whether named typ is a member of pkgPath. typ.PkgPath
// carries the type's own (short) package name; an empty PkgPath is treated as
// belonging (an unqualified local type keeps the old qualify-by-context path).
func (p *Parser) typeBelongsTo(typ *vm.Type, pkgPath string) bool {
	if typ.PkgPath == "" {
		return true
	}
	return typ.PkgPath == pkgPath || typ.PkgPath == p.pkgShortName(pkgPath)
}

// isScopedKey reports whether a symbol-table key names something inside a
// function/block scope rather than a package's top level. Lexical scopes are
// joined with '/' (see scopedName); the package qualifier joins with '.', so
// the two namespaces stay distinct.
func isScopedKey(key string) bool { return strings.ContainsRune(key, '/') }

// The "#" keeps label keys disjoint from variable keys: Go labels live in
// their own namespace, and identifiers cannot contain '#'.
func (p *Parser) labelName(name string) string { return p.funcScope + "/#" + name }

func (p *Parser) takePendingLabel() string {
	l := p.pendingLabel
	p.pendingLabel = ""
	return l
}

// synthLabel returns a synthetic jump-label key; the "#" keeps it disjoint
// from variable keys, as in labelName.
func synthLabel(scope, suffix string) string { return scope + "#" + suffix }

func caseLabel(scope string, index, sub int) string {
	return synthLabel(scope, fmt.Sprintf("c%d.%d", index, sub))
}

func caseBodyLabel(scope string, index int) string {
	return synthLabel(scope, fmt.Sprintf("c%d_body", index))
}

// propagateCapture appends name to FreeVars of every enclosing function scope
// in p.funcScope, walking outward and stopping before definingScope (the
// variable's owning function). Each function's Symbol-table key is the last
// segment of its scope (matches anon "#..." closures keyed bare and named
// functions/methods alike). Non-function scope segments (block/case/for-loop)
// are skipped via the framelen check; without it a synthetic block name could
// alias a top-level Symbol.
func (p *Parser) propagateCapture(name, definingScope string) {
	for cur := p.funcScope; cur != "" && cur != definingScope; {
		j := strings.LastIndex(cur, "/")
		if _, isFunc := p.framelen[cur]; isFunc {
			cloKey := cur
			if j >= 0 {
				cloKey = cur[j+1:]
			}
			if strings.HasPrefix(cloKey, "#") && !isInitFname(cloKey) {
				cloKey = p.anonFuncKey(cloKey)
			}
			if cloSym, ok := p.Symbols[cloKey]; ok && cloSym != nil && cloSym.FreeVarIndex(name) < 0 {
				cloSym.FreeVars = append(cloSym.FreeVars, name)
			}
		}
		if j < 0 {
			break
		}
		cur = cur[:j]
	}
}

func (p *Parser) pushScope(name string) {
	if p.scope != "" {
		p.scope += "/"
	}
	p.scope += name
}

func (p *Parser) popScope() {
	j := strings.LastIndex(p.scope, "/")
	if j == -1 {
		j = 0
	}
	p.scope = p.scope[:j]
}

// ctrlFrame is one active for/switch/select. stop is set only for range loops,
// letting a labeled break/continue unwind the iterators of crossed ranges.
type ctrlFrame struct {
	userLabel   string // labelName form, "" if none
	hasContinue bool   // for-loops only (continue targets)
	stop        *Token // range Stop token, else nil
}

func (p *Parser) pushBreakScope(prefix, pendingLabel string, hasContinue bool) func() {
	label := prefix + strconv.Itoa(p.labelCount[p.scope])
	p.labelCount[p.scope]++
	savedBreak, savedContinue := p.breakLabel, p.continueLabel
	p.pushScope(label)
	p.breakLabel = synthLabel(p.scope, "e")
	if hasContinue {
		p.continueLabel = synthLabel(p.scope, "b")
	}
	if pendingLabel != "" {
		cont := ""
		if hasContinue {
			cont = p.continueLabel
		}
		p.labeledJump[pendingLabel] = [2]string{cont, p.breakLabel}
	}
	p.ctrlStack = append(p.ctrlStack, ctrlFrame{userLabel: pendingLabel, hasContinue: hasContinue})
	return func() {
		p.ctrlStack = p.ctrlStack[:len(p.ctrlStack)-1]
		p.breakLabel, p.continueLabel = savedBreak, savedContinue
		p.popScope()
	}
}

// markRangeStop sets the range Stop on the innermost (currently parsing) frame.
func (p *Parser) markRangeStop(stop Token) {
	p.ctrlStack[len(p.ctrlStack)-1].stop = &stop
}

// ctrlIndexByLabel finds the active frame with the given label, or -1.
func (p *Parser) ctrlIndexByLabel(key string) int {
	for i, v := range slices.Backward(p.ctrlStack) {
		if v.userLabel == key {
			return i
		}
	}
	return -1
}

// innermostContinueIndex returns the innermost for-loop frame, or -1.
func (p *Parser) innermostContinueIndex() int {
	for i, v := range slices.Backward(p.ctrlStack) {
		if v.hasContinue {
			return i
		}
	}
	return -1
}

// rangeStopsAbove returns the Stop tokens for range loops nested inside
// ctrlStack[targetIdx], innermost first.
func (p *Parser) rangeStopsAbove(targetIdx int) Tokens {
	if targetIdx < 0 {
		return nil
	}
	var out Tokens
	for i := len(p.ctrlStack) - 1; i > targetIdx; i-- {
		if s := p.ctrlStack[i].stop; s != nil {
			out = append(out, *s)
		}
	}
	return out
}
