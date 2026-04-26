# ADR-007: Super instructions and instruction fusion

**Status:** accepted
**Date:** 2026-03-28

## Context

The VM dispatch loop is the interpreter's hot path. Each iteration
fetches an instruction, decodes the opcode, and jumps to the handler.
This per-instruction overhead dominates for simple operations like
loading a local variable and adding a constant -- common in loop bodies.

Profiling showed that tight numeric loops spent more time in dispatch
(switch jump, instruction fetch, stack pointer manipulation) than in
actual computation. Three patterns were especially frequent:

1. `GetLocal N; Push K; AddInt` (increment a local by a constant).
2. `LowerIntImm K; JumpFalse L` (loop bound check).
3. `GetLocal N; LowerIntImm K; JumpFalse L` (load + check + branch).

## Decision

The compiler fuses these common multi-instruction sequences into single
"super instructions" that perform all operations in one dispatch cycle.
Three levels of fusion are applied:

**Level 1 -- GetLocal + operation + immediate:**
`GetLocalAddIntImm`, `GetLocalSubIntImm`, `GetLocalMulIntImm`,
`GetLocalLowerIntImm`, `GetLocalGreaterIntImm`, `GetLocalReturn`,
`GetLocal2`, and unsigned comparison variants. The `A` field holds the
local offset; `B` holds the immediate operand.

**Level 2 -- compare + conditional jump:**
`LowerIntImmJumpFalse`, `LowerIntImmJumpTrue`. The compiler rewrites
`GreaterIntImm; JumpFalse` as `LowerIntImmJumpTrue` using the identity
`a > imm` is equivalent to `!(a < imm+1)`, keeping only `Lower`-based
fused ops. `A` holds the jump offset; `B` holds the comparison immediate.

**Level 3 -- triple fusion (GetLocal + compare + jump):**
`GetLocalLowerIntImmJumpFalse`, `GetLocalLowerIntImmJumpTrue`. No stack
operations at all -- the local is read, compared, and the branch is
taken in a single dispatch. `A` holds the jump offset; `B` packs
`localOff<<16 | imm&0xFFFF`.

Fusion is performed by two compiler functions:
- `fuseGetLocal(op, imm)` -- checks if the last emitted instruction is
  `GetLocal` and replaces it with the fused variant.
- `fuseCmpJump(...)` -- checks if the last emitted instruction is a
  comparison immediate (or a GetLocal-fused comparison) and replaces it
  with the fused compare+jump variant.

Additionally, `CallImm` fuses `GetGlobal + Call` for direct calls to
known non-closure functions, and the `Grow` instruction's `B` field
carries the compiler-computed max expression depth so the VM can
pre-allocate stack space and skip bounds checks on `GetLocal`.

## Consequences

**Easier:**
- Tight numeric loops (the Fibonacci benchmark, loop counters, array
  traversals) see significant speedup from reduced dispatch overhead.
- Triple-fused loop-bound checks (`GetLocalLowerIntImmJumpFalse`)
  eliminate all stack manipulation for the most common loop pattern.
- `CallImm` avoids function-value loading and runtime type dispatch
  for the common case of calling a known function.

**Harder:**
- The opcode space grows (14 new opcodes). Each requires a case in the
  VM switch and in debug/disassembly code.
- Compiler fusion logic (`fuseGetLocal`, `fuseCmpJump`) inspects and
  mutates previously emitted instructions, adding complexity to code
  generation.
- The `B` packing scheme for triple-fused ops limits the immediate to
  16-bit signed range and the local offset to 16-bit unsigned range.
  These limits are checked at fusion time; out-of-range cases fall back
  to unfused instruction sequences.
