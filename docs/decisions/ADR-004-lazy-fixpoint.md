# ADR-004: Two-phase compilation with pre-allocated slots

**Status:** accepted (revised)
**Date:** 2024-01-15 (revised 2026-03)

## Context

Go allows out-of-order top-level declarations: a function can reference a
type or variable declared later in the source. A single-pass compiler
cannot resolve forward references without either a pre-pass or a retry
mechanism.

The original design used a retry (fixpoint) loop in both phases. Phase 2
retries required rollback machinery (`savedSlots`, code/data length
checkpoints) that was fragile and hard to reason about.

## Decision

The compiler uses a two-phase approach. Only Phase 1 retries; Phase 2
runs straight through with no retries.

**Phase 1 -- Declarations.** `ScanDecls` splits the source into top-level
declarations. Each is passed to `ParseDecl`, which resolves `package`,
`import`, `const`, `type`, and `var` (type registration only) and
registers function and method signatures via `registerFunc` without
parsing bodies. Declarations that fail with `ErrUndefined` are retried
until convergence. Rollback is lightweight: only `SymTracker` keys are
deleted from the symbol table (no code or data is emitted in Phase 1).

**Phase 2 -- Code generation.** After Phase 1:

1. `SplitAndSortVarDecls` expands `var(...)` blocks and topologically
   sorts var declarations by dependency order.
2. `allocGlobalSlots` pre-assigns a `Data` slot for every `Var` and
   `Func` symbol, so code generation never encounters `UnsetAddr`.
3. Var initializers are compiled first (pass 1), giving all global vars
   concrete types.
4. Func bodies and expression statements are compiled (pass 2).

Because all symbols have pre-allocated indices and vars are sorted by
dependency, Phase 2 needs no retries and no rollback machinery.

## Consequences

**Easier:**
- No rollback machinery in Phase 2 -- `savedSlots`, code/data length
  checkpoints, and string cache cleanup are all removed.
- Topological sort of var declarations makes initialization order
  predictable and eliminates retry-dependent ordering.
- Works naturally with incremental REPL evaluation.
- Method signatures are registered in Phase 1 alongside plain functions,
  improving forward reference coverage.

**Harder:**
- Phase 1 worst case is still O(n^2) in the number of declarations
  (each round resolves at least one). Acceptable for typical program sizes.
- The topological sort adds a dependency analysis pass, but it is simple
  (identifier matching within var initializer tokens) and runs once.
