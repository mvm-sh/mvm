# comp

> Bytecode compiler: walks the flat token stream, resolves symbols, emits
> VM instructions.

## Overview

The `comp` package bridges parsing and execution. Its `Compiler` embeds the
parser and compiles source in two phases: first resolving all declarations,
then generating bytecode. It emits `vm.Instruction` values into a `Code`
slice and populates a `Data` slice (the global memory segment).

## Key types and functions

- **`Compiler`** -- embeds `*goparser.Parser`. Manages `Code`, `Data`,
  `Entry` (start IP), string deduplication (`strings` map), method ID
  allocation (`methodIDs` map), a type-pointer dedup cache (`typeIdxs`),
  and `posBase` (byte offset of the current source in the `Sources`
  registry for position resolution).
- **`Compile(name, src string) error`** -- end-to-end compilation. Delegates
  Phase 1 (declaration resolution with retry loop) to `ParseAll`, then runs
  `allocGlobalSlots` and Phase 2 code generation (var initializers first,
  then func bodies). `name` identifies the source (`"m:<content>"` for
  inline, `"f:<path>"` for file).
- **`Dump() / ApplyDump(d)`** -- snapshot and restore global variable
  state (used for REPL resets).

## Internal design

### Code generation

`generate(tokens)` iterates over the flat token stream. For each token it:

1. Looks up the corresponding symbol in `SymMap`.
2. Emits `Get`/`Set`/`Push` instructions based on symbol kind and locality.
3. For operators, emits the statically-typed opcode; `numericOp()` selects
   the exact per-type opcode using `vm.NumKindOffset`. For `+` on strings,
   emits `AddStr`. Panics if the type is unresolved or non-numeric.
4. For `Label`, records the code address; for `Goto`/`JumpFalse`, emits
   jumps and patches targets.

A symbolic stack shadows the VM stack to track types at compile time,
enabling type-specific opcode selection.

### Call handling

The `lang.Call` token in the flat stream triggers a unified call-handling
path. The compiler distinguishes two cases based on the callee symbol's kind:

- **Mvm function** (`Kind: Func`, `LocalVar`, etc.) -- emits `vm.Call`
  after optionally packing variadic args with `MkSlice`.
- **Native Go function value** (`Kind: Value`) -- also emits `vm.Call`;
  the `Call` opcode handler detects a `reflect.Func` at runtime and
  dispatches via `reflect.Value.Call` directly.

The `lang.CallX` parser token and the `vm.CallX` opcode were both removed;
the distinction between mvm and native callees is now made entirely
inside the `case lang.Call` handler using the compile-time symbolic stack.
Builtin symbols (`Kind: Builtin`) are intercepted by `compileBuiltin`
before either path is reached.

### Peephole optimization and instruction fusion

The compiler applies several layers of instruction fusion after emission,
each building on the previous:

1. **Immediate folding** (`retractPush`). If the preceding instruction was
   a `Push` of an integer constant, folds it into the binary op
   (e.g. `Push 1; AddInt` becomes `AddIntImm 1`).

2. **GetLocal fusion** (`fuseGetLocal`). If the instruction before the
   immediate op is `GetLocal`, replaces both with a super instruction
   (e.g. `GetLocal 2; AddIntImm 1` becomes `GetLocalAddIntImm A=2 B=1`).
   Also fuses `GetLocal + Return` into `GetLocalReturn` and consecutive
   `GetLocal` pairs into `GetLocal2`.

3. **Compare + jump fusion** (`fuseCmpJump`). When emitting `JumpFalse`
   after a comparison immediate, fuses both into a single opcode
   (e.g. `LowerIntImm; JumpFalse` becomes `LowerIntImmJumpFalse`).
   Also handles the GetLocal-fused variants, producing triple-fused
   instructions like `GetLocalLowerIntImmJumpFalse`. The compiler rewrites
   `GreaterIntImm; JumpFalse` as `LowerIntImmJumpTrue` using the identity
   `a > imm` = `!(a < imm+1)`, keeping only `Lower`-based fused ops.

### CallImm and GoCallImm

When calling a declared function (not a closure, not a variable), the
compiler emits `CallImm` instead of loading the function value and
emitting `Call`. `CallImm` encodes the data index in `A` and packs
`narg<<16 | nret` in `B`, skipping the runtime function-value dispatch
entirely. `removeGetGlobal` retracts the preceding `GetGlobal` that
loaded the function address.

