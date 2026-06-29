# vm

> Stack-based bytecode virtual machine.

## Overview

The `vm` package executes compiled bytecode. `Machine.Run()` interprets a
`Code` (slice of `Instruction`) over two separate slices: `globals []Value`
(module-level vars and function addresses, shared across goroutines) and
`mem []Value` (the per-goroutine call stack). The package also defines
`Value` (the runtime value representation) and `Type` (runtime type
metadata).

## Key types and functions

### Execution

- **`Machine`** -- VM state: `code`, `globals []Value` (shared with child
  goroutines), `mem []Value` (per-goroutine call stack), `ip`, `fp`,
  closure `heap`, a `heapFrames [][]*Value` stack (saved caller closure heaps,
  pushed only for closure calls where `heap != nil`), panic state
  (`panicking`, `panicVal`), goroutine state (`wg *sync.WaitGroup`,
  `isGoroutine bool`), a parent-owned `funcFields *funcFieldsTable` keyed
  by the closure's function pointer read live from the field's bytes
  (resolves the original mvm Value even after a struct-copy rewrites the
  field), and debug state.
- **`Run() error`** -- main execution loop. Dispatches on `Op` via a
  switch statement.
- **`Push(vals ...Value)`** -- append values to `globals` (used before
  `Run` to load the data segment). Returns the start index.
- **`PushCode(instrs ...Instruction)`** -- append instructions (for
  incremental evaluation).

### Values

- **`Value`** -- hybrid runtime value:
  - `num uint64` -- inline storage for numeric types (bool, int*, uint*,
    float*). Holds raw bits.
  - `ref reflect.Value` -- composite data (string, slice, map, struct,
    func) or type metadata for numerics.
  - Variable slots (from `NewValue`): `ref` is addressable.
  - Temporaries (from arithmetic): `ref` is `reflect.Zero(typ)`,
    non-addressable; `num` is canonical.
- **`NewValue(reflect.Type) Value`** -- allocate a zero value (addressable).
- **`ValueOf(any) Value`** -- wrap a Go value.

### Types

- **`Type`** -- runtime type: `Rtype reflect.Type`, `Name`, `PkgPath`,
  `Methods []Method`, `IfaceMethods []IfaceMethod`,
  `Embedded []EmbeddedField`, `Params []*Type`, `Returns []*Type`,
  `Fields []*Type`, `ElemType *Type`.
  `ElemType` preserves the mvm-level element type for
  map/slice/array/pointer/chan composites, propagated by `PointerTo`,
  `ArrayOf`, `SliceOf`, `MapOf`, and `ChanOf`. `Elem()` returns
  `ElemType` when set, falling back to a bare `reflect.Type` wrapper.
- **`(*Type).SameAs(u *Type) bool`** -- reports whether two types
  represent the same concrete type (same `Rtype` and `Name`). Used by
  `TypeBranch` for type-switch comparison and by `TypeAssert`.
- **`(*Type).ReturnType(i int) *Type`** -- returns the mvm-level
  i'th return type from `Returns` if available, else falls back to
  `Out(i)` (reflect-level). Preserves interface method metadata for
  multi-return functions.
- **`Iface{Typ *Type, Val Value}`** -- boxed interface value.
- **`Closure{Code int, Heap []*Value}`** -- captured function.
- **`SelectCaseInfo`** -- describes one case of a `select` statement:
  `Dir reflect.SelectDir` (Send/Recv/Default), `Slot`/`OkSlot` (memory
  indices for received value and ok bool, -1 if unused), `Local bool`
  (true if slots are frame-relative rather than global).
- **`SelectMeta`** -- compile-time metadata for a `select` block, stored
  in the data segment at a known index. Holds `Cases []SelectCaseInfo`
  and `TotalPop int` (total stack slots consumed by channel/value
  entries, precomputed so `SelectExec` can find the base without scanning).
- **`MvmFunc{Val Value, GF reflect.Value}`** -- wraps a mvm
  function value alongside its `reflect.MakeFunc`-generated Go wrapper.
  Stored when a mvm func is assigned to a struct field of func type
  so that native Go callbacks (e.g. HTTP handlers) can call back into
  the VM. `WrapFunc` creates it; the inner VM always dispatches via `Val`.

### Opcodes

