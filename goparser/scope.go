package goparser

import (
	"fmt"
	"strconv"
	"strings"
)

func (p *Parser) scopedName(name string) string {
	return strings.TrimPrefix(p.scope+"/"+name, "/")
}

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
