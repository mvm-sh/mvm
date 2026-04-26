// Package scan provide a language independent scanner.
package scan

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mvm-sh/mvm/lang"
)

// Error definitions.
var (
	ErrBlock   = errors.New("block not terminated")
	ErrIllegal = errors.New("illegal token")
)

// Token defines a scanner token.
type Token struct {
	Tok lang.Token // token identificator
	Pos int        // position in source
	Str string     // string in source
	Beg int        // length of begin delimiter (block, string)
	End int        // length of end delimiter (block, string)
}

// Block return the block content of t.
func (t *Token) Block() string { return t.Str[t.Beg : len(t.Str)-t.End] }

// Prefix returns the block starting delimiter of t.
func (t *Token) Prefix() string { return t.Str[:t.Beg] }

// Name return the name of t (short string for debugging).
func (t *Token) Name() string {
	if len(t.Str) == 0 {
		return ""
	}
	if t.Beg > 1 {
		return t.Str[:t.Beg] + ".."
	}
	return t.Str[:t.Beg] + ".." + t.Str[len(t.Str)-t.End:]
}

func (t *Token) String() string {
	s := t.Tok.String()
	if t.Tok.IsLiteral() || t.Tok.IsBlock() || t.Tok == lang.Ident || t.Tok == lang.Comment ||
		t.Tok == lang.Period || t.Tok == lang.Label || t.Tok == lang.Goto || t.Tok == lang.JumpSetFalse ||
		t.Tok == lang.JumpSetTrue || t.Tok == lang.JumpFalse || t.Tok == lang.Colon || t.Tok == lang.Composite {
		s += strconv.Quote(t.Str)
	} else if t.Tok == lang.Call {
		s += "(" + strconv.Itoa(t.Beg) + ")"
	}
	return s
}

// Scanner contains the scanner rules for a language.
type Scanner struct {
	*lang.Spec
	Sources Sources // source position registry (multi-file / REPL)
	PosBase int     // base offset for current source

	// Precomputed lookup tables, built from Spec maps by NewScanner.
	charTok         [lang.ASCIILen]lang.Token // token for single-byte Tokens keys
	blockTok        [lang.ASCIILen]lang.Token // block token by opening byte
	endByte         [lang.ASCIILen]byte       // end delimiter for single-byte openers
	charBlockProp   [lang.ASCIILen]uint       // BlockProp for single-byte keys
	multiStrStart   [lang.ASCIILen]bool       // first byte of a multi-byte string/comment start
	blockPropHasDir bool                      // true if any BlockProp key starts with a direct character
}

// NewScanner returns a new scanner for a given language specification.
func NewScanner(spec *lang.Spec) *Scanner {
	sc := &Scanner{Spec: spec}

	for s, t := range sc.Tokens {
		if len(s) == 1 && s[0] < lang.ASCIILen {
			sc.charTok[s[0]] = t
		}
		if len(s) == 4 && s[1] == '.' && s[2] == '.' && s[0] < lang.ASCIILen {
			sc.blockTok[s[0]] = t
		}
	}
	for s, p := range sc.BlockProp {
		if len(s) == 1 && s[0] < lang.ASCIILen {
			sc.charBlockProp[s[0]] = p
		}
		if len(s) >= 2 && s[0] < lang.ASCIILen && p&lang.CharStr != 0 {
			sc.multiStrStart[s[0]] = true
		}
		if len(s) > 0 && sc.isDir(rune(s[0])) {
			sc.blockPropHasDir = true
		}
	}
	for s, e := range sc.End {
		if len(s) == 1 && s[0] < lang.ASCIILen && len(e) == 1 {
			sc.endByte[s[0]] = e[0]
		}
	}
	return sc
}

func (sc *Scanner) hasProp(r rune, p uint) bool {
	if r >= lang.ASCIILen {
		return false
	}
	return sc.CharProp[r]&p != 0
}

