# ADR-002: Hybrid Value type

**Status:** accepted
**Date:** 2024-01-15

## Context

The VM needs a universal runtime value type. Using `reflect.Value` for
everything is simple but causes heap allocations on every arithmetic
operation (boxing integers and floats). For an interpreter, arithmetic is
on the hot path.

## Decision

`vm.Value` uses a hybrid layout:

```go
type Value struct {
    num uint64        // inline numeric storage (bool, int*, uint*, float*)
    ref reflect.Value // composite data OR type metadata for numerics
}
```

- **Numeric types**: `num` holds raw bits (via `unsafe` reinterpret for
  floats). `ref` carries type information but is not the source of truth
  for the value.
- **Composite types** (string, slice, map, struct, pointer, func): `ref`
  holds the actual value; `num` is unused.
- **Variable slots** (allocated by `NewValue`): `ref` is addressable
  (`reflect.New(typ).Elem()`). `Set` updates both `num` and `ref`.
- **Temporaries** (arithmetic results): `ref` is `reflect.Zero(typ)`,
  non-addressable. `num` is canonical.

## Consequences

**Easier:**
- Arithmetic on the hot path (Add, Sub, Mul, comparisons) operates
  directly on `num` with zero allocations.
- Benchmarks show 12x improvement for single adds, 29x for loops.

**Harder:**
- Two sources of truth (`num` vs `ref`) require careful synchronization.
  `Get` must sync `num` from `ref` for addressable slots; `Set` must
  update both.
- The code is more complex than a single `reflect.Value` wrapper.
- `Reflect()` and `Interface()` must reconstruct from `num` when `ref` is
  non-addressable.