- **`Op`** (int enum) -- 230+ opcodes organized in groups:
  - Stack/control: `Nop`, `Pop`, `Push`, `Swap`, `Exit`, `Jump`,
    `JumpTrue`, `JumpFalse`, `JumpSetTrue`, `JumpSetFalse`, `Call`,
    `CallImm`, `Return`.
  - Memory: `Get`, `GetLocal`, `GetGlobal`, `SetLocal`, `SetGlobal`,
    `SetS`, `Addr`, `Deref`, `DerefSet`, `Grow`.
    `SetLocal`/`SetGlobal` are specialized variants that write directly
    to frame-relative or global indices. `SetS` is used for addressable
    slots (calls `reflect.Value.Set` to propagate writes into the ref
    without touching `num`). Plain `Set` no longer exists as an opcode.
  - Arithmetic -- no generic numeric opcodes remain; every arithmetic op
    is statically typed:
    - `Equal`, `EqualSet` (type-agnostic comparison via `Value.Equal`).
    - `AddStr` -- string concatenation (`s1 + s2`); the only non-numeric
      binary add op.
    - Per-type variants (12 types each, selected at compile time via
      `NumKindOffset`):
      `AddInt`...`AddFloat64`, `SubInt`...`SubFloat64`,
      `MulInt`...`MulFloat64`, `DivInt`...`DivFloat64`,
      `RemInt`...`RemFloat64`, `NegInt`...`NegFloat64`,
      `GreaterInt`...`GreaterFloat64`, `LowerInt`...`LowerFloat64`.
  - Immediate: `AddIntImm`, `SubIntImm`, `MulIntImm`, `GreaterIntImm`,
    `GreaterUintImm`, `LowerIntImm`, `LowerUintImm`.
  - Bitwise: `BitAnd`, `BitOr`, `BitXor`, `BitAndNot`, `BitShl`,
    `BitShr`, `BitComp`.
  - Bit manipulation: `Clz32`, `Clz64`, `Ctz32`, `Ctz64`, `Popcnt32`,
    `Popcnt64`, `Rotl32`, `Rotl64`, `Rotr32`, `Rotr64`. Unary ops
    (clz/ctz/popcnt) set `ref = zint`; binary ops (rotl/rotr) preserve
    the input type. Implemented via `math/bits`.
  - Float math (unary): `AbsFloat32`, `AbsFloat64`, `SqrtFloat32`,
    `SqrtFloat64`, `CeilFloat32`, `CeilFloat64`, `FloorFloat32`,
    `FloorFloat64`, `TruncFloat32`, `TruncFloat64`, `NearestFloat32`,
    `NearestFloat64`. Implemented via `math`.
  - Float math (binary): `MinFloat32`, `MinFloat64`, `MaxFloat32`,
    `MaxFloat64`, `CopysignFloat32`, `CopysignFloat64`.
  - Collections: `Index`, `IndexAddr`, `IndexSet`, `MapIndex`,
    `MapIndexOk`, `MapSet`, `Slice`, `Slice3`, `Field`, `FieldSet`,
    `FieldFset`. `IndexAddr` takes an array/slice and index and pushes
    a pointer to the element (used for `&a[i]`). `MapIndexOk` pushes
    both value and ok (the `v, ok := m[k]` form).
  - Types: `Convert`, `TypeAssert`, `TypeBranch`, `Fnew`, `FnewE`,
    `New`, `MkSlice`, `MkMap`.
  - Builtins: `Append`, `AppendSlice`, `CopySlice`, `DeleteMap`, `Cap`,
    `Len`, `PtrNew`. `AppendSlice` packs N values into a `[]T` and calls
    `reflect.AppendSlice`; used when `append` receives multiple elements
    to avoid intermediate heap allocation.
  - Output: `Print`, `Println` -- dedicated opcodes for `print(v...)` and
    `println(v...)`; write to `m.out` directly without `reflect.Value.Call`.
  - Closures: `HeapAlloc`, `HeapGet`, `HeapSet`, `HeapPtr`, `MkClosure`.
  - Interfaces: `IfaceWrap`, `IfaceCall`.
  - Native interop: `WrapFunc` -- wraps a mvm function value in a
    `reflect.MakeFunc` adapter so it can be called by native Go code.
    Produces a `MvmFunc` on the stack.
  - Goroutines and channels:
    - `GoCall` -- pop function + N args, spawn a child `Machine` via
      `go` and return immediately; `A` = narg.
    - `GoCallImm` -- like `GoCall` but for a known non-closure function
      (avoids loading the function value from the stack); `A` = globals
      index of the function, `B` = narg.
    - `MkChan` -- create a channel; `A` = globals index of elem type,
      `B` = buffer size (negative means read size from stack).
    - `ChanSend` -- pop channel and value, send synchronously via
      `reflect.Value.Send`.
    - `ChanRecv` -- pop channel, push received value; if `A` == 1 also
      push the ok bool (two-result form `v, ok := <-ch`).
    - `ChanClose` -- pop channel, call `reflect.Value.Close`.
    - `SelectExec` -- execute a `select` statement. `A` = globals index
      of `SelectMeta`; `B` = number of cases. Pops channel/value entries
      off the stack (count from `meta.TotalPop`), calls `reflect.Select`,
      then writes the received value and ok bool into the slots named in
      `SelectMeta.Cases`. Pushes the chosen case index.
  - Range: `Next`, `Next0`, `Next2`, `NextLocal`, `Next2Local`, `Pull`,
    `Pull2`, `Stop`, `Stop0`. `Next0`/`Stop0` are used when the range
    variable is blank or absent. `NextLocal`/`Next2Local` are fast paths
    for local-scope iterators (single and double variable forms).
  - Super instructions (fused multi-op sequences):
    - `GetLocal2` -- push two locals in one dispatch.
    - `GetLocalAddIntImm`, `GetLocalSubIntImm`, `GetLocalMulIntImm` --
      load local + arithmetic with immediate.
    - `GetLocalLowerIntImm`, `GetLocalLowerUintImm`,
      `GetLocalGreaterIntImm`, `GetLocalGreaterUintImm` --
      load local + compare with immediate.
    - `GetLocalReturn` -- load local + return.
    - `LowerIntImmJumpFalse`, `LowerIntImmJumpTrue` -- compare with
      immediate + conditional jump (no boolean materialized on stack).
    - `GetLocalLowerIntImmJumpFalse`, `GetLocalLowerIntImmJumpTrue` --
      triple fusion: load local + compare + jump. `B` packs
      `localOff<<16 | imm&0xFFFF`.
  - Exceptions: `Panic`, `Recover`, `DeferPush`, `DeferRet`.
  - Debug: `Trap`.

