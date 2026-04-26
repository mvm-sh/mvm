package lang

//go:generate stringer -type=Token

// Token represents a lexical token.
type Token int

// All known tokens for the set of supported languages.
const (
	Illegal Token = iota
	Comment
	Ident

	// Literal values.
	Char
	Float
	Imag
	Int
	String

	// Binary operators (except indicated).
	// Arithmetic and bitwise binary operators.
	Add    // +
	Sub    // -
	Mul    // *
	Quo    // /
	Rem    // %
	And    // &
	Or     // |
	Xor    // ^
	Shl    // <<
	Shr    // >>
	AndNot // &^
	Period // .

	// Binary operators returning a boolean.
	Equal        // ==
	Greater      // >
	GreaterEqual // >=
	Land         // &&
	Less         // <
	LessEqual    // <=
	Lor          // ||
	NotEqual     // !=

	// Assigment operators (arithmetic and bitwise).
	Define       // :=
	Assign       // =
	AddAssign    // +=
	SubAssign    // -=
	MulAssign    // *=
	QuoAssign    // /=
	RemAssign    // %=
	AndAssign    // &=
	OrAssign     // |=
	XorAssign    // ^=
	ShlAssign    // <<=
	ShrAssign    // >>=
	AndNotAssign // &^=
	Inc          // ++
	Dec          // --
	IndexAssign  // a[i] =
	DerefAssign  // *p =

	// Unary operations.
	Plus     // unary +
	Minus    // unary -
	Addr     // unary &
	Deref    // unary *
	BitComp  // unary ^
	Arrow    // unary ->
	Ellipsis // unary ...
	Not      // unary !
	Tilde    // unary ~ (underlying type)

	// Separators (punctuation).
	Comma     // ,
	Semicolon // ;
	Colon     // :

	// Block tokens.
	ParenBlock   // (..)
	BracketBlock // [..]
	BraceBlock   // {..}

	// Reserved keywords.
	Break
	Case
	Chan
	Const
	Continue
	Default
	Defer
	Else
	Fallthrough
	For
	Func
	Go
	Goto
	If
	Import
	Interface
	Map
	Package
	Range
	Return
	Select
	Struct
	Switch
	Type
	Var

	// Internal virtual machine tokens (no corresponding keyword).
	Call
	ChanSend // ch v -- ; channel send statement
	Composite
	EqualSet
	Grow
	Index
	JumpFalse
	JumpSetFalse
	JumpSetTrue
	Label
	Len
	New
	Next
	Slice
	Stop
	TypeAssert
	TypeSwitchJump
	PopExpr // discard unused expression-statement return values
	Drop    // pop one value from the stack (used to clean up switch/select dispatch)

	// This must be the last token value.
	MaxTok
)

// UnaryOp contains the set of unary operators.
// TODO: define UnaryOp per language.
var UnaryOp = map[Token]Token{
	Add:   Plus,    // +
	And:   Addr,    // &
	Not:   Not,     // !
	Mul:   Deref,   // *
	Sub:   Minus,   // -
	Tilde: Tilde,   // ~
	Xor:   BitComp, // ^
}

// IsKeyword returns true if t is a keyword.
func (t Token) IsKeyword() bool { return t >= Break && t <= Var }

// IsLiteral returns true if t is a literal value.
func (t Token) IsLiteral() bool { return t >= Char && t <= String }

// IsOperator returns true if t is an operator.
func (t Token) IsOperator() bool { return t >= Add && t <= Tilde }

// IsBlock returns true if t is a block kind of token.
func (t Token) IsBlock() bool { return t >= ParenBlock && t <= BraceBlock }

// IsBoolOp returns true if t is boolean operator.
func (t Token) IsBoolOp() bool { return t >= Equal && t <= NotEqual || t == Not }

// IsBinaryOp returns true if t is a binary operator (takes 2 operands).
func (t Token) IsBinaryOp() bool { return t >= Add && t <= NotEqual }

// IsUnaryOp returns true if t is an unary operator (takes 1 operand).
func (t Token) IsUnaryOp() bool { return t >= Plus && t <= Tilde }

// IsLogicalOp returns true if t is a logical operator.
func (t Token) IsLogicalOp() bool { return t == Land || t == Lor }