`GoCallImm` applies the same optimization to `go` statements: if the
target is a named non-closure function, `removeGetGlobal` retracts the
`GetGlobal` and the compiler emits `GoCallImm` with `A` = globals index,
`B` = narg. Otherwise it emits `GoCall narg`, which reads the function
value from the stack at runtime.

### Intrinsics

The compiler replaces calls to known standard library functions with
direct VM opcodes, avoiding the reflection-based native `Call` path
(which allocates a `[]reflect.Value`, converts arguments, and dispatches
via `reflect.Value.Call`).

`compileIntrinsic` is checked in the `lang.Call` handler right after
`compileBuiltin`. It looks up the symbol's qualified name (e.g.
`"math.Abs"`, `"math/bits.LeadingZeros64"`) in the `intrinsicOp` table.
On a match it removes the preceding `GetGlobal` (via `removeGetGlobal`),
pops argument/function symbols, pushes the return type, and emits the
opcode directly -- no frame setup, no reflection.

Current intrinsic mappings:

| Function | Opcode |
|----------|--------|
| `math.Abs` | `AbsFloat64` |
| `math.Sqrt` | `SqrtFloat64` |
| `math.Ceil` | `CeilFloat64` |
| `math.Floor` | `FloorFloat64` |
| `math.Trunc` | `TruncFloat64` |
| `math.RoundToEven` | `NearestFloat64` |
| `math.Min` | `MinFloat64` |
| `math.Max` | `MaxFloat64` |
| `math.Copysign` | `CopysignFloat64` |
| `math/bits.LeadingZeros[32\|64]` | `Clz32` / `Clz64` |
| `math/bits.TrailingZeros[32\|64]` | `Ctz32` / `Ctz64` |
| `math/bits.OnesCount[32\|64]` | `Popcnt32` / `Popcnt64` |
| `math/bits.RotateLeft[32\|64]` | `Rotl32` / `Rotl64` |

The opcode set is intentionally aligned with WASM's computational
instructions to enable a future WASM-to-mvm translation path.
See [ADR-010](../decisions/ADR-010-intrinsics.md).

### Goroutine and channel compilation

**`go` statements.** `lang.Go` tokens are emitted by the parser's
`parseGo`, which reuses `parseExpr` for the callee expression and
`parseBlock` for arguments. The result is the callee postfix output
followed by argument tokens followed by a `lang.Go{narg}` token --
the same shape as a call statement but with `lang.Go` instead of
`lang.Call`. The compiler's `case lang.Go` handler applies `GoCallImm`
when possible (named non-closure function), otherwise emits `GoCall`.

**Channel send.** `parseChanSend(in, arrowIdx)` splits the statement at
`<-`, parses both sides as expressions, and appends a `lang.ChanSend`
token. The compiler's `case lang.ChanSend` handler emits `vm.ChanSend`.

**Channel receive.** `<-ch` in an expression is handled as a unary
operator (`lang.Arrow`) during `parseExpr`. The compiler's
`case lang.Arrow` handler emits `vm.ChanRecv A=0` (single-value form)
or `vm.ChanRecv A=1` (two-result form `v, ok := <-ch`). The ok-form
is signalled by the parser setting `t.Arg[0] = 1` on the `Arrow` token.

**Channel type.** `parseTypeExpr` recognises `chan T` and calls
`vm.ChanOf(reflect.BothDir, elemType)`. Directional channels
(`chan<-`, `<-chan`) are parsed but currently treated as bidirectional.

**`make(chan T[, n])`.** `compileBuiltin` for `make` dispatches on the
reflect kind of the first argument's type. For `reflect.Chan` it emits
`MkChan` with the elem type index and buffer size. An explicit size
argument leaves its value on the stack; the opcode reads it by passing
`B = -1`. An absent size argument uses `B = 0` (unbuffered).

### Stack growth computation

The compiler tracks `maxExprDepth` per function scope -- the high-water
mark of the expression stack above the local variable area. At function
end, it patches the `Grow` instruction's `B` field with this value so
the VM can pre-allocate `locals + maxExprDepth` slots at function entry,
enabling bounds-check-free stack access within the function body.

### Select statement compilation

`select` blocks reach the compiler as a `lang.Select` token whose `Arg[0]`
holds a `[]goparser.SelectCaseDesc` slice (one entry per case, produced
by `parseSelect` in the parser). The compiler's `case lang.Select` handler:

