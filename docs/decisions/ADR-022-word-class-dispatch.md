# ADR-022: Word-class ABI register shapes for method dispatch

**Status:** accepted
**Date:** 2026-06-11

Extends [ADR-021](ADR-021-synthesized-rtypes.md) (synthesized rtypes for native
method dispatch); ADR-021's split between `runtype` and `stdlib/stubs` still
stands.

## Context

A synthesized rtype carries each interpreted method as a text-segment function
pointer in its method table (`Ifn`/`Tfn`).
ADR-021 supplies those pointers from `stdlib/stubs`: per method-signature
*shape* (`S1` = `func() string`, `S13` = `func([]byte) (int, error)`, ...) a
generated pool of stub functions plus one hand-written handler/dispatcher.

The constraint that makes a shape per signature necessary is the ABI.
A method-table stub is invoked by native `itab` dispatch, so its Go function
signature must produce the exact calling convention the method's signature
produces -- the stub cannot be `func(...any)`.
The original answer was to enumerate each signature: a `gen_pools.go` entry, a
`registry_sN.go` handler, and a `detectShape`/`makeHandlerSN` case in
`vm/synth_bridge.go`, three coordinated edits per shape.

That enumeration has two failure modes.
First, it grows linearly against the standard library's interface surface
(the `io/fs` and `log/slog` clusters each cost a batch of shapes, S22..S36).
Second, and fatally, a typed stub cannot *name* an interpreted type at all:
`func (x StructA) Equal(y StructA) bool` on an interpreted `StructA` has no
expressible typed-stub signature, so go-cmp's `Equal`-method types silently
lost their methods and fell back to structural diffing.

The observation that unblocks it: many distinct *Go* signatures share one *ABI*.
`func(string) (fs.File, error)` and `func(string) (io.Reader, error)` both pass a
string as a (pointer, length) register pair and return two interface words plus
an error interface; at the register level they are the same function.
And `Equal(StructA) bool` with `StructA = struct{ X string }` is, at the
register level, just `func(ptr-word, int-word) int-word`.
One stub family can serve every signature that classifies to the same sequence
of registers, including signatures over types that do not exist until runtime.

## Decision

Classify each parameter and result type into ABI register words over `{p, i}`
(`p` = a pointer-containing word the GC must scan, `i` = an integer word), and
key dispatch on the resulting *word-shape* rather than the exact Go signature.
Generate one stub pool and one generic, reflect-driven dispatcher per word-shape;
a method whose signature has no typed shape but whose ABI maps to a generated
word-shape attaches through the word path instead of being dropped.

A handful of word-shapes covers a wide signature family.
This list, unlike the typed catalog, does not grow per new signature:

| Key | Signature shape | Example methods |
|-----|-----------------|-----------------|
| `_i` | `func() <scalar>` | any scalar getter |
| `_pp` | `func() <iface>` | single-interface getter |
| `_pppp` | `func() (iface, iface)` | `func() (X, error)` |
| `_iip` | `func() <word-leaf struct>` | `func() time.Time` |
| `pi_pppp` | `func(string) (iface, iface)` | `func(string) (X, error)` |
| `pi_piipp` | `func(string) (slice, iface)` | `func(string) ([]Y, error)` |
| `i_piipp` | `func(int) (slice, iface)` | `func(int) ([]Y, error)` |
| `pi_i` | `func(<2-word value>) <scalar>` | go-cmp `Equal(StructA) bool` |
| `p_i` | `func(<ptr word>) <scalar>` | `Equal(*T) bool`, `Equal(chan T) bool` |
| `pp_i` | `func(<iface>) <scalar>` | `Equal(InterfaceA) bool` |
| `i_i` | `func(<1-word value>) <scalar>` | `Equal(struct{A int}) bool` |

### Classification

