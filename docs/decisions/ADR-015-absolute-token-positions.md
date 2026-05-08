# ADR-015: Absolute token positions throughout the pipeline

**Status:** accepted
**Date:** 2026-05-08

## Context

mvm tracks source positions through a single 32-bit `vm.Pos` byte
offset into the unified `scan.Sources` registry, which concatenates
every loaded source file with a per-file `Base`. Resolving a `Pos`
back to `(file, line, col)` walks the registry by `Base`.

For this to work end to end, the `Pos` carried on every emitted
`vm.Instruction` must be an offset in that unified space, not relative
to any particular file. Two sites mattered:

1. **`scanAt(basePos, src, ...)` in goparser** scans a source string
   and returns tokens whose `Pos` has already been shifted by
   `basePos`. Top-level parsing passes `p.PosBase` (the value returned
   by `Sources.Add(name, content)`) so the resulting tokens are
   absolute. Nested helpers like `scanBlock(bt, ...)` derive their
   own `basePos` from `bt.Pos + bt.Beg`, which is absolute provided
   `bt` came from a previous `scanAt` call.

2. **`comp.emit(t, op, args...)`** writes the token's `Pos` into the
   instruction. Historically:

   ```go
   inst := vm.Instruction{Op: op, Pos: vm.Pos(t.Pos + c.PosBase)}
   ```

   This made sense in an earlier design where tokens were *relative*
   to the source they came from. Once `scanAt` was made the canonical
   tokenizer, tokens were already absolute -- but the `+ c.PosBase`
   stayed in place.

The bug surfaced once mvm started loading multi-source packages
through `mvm test <importpath>`. After parsing all sources,
`p.PosBase` equals the base of the *last* source. Adding it to
already-absolute tokens shifted every emitted instruction's `Pos`
into garbage; `Sources.Resolve` then mapped them to whichever file's
range happened to contain the shifted offset, almost always not the
right one.

The runtime.Callers virtualization brought this to a head: stack
traces of pkg/errors tests pointed at `stack_test.go:1` for callers
that were actually in `format_test.go`. A second related bug -- a
parser helper using `p.scan(s, false)` (which is `scanAt(0, ...)`)
to tokenize composite-literal bodies -- compounded the first by
producing relative-Pos tokens that the broken `emit` then masked
with the trailing-source `Base`.

## Decision

Establish a single invariant and enforce it at the two boundaries
above:

> **Tokens carry absolute positions in the unified `scan.Sources`
> space. `comp.emit` writes them through unchanged.**

Concretely:

1. `comp.emit` is fixed to:
   ```go
   inst := vm.Instruction{Op: op, Pos: vm.Pos(t.Pos)}
   ```
   No more PosBase addition.

2. `goparser.parseComposite` takes `basePos` as a parameter and uses
   `scanAt(basePos, s, false)` instead of `p.scan(s, false)`. Its
   single caller passes `t.Pos + t.Beg` from the enclosing
   `BraceBlock` token.

3. Parser helpers that *do not* feed `emit` (e.g. `numItems` in
   `goparser/stmt.go`, which only counts tokens) may continue using
   `p.scan` since their results are discarded.

The rule for new contributors: if a helper produces tokens that will
reach `emit`, scan them with the right absolute `basePos`.

## Consequences

**Easier:**

- Single, simple invariant. No more "tokens here are relative,
  tokens there are absolute" drift.
- Multi-source packages now produce correct stack traces, error
  messages, and `DebugInfo`-driven `bt` output.
- The `runtime.Callers` bridge (ADR-016) gets meaningful Pos values
  for free; it walks `Machine.code[ip].Pos` directly.

**Harder / weaker:**

- Anyone introducing a new helper that turns a sub-string into tokens
  must remember to pass the right `basePos`. The rule is documented
  in [goparser.md](../modules/goparser.md#position-propagation), but
  it is the kind of contract that breaks silently when violated.

## Alternatives considered

- **Make tokens relative everywhere and have `emit` always shift by
  some compiler-tracked source base.** Reverses the convention but
  requires the compiler to track which source each token came from
  -- information that is currently encoded only by the `Pos` value
  itself. Rejected: more state, no benefit.

- **Resolve to `(file, line, col)` at parse time and store that on
  instructions.** Bulkier instructions, no need for `Sources.Resolve`
  later. Rejected: tripled the per-instruction memory cost and made
  the in-memory bytecode less compact, for a benefit that
  `WalkCallStack` consumers don't actually need until report time.
