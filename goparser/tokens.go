package goparser

import (
	"strconv"
	"strings"

	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/scan"
	"github.com/mvm-sh/mvm/vm"
)

// Token represents a parser token.
type Token struct {
	scan.Token
	Arg []any
}

// Tokens represents slice of tokens.
type Tokens []Token

// DeferredDecl is a top-level declaration (func body, var initializer) whose
// code generation is deferred to Phase 2, tagged with the import path of the
// package it came from ("" for the main package / REPL). The tag lets Phase 2
// resolve unqualified identifiers against the originating package's symbols,
// which matters when a sibling import shadowed a bare name in the symbol table.
type DeferredDecl struct {
	PkgPath string
	Toks    Tokens
}

func (toks Tokens) String() (s string) {
	var sb strings.Builder
	for _, t := range toks {
		sb.WriteString(t.String() + " ")
	}
	s += sb.String()
	return s
}

// Index returns the index in toks of the first matching tok, or -1.
func (toks Tokens) Index(tok lang.Token) int {
	for i, t := range toks {
		if t.Tok == tok {
			return i
		}
	}
	return -1
}

// LastIndex returns the index in toks of the last matching tok, or -1.
func (toks Tokens) LastIndex(tok lang.Token) int {
	for i := len(toks) - 1; i >= 0; i-- {
		if toks[i].Tok == tok {
			return i
		}
	}
	return -1
}

// Split returns a slice of token arrays, separated by tok.
func (toks Tokens) Split(tok lang.Token) (result []Tokens) {
	for {
		i := toks.Index(tok)
		if i < 0 {
			return append(result, toks)
		}
		result = append(result, toks[:i])
		toks = toks[i+1:]
	}
}

// SplitStart is similar to Split, except the first token in toks is skipped.
func (toks Tokens) SplitStart(tok lang.Token) (result []Tokens) {
	for {
		if len(toks) == 0 {
			return
		}
		i := toks[1:].Index(tok)
		if i < 0 {
			return append(result, toks)
		}
		result = append(result, toks[:i])
		toks = toks[i+1:]
	}
}

// FieldKeyName returns the field name of a struct-composite-literal key
// Colon token (emitted by newFieldColon), and false otherwise.
func (t Token) FieldKeyName() (string, bool) {
	if len(t.Arg) == 0 {
		return "", false
	}
	name, ok := t.Arg[0].(string)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// noFnewMarker tags a Type Ident token whose role is non-composite (type
// conversion T(x), method expression T.M, or a struct field-key shadow).
// The compiler skips the speculative Fnew emit when the marker is present,
// which avoids the matching removeFnew patches on the consumer side.
type noFnewMarker struct{}

// MarkNoFnew tags this Token (intended for Type Idents) so the compiler
// will not emit a speculative Fnew for it.
func (t *Token) MarkNoFnew() { t.Arg = append(t.Arg, noFnewMarker{}) }

// NoFnew reports whether this Token was tagged via MarkNoFnew.
func (t Token) NoFnew() bool {
	for _, a := range t.Arg {
		if _, ok := a.(noFnewMarker); ok {
			return true
		}
	}
	return false
}

func newToken(tok lang.Token, str string, pos int, arg ...any) Token {
	return Token{Token: scan.Token{Tok: tok, Str: str, Pos: pos}, Arg: arg}
}

func newIdent(name string, pos int, arg ...any) Token { return newToken(lang.Ident, name, pos, arg...) }
func newCall(pos int, arg ...any) Token               { return newToken(lang.Call, "", pos, arg...) }
func newDefer(pos int, arg ...any) Token              { return newToken(lang.Defer, "", pos, arg...) }
func newGo(pos int, arg ...any) Token                 { return newToken(lang.Go, "", pos, arg...) }
func newChanSend(pos int) Token                       { return newToken(lang.ChanSend, "", pos) }
func newGoto(label string, pos int) Token             { return newToken(lang.Goto, label, pos) }
func newLabel(label string, pos int) Token            { return newToken(lang.Label, label, pos) }
func newJumpFalse(label string, pos int) Token        { return newToken(lang.JumpFalse, label, pos) }
func newNext(label string, pos, n int) Token          { return newToken(lang.Next, label, pos, n) }
func newGrow(size, pos int) Token                     { return newToken(lang.Grow, "", pos, size) }
func newSemicolon(pos int) Token                      { return newToken(lang.Semicolon, "", pos) }
func newDrop(pos int) Token                           { return newToken(lang.Drop, "", pos) }
func newEqualSet(pos int) Token                       { return newToken(lang.EqualSet, "", pos) }
func newReturn(pos int) Token                         { return newToken(lang.Return, "", pos) }
func newJumpSetFalse(label string, pos int) Token     { return newToken(lang.JumpSetFalse, label, pos) }
func newJumpSetTrue(label string, pos int) Token      { return newToken(lang.JumpSetTrue, label, pos) }
func newComposite(ctype string, pos, sliceLen int) Token {
	return newToken(lang.Composite, ctype, pos, sliceLen)
}
func newIndex(pos int) Token                   { return newToken(lang.Index, "", pos) }
func newInt(i, pos int) Token                  { return newToken(lang.Int, strconv.Itoa(i), pos) }
func newColon(pos int) Token                   { return newToken(lang.Colon, "", pos) }
func newFieldColon(name string, pos int) Token { return newToken(lang.Colon, "", pos, name) }

func newLen(i, pos int) Token { return newToken(lang.Len, "", pos, i) }
func newSlice(pos int) Token  { return newToken(lang.Slice, "", pos) }
func newTypeAssert(typ *vm.Type, pos, okForm int) Token {
	return newToken(lang.TypeAssert, typ.String(), pos, okForm, typ)
}