`classifyType` (`vm/synth_word.go`) maps a `reflect.Type` to its register words:
a scalar is `i`, a pointer/chan/map/func is `p`, a string is `pi`, a slice is
`pii`, an interface is `pp`, and a struct is its leaves flattened.
`classifyStruct` flattens a struct only when every field starts on a word
boundary and occupies a whole number of register words, so the flattened word
sequence equals the memory layout (`time.Time{wall uint64; ext int64; loc
*Location}` -> `iip`).
Floats, complex, arrays, and sub-word-packed or padded structs return `!ok`, so
`detectWordShape` drops them -- the method simply does not attach, identical to
the pre-existing behavior, never mis-marshaled.
`detectWordShape` joins the param words, an underscore, and the result words into
the key (`pi_pppp`) and confirms a generated pool exists, so an attach never
errors on an unsupported shape.

### The generic dispatcher and marshaling

The generated `pool_w*.go` (from `gen_pools.go`'s `emitWord`) carries, per
word-shape, a pool of stubs `stubW..._k(recv, w0, ...) { dispatchW...(k, recv,
...) }` and one dispatcher.
`dispatchW...` scatters its native register words into `pw` (pointer words, a
typed `[]unsafe.Pointer` so the GC scans them) and `sw` (integer words, raw
`uint64`), invokes the per-slot `stubs.CoreFunc`, then gathers the result words
back from `rpw`/`rsw`.

The vm supplies the `CoreFunc` via `Machine.makeWordCore`: `marshalArgs`
reconstructs each argument in a fresh `reflect.New(t)` allocation (typed, so its
pointer words stay GC-visible) and `writeWords` pours the register words in;
`callMethod` re-enters the interpreter; `marshalResults`/`readWords` pour the
return back into `rpw`/`rsw`.
A failed dispatch panics (`raiseMethodErr`), so a panicking interpreted method
propagates through the native caller as in Go.

### Routing

`vm/synth_bridge.go`'s `allSynthMethods` and `promotedSynthMethods` resolve each
method via `synthMethodSpec.resolveDispatch`: `detectShape` first, falling back
to `detectWordShape`; a method matching neither is dropped, exactly as before
the fallback existed.
The word path consumes the same erased signature the method table publishes
(`eraseSynthIfaceParams`), so a synth non-empty interface param marshals as the
eface words native callers actually pack.
`stubs.Method` gains `WordKey`/`Core`, and `acquireSlot` routes a non-empty
`WordKey` to `acquireWordSlot` (the word-shape pool) instead of the typed
handler pools.
The reserve gates (`vm/derive.go`) count word-shaped methods via the silent
`wordShapeAvailable` probe, so a type whose only methods are word-shaped still
gets a method-bearing reservation.

### Why keep the typed shapes

The typed shapes `S1`..`S38` are preferred, not replaced, for three reasons.
A typed handler calls the method with no per-call reflect marshaling, so it is
faster on hot interfaces (`Stringer`, `error`, `io.Reader`).
The typed handlers also carry hand-tuned error policy (e.g. `reflectToError` for
the Marshal/Unmarshal shapes, the `Is`/`As`/`IsBoolFlag` shapes) that the generic
path does not reproduce.
And the typed shapes are architecture-independent, where the word path is gated
off (below).

### Architecture gate

`wordShapesSupported` restricts the whole path to a 64-bit little-endian target.
The classifier treats each scalar or pointer as one 8-byte register word (wrong
for a multi-register `int64` on a 32-bit target) and the integer-word packing is
little-endian.
The size check is a compile-time constant and the endianness check a one-time
init probe, so the path is enabled on amd64/arm64/riscv64/ppc64le/wasm and
disabled on 32-bit or big-endian targets; when disabled, `detectWordShape` drops
every method and only the arch-independent typed shapes attach -- correct, just
less capable.
The register budget caps the words on each side below the arch's argument
registers so the receiver plus arguments never spill to the stack, where the
stub's ABI would diverge; an over-budget signature drops.
CI runs the suite on both `ubuntu-latest` (amd64) and `ubuntu-24.04-arm`
(arm64) to verify the register marshaling on each supported architecture.

### ABI0 (wasm) variant

Go's wasm target is 64-bit little-endian (it passes the gate) but uses ABI0: all
parameters and results live in contiguous Go-stack memory, not registers.
A stub matches a real method there when its parameter/result *bytes* reproduce
that layout, so `wordABI0` (a build-tagged const) swaps `classifyWordSig` and
`makeWordCore` to a stack-slot decomposition (`synth_word_abi0.go`): each side is
chunked into 8-byte slots, and -- unlike the register path's one-word-per-leaf --
sub-word struct fields *pack* into a shared slot (`fixed.Point26_6` is one slot,
`color.Color.RGBA`'s four `uint32` results are two).
A slot is `p` iff it is exactly a pointer at an 8-aligned offset, `f` iff exactly
a `float64`, else `i`; ABI0 pads each side to a pointer-word boundary, so a
sub-word tail (a lone `bool` result) sits in a full slot whose high bytes are
frame padding.
All-8-byte signatures (the vast majority: pointers, `int`/`int64`, `string`,
slice, interface, `float64`) decompose identically to the register path, so they
share the same generated pools and keys; only the packed-aggregate keys differ
and add a couple of pool entries (`iii_`, `_ii`).
The register and ABI0 classifiers are both compiled on every arch (the dead one
is eliminated via the `wordABI0` const) so the stack decomposition is
unit-testable on a register host, including a memory-image equivalence check.
The stub-PC-into-method-table install path is shared with the register targets
(it mirrors `reflect`'s own synthesis) but is not yet exercised on a wasm runtime.

## Consequences

**Easier:**
- The stub catalog stops growing per signature; an ABI-compatible signature
  rides an existing word-shape with no new shape code.
- Methods whose signatures mention interpreted types -- inexpressible as typed
  stubs -- now attach: go-cmp's `Equal(T) bool` method family dispatches and
  `cmp.Equal` honors it.
- The word-shape catalog is grown by telemetry, not guesswork: `MVM_WORDDROPS=1`
  reports, at process exit, every dropped signature as either a "missing pools"
  key to add to `wordShapes` or an "unsupported" reason (floats / over budget).

**Harder / limitations:**
- Floats, complex, arrays, sub-word-packed structs, and signatures over six words
  per side still drop (no attach); float (`f`-class) words are a deferred
  extension.
- The path is gated to 64-bit little-endian (amd64/arm64/.../wasm); on 32-bit or
  big-endian targets only the typed shapes work.
- The word pools are finite and consumed monotonically like the typed pools.
- Per-call cost is higher than a typed shape (reflect marshaling plus a
  `reflect.New` per argument), so a hot interface still warrants a typed shape.

**Retained:**
- The typed shapes `S1`..`S38` are unchanged and tried first.
- ADR-021's `runtype`/`stubs` split and the C1--C5 synth-rtype invariants (see
  [synth-types](../modules/synth-types.md)) still hold; the word path adds a
  second slot source inside `stubs`, not a new rtype mechanism.

## Files

- `vm/synth_word.go` -- the arch gate, `detectWordShape`/`wordShapeAvailable`,
  and the `classifyWordSig`/`makeWordCore` per-arch selectors.
- `vm/synth_word_regabi.go` -- the register-ABI classifier (`classifyType`,
  one word per leaf) and marshaling.
- `vm/synth_word_abi0.go` -- the wasm/ABI0 stack-slot classifier and marshaling.
- `vm/synth_word_arch_{regabi,wasm}.go` -- the `wordABI0` build-tagged const.
- `vm/synth_word_drops.go` -- the `MVM_WORDDROPS` drop collector and report.
- `stdlib/stubs/word.go` -- `CoreFunc`, the `wordPool` registry, and
  `acquireWordSlot`/`HasWordShape`/`registerWordPool`.
- `stdlib/stubs/gen_pools.go` -- the `wordShapes` catalog and the `emitWord`
  generator.
- `stdlib/stubs/pool_w*.go` -- generated per-shape stub pools and dispatchers.
- `stdlib/stubs/registry.go` -- `Method.WordKey`/`Core`; `acquireSlot` routes
  `WordKey`.
- `vm/synth_bridge.go` -- `resolveDispatch` (typed first, word fallback);
  `toSynthMethods` builds the word-path `Method`.
- `vm/derive.go` -- reserve gates accept word-shaped methods.
- `.github/workflows/go.yml` -- amd64 + arm64 build matrix.
