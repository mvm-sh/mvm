# lang

> Token types and language specification.

## Overview

The `lang` package defines the vocabulary shared by all pipeline stages:
token types, operator properties, and the `Spec` structure that parameterizes
the scanner for a specific language. The concrete Go specification lives in
the `lang/golang` sub-package.

## Key types and functions

- **`Token`** (int enum) -- every lexical and synthetic token type:
  literals (`Int`, `Float`, `String`, ...), operators (`Add`, `Sub`, ...),
  keywords (`Func`, `If`, `For`, ...), blocks (`ParenBlock`, `BraceBlock`),
  and VM-internal tokens (`Label`, `Goto`, `JumpFalse`, `Call`, ...).
- **`Spec`** -- language specification:
  - `CharProp [128]uint` -- per-ASCII-character property bits.
  - `End map[string]string` -- maps opening delimiters to closing ones.
  - `BlockProp map[string]uint` -- per-block flags (escape handling, etc.).
  - `Tokens map[string]Token` -- keyword/operator text to token mapping.
  - `TokenProps []TokenProp` -- precedence, associativity, semicolon rules.
- **`TokenProp`** -- per-token metadata: `Precedence` (0-8), `Associativity`,
  `SkipSemi`, `HasInit`.
- **`UnaryOp map[Token]Token`** -- maps binary tokens to their unary
  counterparts (e.g. `Sub` -> `Minus`).

Character property constants (`CharOp`, `CharNum`, `CharAlpha`, `CharStr`,
`CharBlock`, etc.) are bitflags combined in `CharProp` entries.

### lang/golang

- **`GoSpec`** -- complete Go lexical specification. Maps all Go keywords,
  operators, and block delimiters. Defines precedence levels 0--8 and
  associativity for all operators. `default` is mapped to `lang.Case`.

## Dependencies

None (leaf package).