## Internal design

### Memory layout

```
globals[0 .. dataLen-1]   global vars, func code addresses, string literals
                          (shared backing array across all goroutines)
mem[0 ..]                 per-goroutine call stack (frame-relative indices only)
```

The two slices were previously unified in a single `mem`. They were split so
that goroutines can share `globals` while running independent stacks.
`Push()` appends to `globals`; `GetGlobal`/`Set` (global scope) index into
`globals`; `GetLocal`/`Set` (local scope) index into `mem` relative to `fp`.

### Call frame

```
[ ... args | deferHead | retIP | prevFP | locals ... ]
                                  ^
                                  fp  (frameOverhead = 3 slots)
```

`Call` pushes `deferHead`, `retIP`, and `prevFP` onto the stack, then sets
`fp` past all three. If the caller has a non-nil closure `heap`, it is
saved to `Machine.heapFrames` and the high bit of `prevFP` is set
(`heapSavedFlag`). `Return` inspects `deferHead` (at `mem[fp-3]`) for
pending deferred calls, unpacks nret/frameBase from `retIP`, and
restores `heap` from `heapFrames` if the heap flag is set.

- `mem[fp-3]` -- `deferHead`: index of the topmost deferred-call record
  (0 = none). Updated by `DeferPush`.
- `mem[fp-2]` -- packed `retIP`: `[frameBase:16 | nret:16 | retIP:32]`.
  Encodes the return address, number of return values, and frame size
  (distance from fp to bottom of frame) in a single `uint64`.
  `packRetIP(retIP, nret, frameBase)` constructs the value.
- `mem[fp-1]` -- `prevFP`: the caller's frame pointer. High bit
  (`heapSavedFlag = 1<<63`) indicates a closure heap was saved to `heapFrames`.

`CallImm` is a specialized variant for direct calls to known functions.
It avoids loading the function value from memory and skips runtime type
dispatch. `A` holds the data index of the function; `B` packs
`narg<<16 | nret`.

### Per-type numeric ops

All arithmetic opcodes are statically typed -- there are no generic
`Add`/`Sub`/`Mul`/`Neg`/`Greater`/`Lower` opcodes. The compiler always
knows the operand kind at compile time and selects the exact opcode from
the 12-variant block using `NumKindOffset`:

```
opcode = baseOp + Op(NumKindOffset[reflect.Kind])
```

