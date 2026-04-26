# ADR-008: Goroutine and channel support

**Status:** accepted
**Date:** 2026-03-30

## Context

Mvm interprets a subset of Go. Goroutines and channels are central
to Go concurrency; many real programs rely on `go func(){}()`,
`make(chan T)`, `ch <- v`, and `<-ch`. Adding them requires changes at
every layer: the lexer (the `<-` token), the parser (`go` statements,
send statements, receive expressions, channel types), the compiler
(new opcodes, builtin dispatch), and the VM (spawning, synchronisation,
channel operations).

The key design question is how goroutines share state. Each goroutine
needs its own call stack, but they must share module-level variables.
Before this change the VM used a single `mem []Value` slice that held
globals at low indices and the stack above. Goroutines cannot share a
single-stack layout.

## Decision

### globals / mem split

`Machine.mem` is narrowed to hold only the call stack (frame-relative
indices). Global variables and function code addresses move to a new
`Machine.globals []Value` field. All existing global accesses
(`GetGlobal`, `Set` with Global scope, `Push`, `Top`, etc.) are updated
to use `globals`.

This is the minimal change that enables goroutine isolation: each
goroutine gets a fresh `mem` but points `globals` at the parent's
backing array, so global writes are immediately visible across goroutines
without copying.

### Goroutine spawning

`newGoroutine(fval, args)` builds a child `Machine` that:

1. Shares `globals` -- same slice header and backing array as the parent.
2. Gets a private `code` copy (length `baseCodeLen + 2`) with a
   `Call narg 0; Exit` epilogue appended, so the goroutine's entry point
   is a normal call to `fval`.
3. Gets a fresh `mem` with `fval` followed by `args`.
4. Shares the parent's `*sync.WaitGroup`.

The parent increments `wg.Add(1)` before `go func() { child.Run() }()`
and the goroutine decrements with `defer wg.Done()`. The top-level
(non-goroutine) machine calls `wg.Wait()` before returning from `Run`,
so all goroutines complete before the interpreter exits. Child machines
(`isGoroutine = true`) do not wait -- they exit when their own `Run`
returns.

WaitGroup is created lazily (nil until the first `go` statement) to
avoid overhead in programs that never spawn goroutines.

### GoCallImm optimisation

When the target of a `go` statement is a named, non-closure function,
the compiler removes the preceding `GetGlobal` instruction and emits
`GoCallImm A=globalsIdx B=narg` instead of `GoCall narg`. This mirrors
the existing `CallImm` optimisation for regular calls and avoids one
stack read per goroutine spawn.

### Channel operations

All channel operations delegate to `reflect`:

- `make(chan T[, n])` -> `reflect.MakeChan(chanType, bufSize)`
- `ch <- v`          -> `ch.ref.Send(v.Reflect())`
- `<-ch`             -> `ch.ref.Recv()` (returns value + ok bool)
- `close(ch)`        -> `ch.ref.Close()`

Channel values are stored as `Value{ref: reflect.Value}` -- the same
representation used for slices and maps -- and participate normally in
variable slots, assignments, and function calls.

Directional channel types (`chan<-`, `<-chan`) are parsed as
bidirectional (`reflect.BothDir`) for now.

## Consequences

**Easier:**
- Goroutine and channel semantics are correct by construction: Go's
  runtime handles scheduling and synchronisation; the VM just calls
  `go` and uses `sync.WaitGroup`.
- Channel operations require no VM-level synchronisation primitives;
  `reflect.Value.Send` and `Recv` block as expected.
- The globals/mem split is a clean separation: only the compiler and
  `interp` need to know about globals indices; the stack is purely
  local to each goroutine.

**Harder:**
- `CallFunc` (re-entrant execution for native callbacks) must save and
  restore `globals` separately, and copy it to a fresh backing array so
  inner writes do not affect the outer run.
- The `Top()` helper requires a fallback: when `mem` is empty after a
  pure global assignment, it inspects `globals` to return the last value.
  This preserves REPL behaviour that previously relied on globals living
  in `mem`.
- Directional channel enforcement (send-only `chan<-`, receive-only
  `<-chan`) is not yet implemented; all channels are bidirectional at
  runtime.
- `select` statements are now implemented via the `SelectExec` opcode and
  `SelectMeta` data structure. `reflect.Select` handles the actual
  blocking. See `comp/compiler.go` (`case lang.Select`) and `vm/vm.go`
  (`case SelectExec`) for details.
