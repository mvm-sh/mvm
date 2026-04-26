// Package lang provides tokens for possibly  multiple languages.
package lang

// Lexical properties of tokens to allow scanning.
const (
	CharIllegal = 1 << iota
	CharOp
	CharNum
	CharAlpha
	CharSep
	CharLineSep
	CharGroupSep
	CharStr
	CharBlock
	StrEsc
	StrNonl
	ExcludeEnd  // exclude end delimiter from content
	EosValidEnd // end of input string terminates block or string token
)

// ASCIILen is the length of the ASCII characters set.
const ASCIILen = 1 << 7 // 128

// Associativity represent the associativity rule of an operator.
type Associativity int

// Associativity kinds for operators.
const (
	Aboth  Associativity = iota // both left and right associative
	Aleft                       // left associative only
	Aright                      // right associative only
	Anon                        // non associative
)

// TokenProp represent token properties for parsing.
type TokenProp struct {
	Token
	SkipSemi      bool // automatic semicolon insertion after newline
	Precedence    int  // operator precedence
	Associativity      // associativity of operator
	HasInit       bool // true if may have an init clause
}

// Spec represents the language specification for scanning.
type Spec struct {
	CharProp   [ASCIILen]uint    // special Character properties
	End        map[string]string // end delimiters, indexed by start
	BlockProp  map[string]uint   // block properties
	Tokens     map[string]Token  // token per string
	TokenProps []TokenProp       // token properties, indexed by token
	DotNum     bool              // true if a number can start with '.'
	IdentASCII bool              // true if an identifier can be in ASCII only
	NumUnder   bool              // true if a number can contain _ character
}