`NumKindOffset` is a fixed array (indexed by `reflect.Kind`) that maps
each numeric kind to a 0-based slot: 0=int, 1=int8, ..., 9=uint64,
10=float32, 11=float64. Non-numeric kinds return -1 (the compiler panics
before reaching that state).

String concatenation (`s1 + s2`) is handled by the dedicated `AddStr`
opcode rather than a typed numeric block.

The helper functions `add[T]`, `sub[T]`, etc. in `numops.go` use Go
generics internally; each typed opcode dispatches to exactly one
instantiation with zero runtime branching.

Float32 values are stored as `math.Float64bits(float64(f32value))` --
the float32 is promoted to float64 before encoding into `uint64`. The
helpers `getf32(n uint64) float32` and `putf32(f float32) uint64` in
`numops.go` encapsulate this encoding for the float math opcodes.

### Instruction encoding

`Instruction` is a fixed-size 16-byte struct:

```go
type Instruction struct {
    Op   Op    // opcode
    A, B int32 // up to 2 immediate operands (0 when unused)
    Pos  Pos   // source position
}
```

Earlier versions used `{Op Op; Arg []int}`, which heap-allocated an arg
slice per instruction. The flat layout avoids allocation and improves
cache locality in the dispatch loop. Super instructions that need more
than two operands pack them into `A` and `B` (e.g. `GetLocalLowerIntImmJumpFalse`
packs `localOff<<16 | imm&0xFFFF` into `B`).

### Get specialization

`Get` dispatches on a scope flag (`Global` vs `Local`) at runtime.
Two specialized opcodes avoid this branch:

- `GetLocal` -- reads `mem[A + fp - 1]` directly.
- `GetGlobal` -- reads `mem[A]` and syncs `num` from `ref` for
  addressable slots (needed when `SetS` updated `ref` without touching
  `num`).

The compiler emits `GetLocal`/`GetGlobal` whenever the scope is known
statically. `Get` remains for cases requiring runtime scope resolution.

### Super instructions

The compiler fuses common multi-instruction sequences into single
opcodes that perform multiple operations in one dispatch cycle. Three
levels of fusion exist:

**Level 1 -- GetLocal + operation:**

| Opcode | Equivalent sequence |
|--------|---------------------|
| `GetLocal2` | `GetLocal A; GetLocal B` |
| `GetLocalAddIntImm` | `GetLocal A; Push B; AddInt` |
| `GetLocalSubIntImm` | `GetLocal A; Push B; SubInt` |
| `GetLocalMulIntImm` | `GetLocal A; Push B; MulInt` |
| `GetLocalLowerIntImm` | `GetLocal A; Push B; LowerInt` |
| `GetLocalGreaterIntImm` | `GetLocal A; Push B; GreaterInt` |
| `GetLocalReturn` | `GetLocal A; Return` |

**Level 2 -- compare + jump:**

| Opcode | Effect |
|--------|--------|
| `LowerIntImmJumpFalse` | `if n >= B { ip += A }; sp--` |
| `LowerIntImmJumpTrue` | `if n < B { ip += A }; sp--` |

These avoid materializing a boolean on the stack. The compiler rewrites
`Greater` comparisons using the identity `a > imm` = `!(a < imm+1)`.

**Level 3 -- triple fusion (GetLocal + compare + jump):**

| Opcode | Effect |
|--------|--------|
| `GetLocalLowerIntImmJumpFalse` | `if local >= imm { ip += A }` |
| `GetLocalLowerIntImmJumpTrue` | `if local < imm { ip += A }` |

`B` packs `localOff<<16 | imm&0xFFFF`. No stack operations at all --
the local is read, compared, and the branch is taken in a single
dispatch.

### Stack growth pre-computation

The compiler tracks the maximum expression depth per function and stores
it in the `Grow` instruction's `B` field. At function entry, `Grow`
pre-allocates `A + B` slots (locals + max expression depth). This
guarantees that `GetLocal` and the fused super instructions can access
stack slots without bounds checks within the function body.

### Closure dispatch

When a closure is called, `Machine` swaps in its `Heap` (saved heap cells)
and restores the caller's heap on return. `HeapGet`/`HeapSet` read/write through
`heap[i]` pointers.

### Goroutines and channels

`GoCall` and `GoCallImm` both call `newGoroutine(fval, args)`, which creates
a child `Machine` with:

- `globals` pointing to the parent's `globals` slice (same backing array --
  writes in either direction are immediately visible to the other).
- A fresh `mem` slice containing the function value and argument copies.
- A private copy of `code` with a `Call + Exit` epilogue appended at
  `baseCodeLen`, so the goroutine's entry point is a normal call sequence.