1. Pops stack entries in reverse order (channels and send values for each
   non-default case).
2. Allocates or reuses variable slots for each `recv` case's value and ok
   variables, emitting `New` for locals.
3. Builds a `*vm.SelectMeta` with `Cases []SelectCaseInfo` and stores it
   in `Data` at a fresh index.
4. Emits `SelectExec metaIdx ncase`.

At runtime, `SelectExec` uses `reflect.Select` to block until one case is
ready, then writes the received value and ok bool into the pre-allocated
slots using `meta.Cases`.

### Two-phase compilation

`Compile` delegates Phase 1 to `goparser.ParseAll` and handles Phase 2
directly:

1. **Phase 1 -- Declarations** (in `goparser.ParseAll`). Splits the source
   into top-level declarations, pre-registers struct type placeholders,
   and runs a retry loop passing each declaration to `ParseDecl`. Returns
   the remaining declarations (func bodies, var initializers) after
   topological sorting. See [goparser](goparser.md#package-and-import-handling)
   for details.

2. **Phase 2 -- Code generation** (in `Compile`). `allocGlobalSlots`
   pre-assigns data indices for every `Var` and `Func` symbol. Code is
   then generated in two passes:
   - **Pass 1:** var initializers, so all global var types are concrete.
   - **Pass 2:** func bodies and expression statements.

   Because all symbols have allocated slots, Phase 2 needs no retries or
   rollback machinery.

#### allocGlobalSlots

After Phase 1, every `Func` and `Var` symbol has a signature or type but
`Index == UnsetAddr`. `allocGlobalSlots` iterates the symbol table and
assigns a `Data` slot to each, appending the symbol's `Value` (or a
`NewValue` zero for uninitialized vars). Type and Value symbols are still
allocated lazily in the `Ident` handler, since many built-in types may
never be referenced.

### Variadic call-site packing

When calling a variadic function, the compiler emits `MkSlice` to collect
the trailing arguments into a `[]T` before `Call`. The number of fixed
parameters is computed from the function type; `MkSlice` receives the count
of extra arguments and the element type index. The callee sees a normal
slice parameter.

### Built-in function dispatch

`compileBuiltin()` intercepts calls to Go builtins by matching on
`Symbol.Name`. It is called from the `lang.Call` handler (which now
handles both mvm function calls and native Go value calls). Each
builtin emits a dedicated opcode:

| Builtin | Opcode(s) | Notes |
|---------|-----------|-------|
| `print` | `Print` | Registered as `Kind: Builtin`; emits `vm.Print narg` directly |
| `println` | `Println` | Same pattern; emits `vm.Println narg` |
| `len` | `Len` + `Swap` + `Pop` | `Len` does not consume input (used in slice exprs too) |
| `cap` | `Cap` + `Swap` + `Pop` | Same pattern as `len` |
| `append` | `Append` (1 value) or `AppendSlice` (N values) | `AppendSlice` packs N trailing args into `[]T` via `reflect.AppendSlice`; avoids intermediate heap allocation |
| `copy` | `CopySlice` | Returns element count |
| `delete` | `DeleteMap` + `Pop` | Void; extra `Pop` discards the map value |
| `new` | `PtrNew` | Removes the `Fnew` emitted for the type argument |
| `make` | `MkSlice` (negative n) / `MkMap` / `MkChan` | Reuses `MkSlice` with negative `Arg[0]` for make-slice mode; `MkChan` for `make(chan T[, n])` |
| `close` | `ChanClose` | Pops channel; closes it via `reflect.Value.Close` |
| `panic` | `Panic` | |
| `recover` | `Recover` | |
| `trap` | `Trap` | Zero arguments; pauses VM and enters interactive debug mode |

For `new` and `make`, the first argument is a type, not a value. The
parser's `Ident` handler emits a `Fnew`/`FnewE` instruction for type
symbols; `compileBuiltin` removes it via `removeFnew()` and uses the
type's data index directly.

### Method and interface dispatch

The compiler maintains a `methodIDs` map assigning unique integers to
method names. When a concrete type is wrapped in an interface
(`IfaceWrap`), the compiler verifies that all required methods exist.
`IfaceCall` dispatches by method ID at runtime.

## Dependencies

- `goparser/` -- token stream and parser.
- `symbol/` -- symbol table.
- `vm/` -- instructions, opcodes, `Value`, `Type`.
- `lang/` -- token types.