func (sc *Scanner) isOp(r rune) bool       { return sc.hasProp(r, lang.CharOp) }
func (sc *Scanner) isSep(r rune) bool      { return sc.hasProp(r, lang.CharSep) }
func (sc *Scanner) isLineSep(r rune) bool  { return sc.hasProp(r, lang.CharLineSep) }
func (sc *Scanner) isGroupSep(r rune) bool { return sc.hasProp(r, lang.CharGroupSep) }
func (sc *Scanner) isStr(r rune) bool      { return sc.hasProp(r, lang.CharStr) }
func (sc *Scanner) isBlock(r rune) bool    { return sc.hasProp(r, lang.CharBlock) }
func (sc *Scanner) isDir(r rune) bool {
	return !sc.hasProp(r, lang.CharOp|lang.CharSep|lang.CharLineSep|lang.CharGroupSep|lang.CharStr|lang.CharBlock)
}

func isNum(r rune) bool { return '0' <= r && r <= '9' }

// Scan performs a lexical analysis on src and returns tokens or an error.
func (sc *Scanner) Scan(src string, semiEOF bool) (tokens []Token, err error) {
	tokens = make([]Token, 0, len(src)/4+1)
	s := strings.TrimLeftFunc(src, unicode.IsSpace)
	offset := len(src) - len(s)
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	for len(s) > 0 {
		t, err := sc.Next(s)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", loc(src, offset+t.Pos), err)
		}
		if t.Tok == lang.Illegal && t.Str == "" {
			break
		}
		skip := false
		if len(tokens) > 0 && t.Str == "\n" {
			// Check for automatic semi-colon insertion after newline.
			last := tokens[len(tokens)-1]
			if last.Tok.IsKeyword() && sc.TokenProps[last.Tok].SkipSemi ||
				last.Tok.IsOperator() && !sc.TokenProps[last.Tok].SkipSemi ||
				last.Tok == lang.Comma {
				skip = true
			} else {
				t.Tok = lang.Semicolon
				t.Str = ";"
			}
		}
		nr := t.Pos + len(t.Str)
		s = s[nr:]
		t.Pos += offset
		offset += nr
		if !skip {
			tokens = append(tokens, t)
		}
	}

	// Optional insertion of semi-colon at the end of the token stream.
	if semiEOF && len(tokens) > 0 {
		last := tokens[len(tokens)-1]
		if last.Str == ";" {
			return tokens, nil
		}
		if last.Tok == lang.Ident && sc.TokenProps[last.Tok].SkipSemi ||
			last.Tok.IsOperator() && !sc.TokenProps[last.Tok].SkipSemi {
			return tokens, nil
		}
		tokens = append(tokens, Token{Tok: lang.Semicolon, Str: ";"})
	}

	return tokens, nil
}

func loc(s string, p int) string {
	line, col := lineCol(s, p)
	return fmt.Sprintf("%d:%d", line, col)
}

// Next returns the next token in string.
func (sc *Scanner) Next(src string) (tok Token, err error) {
	p := 0

	// Skip initial separators.
	for i, r := range src {
		p = i
		if !sc.isSep(r) {
			break
		}
	}
	src = src[p:]

	// Get token according to its first characters.
	for i, r := range src {
		switch {
		case sc.isSep(r):
			return Token{}, nil
		case sc.isGroupSep(r):
			return Token{Tok: sc.charTok[r], Pos: p + i, Str: string(r)}, nil
		case sc.isLineSep(r):
			return Token{Pos: p + i, Str: "\n"}, nil
		case sc.isStr(r):
			s, ok := sc.getStr(src[i:], 1)
			if !ok {
				err = ErrBlock
			}
			return Token{Tok: lang.String, Pos: p + i, Str: s, Beg: 1, End: 1}, err
		case sc.isBlock(r):
			b, ok := sc.getBlock(src[i:], 1)
			if !ok {
				err = ErrBlock
			}
			return Token{Tok: sc.blockTok[r], Pos: p + i, Str: b, Beg: 1, End: 1}, err
		case sc.isOp(r):
			// A dot followed by a digit starts a float literal (e.g. .5, .0312).
			if r == '.' && i+1 < len(src) && isNum(rune(src[i+1])) {
				s, tok := sc.getNum(src[i:])
				return Token{Tok: tok, Pos: p + i, Str: s}, nil
			}
			op, isOp := sc.getOp(src[i:])
			if isOp {
				var t lang.Token
				if len(op) == 1 {
					t = sc.charTok[op[0]]
				} else {
					t = sc.Tokens[op]
				}
				if t == lang.Illegal {
					err = fmt.Errorf("%w: %s", ErrIllegal, op)
				}
				return Token{Tok: t, Pos: p + i, Str: op}, err
			}
			flag := sc.BlockProp[op]
			if flag&lang.CharStr != 0 {
				s, ok := sc.getStr(src[i:], len(op))
				if !ok {
					err = ErrBlock
				}
				return Token{Tok: lang.Comment, Pos: p + i, Str: s, Beg: len(op), End: len(op)}, err
			}
		case isNum(r):
			s, tok := sc.getNum(src[i:])
			return Token{Tok: tok, Pos: p + i, Str: s}, nil
		default:
			t, isDefined := sc.getToken(src[i:])
			if isDefined {
				ident := sc.Tokens[t]
				if ident == lang.Illegal {
					ident = lang.Ident
				}
				return Token{Tok: ident, Pos: p + i, Str: t}, nil
			}
			flag := sc.BlockProp[t]
			if flag&lang.CharBlock != 0 {
				s, ok := sc.getBlock(src[i:], len(t))
				if !ok {
					err = ErrBlock
				}
				return Token{Pos: p + i, Str: s, Beg: len(t), End: len(t)}, err
			}
		}
	}
	return Token{}, nil
}

