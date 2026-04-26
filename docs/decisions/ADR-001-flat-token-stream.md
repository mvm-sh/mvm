# ADR-001: Flat token stream instead of AST

**Status:** accepted
**Date:** 2024-01-15

## Context

Most language implementations parse source code into an abstract syntax tree
(AST), then walk the tree during compilation or interpretation. An AST
provides a natural recursive structure but requires a tree traversal pass
and allocates many small nodes.

Mvm targets a simple Go subset and prioritizes implementation simplicity
and compilation speed.

## Decision

The parser (`goparser`) emits a flat `Tokens` slice instead of building an
AST. Control flow is encoded inline using synthetic tokens:

- `Label(name)` -- marks a jump target.
- `Goto(name)` -- unconditional jump.
- `JumpFalse(name)` -- conditional jump (pops a bool).
- `JumpSetFalse` / `JumpSetTrue` -- short-circuit logical operators.

Expressions are rewritten from infix to postfix by `parseExpr` (shunting-yard
algorithm), so the compiler can evaluate them with a simple left-to-right
walk.

Control structures are lowered during parsing:

```
if cond { body } else { alt }
-->  cond, JumpFalse(L1), body, Goto(L2), Label(L1), alt, Label(L2)
```

## Consequences

**Easier:**
- Code generation is a single linear pass over the token slice.
- No tree node types or visitor pattern needed.
- Token stream is easy to inspect and debug (just print the slice).

**Harder:**
- Some analyses that are natural on a tree (e.g. nested expression
  transforms) require more bookkeeping with a flat stream.
- Error messages cannot easily point to a subtree; they reference token
  positions instead.
- Adding complex language features (generics, advanced type inference) may
  eventually demand a richer intermediate representation.