- `wg` pointing to the parent's `*sync.WaitGroup`.

The goroutine runs via `go func() { child.Run() }()`. The parent does not
wait for it; instead, `Run` waits for all spawned goroutines at exit via
`m.wg.Wait()` when `!m.isGoroutine` (the top-level machine only).

Channel operations delegate entirely to `reflect`: `reflect.MakeChan`,
`reflect.Value.Send`, `reflect.Value.Recv`, and `reflect.Value.Close`.
The channel value is stored as a `Value{ref: reflect.Value}` on the stack
and in variable slots like any other composite type.

`GoCallImm` applies the same optimization as `CallImm` for regular calls:
when the target is a named non-closure function, the compiler removes the
preceding `GetGlobal` and encodes the globals index directly in the
instruction, avoiding one stack read.

### Panics, defer, recover, and diagnostics

- `DeferPush` saves a sentinel frame pointing to a deferred function.
- `Panic` sets `panicking = true` and unwinds, calling deferred functions.
- `Recover` clears the panic state if called inside a deferred function.
- `DeferRet` is emitted at function exit to run deferred functions in LIFO
  order.

**Diagnostics: `PanicError`.** When a raw Go panic (nil deref, divide by
zero, a `reflect.Convert` mismatch, an explicit interpreted `panic(...)`)
escapes the VM, `recoverPanic` wraps it in a `*PanicError` (`vm/debug.go`)
that carries the panic value, the source `Pos` and bytecode `IP` of the
faulting instruction, the captured call frames, and the `DebugInfo`
snapshot. `capturePanic` builds it *before* the stack unwinds, so
`PanicError.Error()` can render a header, a source snippet with a caret,
and an `mvm stack:` trace that interleaves interpreted frames (with
`file:line:col` and function name) and `-- via ... [native] --` markers
for reentrant native calls. Symbolization reuses `DebugInfo.FuncAt` and
`Sources.Snippet`, so a program that never panics pays nothing.

