# ADR-011: Generics via monomorphization

**Status:** accepted
**Date:** 2026-04-16

## Context

Go 1.18 introduced type parameters. Mvm needs to support at least basic
generic functions and types (`func Max[T any](a, b T) T`,
`type Box[T any] struct { Value T }`).

Two main strategies exist:

- **Monomorphization** -- produce a specialized copy per set of concrete type
  arguments. Used by C++ templates and Rust generics.
- **Type erasure / boxing** -- represent type parameters as `any` at runtime
  and insert casts. Used by Java generics and (partially) by the gc Go
  compiler.

## Decision

Monomorphization at the parser level, via token-level substitution.

A generic declaration is stored as a `genericTemplate` containing the raw
token slice and the list of type parameter names. When the parser encounters
a use like `Max[int](...)`, it:

1. Resolves concrete type arguments from the bracket block.
2. Builds a substitution map (`T -> int`).
3. Deep-copies the token stream, replacing identifier tokens matching type
   parameter names with the concrete type name, and removing the bracket
   block.
4. Renames the declaration to a mangled name (`Max#int`).
5. Passes the rewritten tokens through the normal `registerFunc`/`parseFunc`
   or `parseTypeLine` path.

The compiler and VM see only concrete, non-generic code.

## Consequences

**Easier:**

- No changes to the compiler or VM. The entire feature lives in `goparser/`.
- Each instantiation compiles to optimal per-type opcodes automatically (no
  boxing, no interface dispatch overhead).
- Fits naturally with the existing flat token stream model -- no AST
  rewriting needed.

**Harder:**

- Code size grows with the number of distinct instantiations (same trade-off
  as C++ templates). Not a concern for an interpreter.
- Constraints are syntactically accepted. Approximation constraints
  (`~[]E`, `~map[K]V`) are structurally matched during inference but not
  enforced as a hard check at instantiation time; `cmp.Ordered`-style unions
  are not type-checked either.
- Error messages from instantiated code reference the mangled name, not the
  original generic name.
- Methods declared after a generic type's first instantiation must be
  picked up retroactively. `finalizeGenericMethods` runs a post-Phase-1
  catch-up pass that iterates the recorded instances against newly
  registered method templates.

### Status of initial limitations (2026-04-18)

When this ADR was drafted, three items were called out as not yet
implemented. All three now work and ship with regression tests:

- Generic methods on generic receivers: `func (b Box[T]) Get() T` -- fixed.
- Package-qualified type args in explicit instantiation: `Slice[netip.Prefix]`
  -- fixed.
- Compound-shape inference in implicit calls: `F(ptr)`, `F(slice)`,
  `F(map)` -- fixed.

See the `_samples/generic_*.go` suite and `TestGenericImplicit` in
`interp/interpreter_test.go`. A few minor edge cases remain (non-range
local-var inference from non-composite RHS; transitive-import alias leak)
that are tracked in project memory but not release-blocking.
