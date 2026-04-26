# ADR-006: Native Go function interop (WrapFunc / MvmFunc)
**Status:** accepted
**Date:** 2026-03-22

## Context

Mvm function values are represented at runtime as plain integers (code
addresses) or `Closure` structs. Neither can be stored directly into a
typed Go `func` field via `reflect.Value.Set`, because reflect requires the
stored value to match the declared field type exactly.

This matters when a mvm program passes a function literal as a callback
to native Go code -- for example, as an HTTP handler or an event listener
stored in a struct field.

## Decision

Two complementary mechanisms handle this:

1. **`funcFields` and `funcFieldsByFuncPtr` side-tables in `Machine`.**
   Two `map[uintptr]Value` maps maintain mvm func values assigned to
   native struct `func` fields:
   - `funcFields` -- keyed by the field's `reflect.Value` memory address.
     Fast but invalidated when the containing struct is copied (e.g. by
     `append`).
   - `funcFieldsByFuncPtr` -- keyed by the closure's stable function
     pointer (dereferenced from the field address). Provides a fallback
     when `funcFields` misses due to a copy.
   When the VM detects assignment of a mvm func to a struct `func`
   field, it writes into both tables and stores a placeholder in the
   actual field. Reads route back through the tables.

2. **`WrapFunc` opcode and `MvmFunc` type.** When a mvm func must
   be callable by native Go code that holds a `reflect.Value` reference,
   `WrapFunc` converts it into a `MvmFunc{Val, GF}`:
   - `Val` is the original mvm func value (int or Closure), used for
     fast in-VM dispatch.
   - `GF` is a `reflect.MakeFunc`-constructed Go function that re-enters
     the VM via `Machine.CallFunc` when invoked from native Go.

   `CallFunc` is a re-entrant entry into the VM run loop, safe for
   single-threaded synchronous callbacks. Concurrent calls from separate
   goroutines on the same `Machine` are not safe.

## Consequences

**Easier:**
- Native Go libraries that accept `func` callbacks work without special
  wrapper code in the interpreted program.
- `Val` is preserved, so in-VM calls to wrapped functions remain fast
  (no reflect overhead).

**Harder:**
- `Machine` carries two func-field side-tables (`funcFields` and
  `funcFieldsByFuncPtr`) that must be consulted on struct func-field
  reads/writes, adding two map lookups per access.
- `WrapFunc`-generated functions hold a reference to the `Machine`,
  so the machine cannot be garbage-collected while a native callback
  is live.
- Concurrent safety is the caller's responsibility; the VM provides no
  locking.
