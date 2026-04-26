# ADR-010: Compiler intrinsics for math and bit manipulation

**Status:** accepted
**Date:** 2026-04-11

## Context

Standard library functions like `math.Abs`, `math.Sqrt`, `math.Floor`, and
`math/bits.LeadingZeros` are called frequently in numeric code. In mvm
these went through the reflection-based native `Call` path: allocate a
`[]reflect.Value` slice, convert each argument, dispatch via `rv.Call(in)`,
and convert results back. This is orders of magnitude slower than a native
VM opcode that operates directly on the `uint64` storage in `Value.num`.

Separately, a future goal is to execute WebAssembly programs by translating
WASM bytecode to mvm bytecode at load time. WASM's core instruction set
requires bit manipulation (clz, ctz, popcnt, rotl, rotr) and float math
(abs, sqrt, ceil, floor, trunc, nearest, min, max, copysign) -- operations
the mvm VM previously lacked.

## Decision

Add 28 new VM opcodes (10 bit manipulation + 18 float math) and a compiler
intrinsics mechanism that emits them for known `math` and `math/bits` calls.

**New opcodes:**

- Bit manipulation (32-bit and 64-bit variants): `Clz`, `Ctz`, `Popcnt`,
  `Rotl`, `Rotr`.
- Float math (Float32 and Float64 variants): `Abs`, `Sqrt`, `Ceil`, `Floor`,
  `Trunc`, `Nearest`, `Min`, `Max`, `Copysign`.

**Compiler intrinsics:**

`compileIntrinsic` in `comp/compiler.go` checks a static `intrinsicOp` map
keyed by qualified function name (e.g. `"math.Abs"`). On a match it removes
the preceding `GetGlobal`, pops argument/function symbols, pushes the return
type, and emits the opcode directly -- no `Call`, no frame setup, no
reflection.

**Design constraints:**

- **No duplication.** One set of opcodes serves both Go programs (via
  intrinsics) and a future WASM translator.
- **No performance regression.** Additional switch cases compile to a jump
  table; programs not using these ops are unaffected.
- **Translation over interpretation.** The opcodes are designed so a WASM
  binary can be translated to mvm bytecode at load time, keeping
  `vm.Run()` as the single execution engine.

## Consequences

- Go programs calling `math.Abs`, `bits.LeadingZeros`, etc. skip the
  reflection overhead entirely -- the call compiles to a single opcode.
- The opcode space grows from ~210 to ~240 entries.
- Float32 opcodes exist in the VM but have no compiler emission path yet
  (the `intrinsicOp` table only maps Float64 variants). They are reachable
  via a future WASM translator or manual bytecode.
- Adding new intrinsics is a one-line addition to the `intrinsicOp` map
  plus the corresponding opcode and switch case.
- The `Rotr` opcodes are implemented as `RotateLeft(x, -k)`. They exist
  as distinct opcodes for 1:1 alignment with WASM's instruction set.
