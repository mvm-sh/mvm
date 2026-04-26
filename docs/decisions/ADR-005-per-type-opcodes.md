# ADR-005: Per-type opcodes with immediate variants

**Status:** accepted (revised)
**Date:** 2024-01-15 (revised 2026-03-27)

## Context

A generic `Add` opcode must inspect the operand type at runtime to select
the correct arithmetic operation. In a tight loop this type-switch overhead
is significant.

An earlier design kept generic `Add`/`Sub`/`Mul`/`Neg`/`Greater`/`Lower`
opcodes alongside typed ones, emitting the typed variants only when the
operand type was known at compile time and falling back to the generics
otherwise. The fallback case required a runtime type-switch and complicated
`numericOp()` with a nullable fallback parameter.

## Decision

The generic fallback opcodes (`Add`, `Sub`, `Mul`, `Neg`, `Greater`,
`Lower`) have been removed. The compiler always knows the operand type at
compile time; `numericOp()` now panics on nil or non-numeric types rather
than silently falling back.

The opcode set for numeric arithmetic is fully statically typed:
12 numeric type variants (int, int8, int16, int32, int64, uint, uint8,
uint16, uint32, uint64, float32, float64) for each of `Add`, `Sub`, `Mul`,
`Div`, `Rem`, `Neg`, `Greater`, `Lower` (96 typed opcodes total).

String concatenation (`s1 + s2`) is handled by the dedicated `AddStr`
opcode, which operates on `reflect.Value` strings rather than inline `num`
bits.

The compile-time dispatch uses `NumKindOffset` -- a fixed array (indexed
by `reflect.Kind`) mapping each numeric kind to a 0-based slot -- to
compute the exact opcode:

```
opcode = baseOp + Op(NumKindOffset[kind])
```

Additionally, a peephole optimization in `generate()` folds `Push N; BinOp`
sequences into single immediate-operand instructions (`AddIntImm`,
`SubIntImm`, `MulIntImm`, `GreaterIntImm`, `GreaterUintImm`, `LowerIntImm`,
`LowerUintImm`). These avoid pushing the constant onto the stack entirely.

## Consequences

**Easier:**
- The VM dispatch loop hits a single opcode per arithmetic operation with
  no type branching at runtime.
- `numericOp()` is simpler: one entry point, panics on bad input, no
  optional fallback parameter.
- Immediate variants eliminate a push and a stack slot for constant
  operands, reducing loop overhead by ~20-30%.

**Harder:**
- Large opcode space (100+ arithmetic opcodes). The `op_string.go`
  stringer file is generated to keep names in sync.
- Adding a new numeric type or arithmetic operation requires adding opcodes
  in several places and running `make generate`.
- The peephole pass adds complexity to the compiler's `generate` function.
