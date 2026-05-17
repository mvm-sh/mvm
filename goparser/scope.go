package goparser

import (
	"fmt"
	"strconv"
	"strings"
)

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

// isScopedKey reports whether a symbol-table key names something inside a
// function/block scope rather than a package's top level. Lexical scopes are
// joined with '/' (see scopedName); the package qualifier joins with '.', so
// the two namespaces stay distinct.
func isScopedKey(key string) bool { return strings.ContainsRune(key, '/') }

func (p *Parser) labelName(name string) string { return p.funcScope + "/" + name }

func (p *Parser) takePendingLabel() string {
	l := p.pendingLabel
	p.pendingLabel = ""
	return l
}

func caseLabel(scope string, index, sub int) string {
	return fmt.Sprintf("%sc%d.%d", scope, index, sub)
}

func caseBodyLabel(scope string, index int) string {
	return fmt.Sprintf("%sc%d_body", scope, index)
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

func (p *Parser) pushBreakScope(prefix, pendingLabel string, hasContinue bool) func() {
	label := prefix + strconv.Itoa(p.labelCount[p.scope])
	p.labelCount[p.scope]++
	savedBreak, savedContinue := p.breakLabel, p.continueLabel
	p.pushScope(label)
	p.breakLabel = p.scope + "e"
	if hasContinue {
		p.continueLabel = p.scope + "b"
	}
	if pendingLabel != "" {
		cont := ""
		if hasContinue {
			cont = p.continueLabel
		}
		p.labeledJump[pendingLabel] = [2]string{cont, p.breakLabel}
	}
	return func() {
		p.breakLabel, p.continueLabel = savedBreak, savedContinue
		p.popScope()
	}
}