func (sc *Scanner) getToken(src string) (s string, isDefined bool) {
	s = sc.nextToken(src)
	if sc.blockPropHasDir {
		if _, match := sc.BlockProp[s]; match {
			return s, false
		}
	}
	return s, true
}

func (sc *Scanner) nextToken(src string) (s string) {
	for i, r := range src {
		if !sc.isDir(r) {
			break
		}
		s = src[:i+utf8.RuneLen(r)]
	}
	return s
}

func (sc *Scanner) getOp(src string) (s string, isOp bool) {
	for i, r := range src {
		if !sc.isOp(r) {
			break
		}
		s = src[:i+1]
		if len(s) == 1 {
			if sc.charBlockProp[s[0]] != 0 {
				return s, false
			}
		} else if sc.multiStrStart[s[0]] {
			if _, match := sc.BlockProp[s]; match {
				return s, false
			}
		}
	}
	// If the longest match is not a known token, try shorter prefixes.
	for len(s) > 1 && sc.Tokens[s] == lang.Illegal {
		s = s[:len(s)-1]
	}
	return s, true
}

func (sc *Scanner) getNum(src string) (s string, tok lang.Token) {
	tok = lang.Int
	hasDot := false
	hasExp := false

	// Handle leading dot (e.g. .5, .0312).
	if len(src) > 0 && src[0] == '.' {
		hasDot = true
		tok = lang.Float
	}

	for i, r := range src {
		switch {
		case isNum(r):
			// ok
		case r == '.' && !hasDot && !hasExp:
			// Check this is not a method call (e.g. 123.String()).
			if i+1 < len(src) && isNum(rune(src[i+1])) {
				hasDot = true
				tok = lang.Float
			} else {
				return src[:i], tok
			}
		case r == '.' && i == 0 && hasDot:
			// Leading dot already accounted for.
		case (r == 'e' || r == 'E') && !hasExp:
			hasExp = true
			tok = lang.Float
			// Allow optional +/- after exponent.
			if i+1 < len(src) && (src[i+1] == '+' || src[i+1] == '-') {
				s = src[:i+2]
				continue
			}
		case r == '+' || r == '-':
			// Valid only right after 'e'/'E' exponent prefix.
			if i < 2 || (src[i-1] != 'e' && src[i-1] != 'E') {
				return s, tok
			}
		case r == 'x' || r == 'X' || r == 'o' || r == 'O':
			if i != 1 || src[0] != '0' {
				return src[:i], tok
			}
		case (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			// Hex digits (a-f) and binary prefix (0b/0B).
			// 'b'/'B' at position 1 after '0' is a binary prefix, not a hex digit.
			if (i != 1 || src[0] != '0' || (r != 'b' && r != 'B')) &&
				(len(src) < 3 || src[0] != '0' || (src[1] != 'x' && src[1] != 'X')) {
				return src[:i], tok
			}
		case r == '_':
			// digit separator, ok
		default:
			return src[:i], tok
		}
		s = src[:i+1]
	}
	return s, tok
}

func (sc *Scanner) getStr(src string, nstart int) (s string, ok bool) {
	// Fast path: single-byte start with precomputed end byte.
	if nstart == 1 && src[0] < lang.ASCIILen {
		if eb := sc.endByte[src[0]]; eb != 0 {
			prop := sc.charBlockProp[src[0]]
			canEscape := prop&lang.StrEsc != 0
			nonl := prop&lang.StrNonl != 0
			excludeEnd := prop&lang.ExcludeEnd != 0
			var esc bool
			for i := 1; i < len(src); i++ {
				b := src[i]
				if b == '\n' && nonl {
					return src[:i+1], false
				}
				if b == eb && !esc {
					if excludeEnd {
						return src[:i], true
					}
					return src[:i+1], true
				}
				esc = canEscape && b == '\\' && !esc
			}
			ok = prop&lang.EosValidEnd != 0
			return src, ok
		}
	}

	// General path: multi-byte start/end delimiter (covers //, /*).
	start := src[:nstart]
	end := sc.End[start]
	prop := sc.BlockProp[start]
	canEscape := prop&lang.StrEsc != 0
	nonl := prop&lang.StrNonl != 0
	excludeEnd := prop&lang.ExcludeEnd != 0
	var esc bool
	for i, r := range src[nstart:] {
		s = src[:nstart+i+1]
		if r == '\n' && nonl {
			return s, false
		}
		if strings.HasSuffix(s, end) && !esc {
			if excludeEnd {
				s = s[:len(s)-len(end)]
			}
			return s, true
		}
		esc = canEscape && r == '\\' && !esc
	}
	ok = prop&lang.EosValidEnd != 0
	return src, ok
}

// skipStr detects a string or comment literal starting at src[pos] and returns
// how many bytes to skip. Returns 0 if no string literal starts there.
// ok is false if an unterminated string is found.
func (sc *Scanner) skipStr(src string, pos int) (advance int, ok bool) {
	b := src[pos]
	if b < lang.ASCIILen && sc.charBlockProp[b]&lang.CharStr != 0 {
		str, sok := sc.getStr(src[pos:], 1)
		return len(str), sok
	}
	if b < lang.ASCIILen && sc.multiStrStart[b] && pos+1 < len(src) {
		if sp, match := sc.BlockProp[src[pos:pos+2]]; match && sp&lang.CharStr != 0 {
			str, sok := sc.getStr(src[pos:], 2)
			return len(str), sok
		}
	}
	return 0, true
}

func (sc *Scanner) getBlock(src string, nstart int) (s string, ok bool) {
	n := 1

	// Fast path for single-byte start/end delimiters (covers (, {, [).
	if nstart == 1 && src[0] < lang.ASCIILen {
		if eb := sc.endByte[src[0]]; eb != 0 {
			sb := src[0]
			prop := sc.charBlockProp[sb]
			for i := 1; i < len(src); i++ {
				b := src[i]
				switch b {
				case eb:
					n--
				case sb:
					n++
				default:
					if advance, sok := sc.skipStr(src, i); advance > 0 {
						if !sok {
							return src[:i+1], false
						}
						i += advance - 1
						continue
					}
				}
				if n == 0 {
					s = src[:i+1]
					if prop&lang.ExcludeEnd != 0 {
						s = s[:len(s)-1]
					}
					return s, true
				}
			}
			ok = prop&lang.EosValidEnd != 0
			return src, ok
		}
	}

	// General path for multi-byte delimiters.
	start := src[:nstart]
	end := sc.End[start]
	prop := sc.BlockProp[start]
	skip := 0
	for i := range src[nstart:] {
		if i < skip {
			continue
		}
		s = src[:nstart+i+1]
		switch {
		case strings.HasSuffix(s, end):
			n--
		case strings.HasSuffix(s, start):
			n++
		default:
			advance, sok := sc.skipStr(src, nstart+i)
			if !sok {
				return s, false
			}
			skip = i + advance
		}
		if n == 0 {
			if prop&lang.ExcludeEnd != 0 {
				s = s[:len(s)-len(end)]
			}
			return s, true
		}
	}
	ok = prop&lang.EosValidEnd != 0
	return s, ok
}