**Clean-exit passthrough.** `recoverPanic` does a *shape* check rather
than a type check: any recovered value that is an `error` but not a
`runtime.Error` is propagated unwrapped, and anything implementing the
`CleanExit` marker interface bypasses interpreted `recover()` entirely
and reaches the top-level `Run`. This is how `*interp.ExitError` (from
the virtualized `os.Exit` / `log.Fatal*` bindings) surfaces as a clean
signal instead of a crash, while keeping `vm` free of any `interp`
dependency. Genuine `runtime.Error` crashes keep the full `capturePanic`
diagnostics. See
[ADR-018](../decisions/ADR-018-virtualized-process-exit.md) and
[interp.md](interp.md#process-exit-virtualization).

**Nil-func guard.** Function code never lives at code index 0 (that slot
holds the program-entry `Jump`), so a resolved call target of
`nilFuncAddr = 0` means the func value was nil or a global slot was
corrupt. `Call`/`CallImm` panic with a Go nil-func deref in that case
rather than jumping to 0, which would re-run the program (or `_testmain`)
and recurse without bound.

### Native Go interop (WrapFunc / CallFunc)

Mvm functions are integers (code addresses) or `Closure` values at
runtime. Neither can be stored directly in a typed Go `func` field via
`reflect.Set`. Two mechanisms bridge this gap:

1. **`funcFields` side-table.** A `funcFieldsTable` (a
   `map[uintptr]Value` behind a `sync.RWMutex`) holds mvm funcs assigned
   to native struct func fields. Lookup is keyed by the closure's
   function pointer read live from the field's bytes (`funcValuePtr`),
   so a `reflect.Set` that overwrites the field still resolves to the
   right closure on the next `Call`. The table is parent-owned and
   pointer-shared with runner Machines and spawned goroutines, so a
   callback's write is observable to siblings and the parent.
   When the compiler detects assignment of a mvm func to a native struct
   field, it emits an instruction that stores the mvm func into the table
   while writing a zero/stub into the actual field.

2. **`WrapFunc` opcode.** Converts the mvm func on the stack into a
   `MvmFunc` by calling `reflect.MakeFunc` with a trampoline that
   re-enters the VM via `Machine.CallFunc`. The resulting `GF`
   `reflect.Value` is assignable to any Go func field of the matching type.

### Re-entrant execution (CallFunc)

`CallFunc` provides re-entrant VM execution for native Go callbacks. It
saves all volatile state (`mem`, `ip`, `fp`, `heap`, `heapFrames`, panic
state, code length), resets per-call state, copies globals to a fresh
stack, pushes the function value and arguments, appends a temporary
`Call` + `Exit` sequence, and runs the inner loop. On return (including
via `defer`), all saved state is restored.

Native callbacks dispatched from multiple goroutines (e.g. `sort.Slice`
on a wrapped mvm comparator, parallel `strings.Map`, fmt verb callbacks)
are safe: each dispatch acquires its own runner Machine from the
parent's `sync.Pool` and the user-invisible internal tables
(`funcFields`, `typesByRtype`) are parent-owned and synchronised so a
runner's mutation is visible to siblings and the parent.
Direct invocation of `m.CallFunc` on the parent `Machine` itself remains
single-goroutine entry (it mutates `m.mem`/`m.ip`/`m.fp` directly); the
spec-safe public concurrent entry is `WrapFunc` -> trampoline -> pooled
runner.

Note that `CallFunc` does NOT isolate `globals`: a callback's package-var
write is visible to the outer `Run`, matching Go callback semantics
(a native callback runs in the same address space as the surrounding
program) and the goroutine model documented in ADR-008. Only per-frame
state (`mem`, `ip`, `fp`, `heap`, `heapFrames`, `panicking`, `panicVal`,
appended `code`) is saved and restored. Concurrent user-code writes to
package-level vars follow the Go memory model exactly as in native Go;
mvm does not layer extra locking over `globals`.

`makeCallFunc` (the trampoline returned by `WrapFunc`) acquires a
pooled runner Machine via `runnerState.acquireRunner`, runs the
function with `callPooled`, and releases the runner back to the pool on
return. The `runnerState` captured at WrapFunc time carries
pointer-shared references to the parent's `funcFields`, `typesByRtype`,
`code` / `globals` headers, the `sync.Pool` itself, and `debugInfoFn` --
so bridges that introspect the callback runner (e.g. the
runtime.Callers replacement, see [Runtime virtualization
bridges](#runtime-virtualization-bridges)) can still resolve symbols
through `m.DebugInfo()`.

### Trap and interactive debug mode

The `Trap` opcode pauses VM execution and enters an interactive debug
session. It is emitted by the compiler for calls to the `trap()` builtin.

**Sentinel opcodes.** Out-of-band control flow (defer return, panic unwind,
debug trap) is handled via sentinel opcodes (`DeferRet`, `PanicUnwind`)
appended to the code array at `Run` entry. The `Trap` opcode handles debug
entry inline: it saves the resume address in `trapOrig`, syncs Machine
state, and calls `enterDebug()`. When the user types `cont`, `enterDebug`
returns and execution resumes from `Machine.ip`.

**Debug fields on Machine:**

- `debugInfoFn func() *DebugInfo` -- builder registered by the interpreter.
  Called on demand inside `enterDebug` to produce symbolic info (labels,
  globals, locals, source registry). Not cached on Machine.
- `debugIn` / `debugOut` -- I/O overrides for the debug REPL (default:
  stdin/stderr). Tests inject buffers here.
- `trapOrig int` -- the ip to resume after the debug session ends.

**Debug REPL commands:**

| Command | Action |
|---------|--------|
| `bt`, `stack` | Dump the full call stack with frame layout and symbol names |
| `c`, `cont` | Continue execution |
| `h`, `help` | Show available commands |

**DebugInfo** (`vm/debug.go`) holds symbolic metadata populated by
`comp.Compiler.BuildDebugInfo()`: a `scan.Sources` registry for
multi-file/REPL position resolution, label-to-name mappings,
global-index-to-name mappings, and per-function local variable lists.
`DumpFrame` and `DumpCallStack` use this information to annotate memory
slots with human-readable names and source positions.
`Machine.DebugInfo()` returns the result of `debugInfoFn` (or nil) and
is the entry point used by native bridges that need symbolic info.

`DumpCallStack` walks the frame-pointer chain directly today; the same
walk has been factored into the public `WalkCallStack` iterator (see
[Call-stack walking](#call-stack-walking)) for reuse by bridges and the
planned profiler. A future cleanup can fold `DumpCallStack` onto that
iterator once `StackFrame` exposes the per-slot annotation state
(`narg`, `nret`, `funcAddr`).

### Call-stack walking

`Machine.WalkCallStack(yield)` (`vm/debug.go`) is the public iterator
over the live call stack. It yields `StackFrame{IP, Pos}` from
innermost (the currently running function) to outermost; the first
frame's `IP` is `m.ip-1` (the just-executed instruction), each
subsequent frame's `IP` is `retIP-1` taken from the inner frame's
`mem[fp-2]` slot -- the call instruction in the caller, whose `Pos`
points at the source line of the call. `yield` returns `false` to stop
early.

Symbolization is not the iterator's job; callers compose with:

- `DebugInfo.FuncAt(ip)` -- linear scan of `Labels` returning the
  function whose code address is the largest `<= ip`.
- `DebugInfo.Sources.Resolve(int(Pos))` -- (file, line, col) for a
  byte offset.

Splitting walk from symbolize matters for the planned sampling
profiler, which must produce IPs cheaply on a tick and resolve only at
profile-export time. Today's consumers are the `runtime.Callers`
bridge (see [Runtime virtualization bridges](#runtime-virtualization-bridges))
and (partially) `DumpCallStack`.

### Native method dispatch via synthesized rtypes

Go's `reflect.StructOf` cannot attach methods to dynamically-created types, so
an interpreted struct (or named type) passed to a native function as an
`interface{}` would not satisfy `fmt.Stringer`, `error`, `json.Marshaler`, etc.
The VM closes this gap by attaching the interpreted methods to a *synthesized*
rtype that native `itab`/reflect dispatch reads directly -- no per-call wrapper.

`Machine.AttachSynthMethods(t)` (`vm/synth_bridge.go`) runs for every compiled
type. For each method it:

1. matches the signature to a *shape* (`detectShape` -> `stubs.ShapeS1..S38`),
   falling back to a word-class shape (`detectWordShape`,
   [ADR-022](../decisions/ADR-022-word-class-dispatch.md)) when no typed shape
   fits;
2. builds a handler closure (`makeHandlerS*`, or `makeWordCore` for the word
   path) that re-enters the interpreter via `CallFunc` when the stub fires;
3. delegates to `stdlib/stubs`, which resolves each method to a stub-pool slot
   PC and fills them into the type's reserved synth rtype in place.

The type's synth identity was reserved at materialize (`maybeReserve` /
`maybeReserveStruct` in `derive/derive.go`), so the fill is in place and no rtype swap
or cascade is needed -- a composite that captured the reserved rtype sees the
methods afterward. See [runtype](runtype.md), [stubs](stubs.md), and
[ADR-021](../decisions/ADR-021-synthesized-rtypes.md). This replaced the former
per-call bridge registries and argument proxies -- `vm/bridge.go` no longer holds
`Bridges`/`DisplayBridges`/`CompositeBridges`/`InterfaceBridges`.

**`IfaceWrap` / `bridgeArgs`.** The compiler still emits `IfaceWrap` for
interface-typed arguments to native calls, boxing the value as `Iface{Typ, Val}`
so the mvm `*Type` identity crosses the boundary (essential for non-struct named
types whose `reflect.Type` is shared with the underlying type). `bridgeArgs(in,
funcType, fn)` then, per argument: retypes a pointer-to-interpreted-interface to
the synthesized interface rtype (`bridgePtrToIface`, a gated allowlist, for
`errors.As`), otherwise unwraps the `Iface` to its concrete (synthesized) value
via `bridgeIface`.

**Pointer-receiver method dispatch.** `IfaceCall` resolves the method set via
`Type.ResolveMethodType`, which walks to `ElemType.Methods` for pointer types,
so methods on `T` are visible when the concrete value is `*T`;
`runtype.ReservePtrMethods` mirrors this on the runtype side by wiring `*T`'s
`PtrToThis`.

**`MethodNames []string`** on `Machine` maps global method IDs back to names
(populated by the compiler after each `Compile`); the dispatch handlers use it
to call methods promoted from embedded interfaces.

**Native-receiver hook.** `RegisterNativeMethodHook(recvInstance, name, hook)`
(`vm/bridge.go`) substitutes the result of a named method on a *native*
receiver. The runtime virtualization bridge uses it for
`(*runtime.Func).Name`/`FileLine` -- see [Runtime virtualization
bridges](#runtime-virtualization-bridges).

### Runtime virtualization bridges

Some host APIs round-trip opaque pointers (PCs, handles) through user
code and back again. `runtime.Callers` / `runtime.FuncForPC` is the
canonical case: pkg/errors stores PCs in `Frame uintptr` and later
calls `(*runtime.Func).Name()` / `FileLine()` to format them. A
plain reflect passthrough makes those calls return host frames inside
`vm.(*Machine).Run` rather than the interpreted call stack.

The VM provides three primitives in `vm/runtime_intercept.go`; the
bridge itself lives in `stdlib/runtime_virt.go` and registers via
`stdlib.RegisterPackagePatcher("runtime", ...)`.

**1. Active-machine slot.** Bridges run as Go functions called via
`reflect.Call`; they need to find the live `Machine`. `Run` threads the
current value through:

```go
prev := SetActiveMachine(m)
defer SetActiveMachine(prev)
```

`activeMachine` is an `atomic.Pointer[Machine]`, single-machine-at-a-
time semantics. Concurrent goroutines racing on this slot is no worse
than under a mutex+stack alternative because the slot is a single
pointer; per-goroutine GLS would require unsafe-G inspection or
`runtime/pprof` labels.

**2. Synthetic PC sentinels.** An mvm-virtual PC is the address of a
freshly allocated `*runtime.Func`. Two non-obvious bits:

- `runtime.Func` is a zero-sized struct; Go reuses one canonical
  address for all zero-size allocations. `runtimeFuncSentinel` wraps
  it with a padding byte so each `NewRuntimeFuncSentinel()` returns a
  unique pointer.
- `RegisterRuntimeFunc(rf, name, file, line)` stores
  `RuntimeFuncInfo{...}` in a side `sync.Map`; `LookupRuntimeFunc(rf)`
  returns nil for non-mvm pointers so a host PC can fall through.

**3. Method intercept.** `nativeMethodLookup` (top of the
`MethodByName` path) calls `runtimeFuncShim(rv, name)` first. On a hit
(receiver type is `*runtime.Func`, pointer is in the side table, name
is `"Name"` or `"FileLine"`) the shim returns a `reflect.MakeFunc`
closure with the matching signature; the host method never runs. On a
miss, the shim returns the zero `reflect.Value` and the regular lookup
proceeds. Cost on the miss path is one type-pointer compare.

The VM also synchronizes its execution locals into the `Machine`
struct just before the native `rv.Call(in)`:

```go
m.mem, m.fp, m.ip = mem, fp, ip+1
```

This is a one-way push; the local copies remain authoritative. Bridges
that introspect via `m.WalkCallStack`, `m.fp`, `m.ip`, etc. need this
because `Run` keeps those fields stale until exit otherwise.

```mermaid
sequenceDiagram
    participant User as Interpreted code
    participant VM
    participant Bridge as stdlib/runtime_virt
    participant Walk as WalkCallStack
    participant Side as Side table
    User->>VM: runtime.Callers(skip, pcs)
    VM->>Bridge: rv.Call (after sync mem/fp/ip)
    Bridge->>Walk: m.WalkCallStack(yield)
    Walk-->>Bridge: StackFrame{IP, Pos} per frame
    Bridge->>Side: NewRuntimeFuncSentinel + Register
    Bridge-->>User: pcs[i] = uintptr(sentinel) + 1
    User->>VM: fn := runtime.FuncForPC(pc)
    VM->>Bridge: rv.Call (mvmFuncForPC)
    Bridge->>Side: LookupRuntimeFunc(pc-1)
    Bridge-->>User: *runtime.Func sentinel
    User->>VM: fn.Name() / fn.FileLine(pc)
    VM->>VM: nativeMethodLookup -> runtimeFuncShim
    VM-->>User: shim returns metadata from side table
```

**Limitations:**

- Sentinels are interned by `(IP, Pos)` in `stdlib/runtime_virt.go`'s
  `sentinelByFrame` map, so `runtimeFuncMeta` size is bounded by the
  number of distinct interpreted call sites (typically thousands)
  rather than the number of stack captures. Long-running embedders
  that recompile the program many times still grow the map across
  recompiles -- nothing sweeps the cache automatically. A public
  `Clear` would be straightforward to add when needed.
- Single-machine-at-a-time semantics leaks under truly concurrent
  goroutine execution.

See [ADR-016](../decisions/ADR-016-runtime-introspection-bridge.md).

## Dependencies

- `scan` -- for `scan.Sources` (source position registry used by `DebugInfo`).
- `runtype` -- rtype synthesis + derive helpers used by `derive/` and
  `vm/synth_bridge.go` (see [runtype](runtype.md)).
- `stdlib/stubs` -- the method-shape catalog and `Attach*` wrappers used by
  `vm/synth_bridge.go` (see [stubs](stubs.md)). `stubs` is a leaf package
  (it imports only `runtype`), so this introduces no cycle.
