# ADR-022: Word-class ABI register shapes for method dispatch

**Status:** accepted
**Date:** 2026-06-02

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

That enumeration grows linearly against the standard library's interface
surface.
`io/fs` alone wants `FS.Open`, `ReadDirFS.ReadDir`, `GlobFS.Glob`,
`ReadFileFS.ReadFile`, `ReadDirFile.ReadDir`, `FileInfo.Size`/`Mode`/`ModTime`,
and `DirEntry.Type`/`Info` -- a cluster of distinct signatures the typed catalog
would have to spell out one by one, and the same story repeats for every new
package whose interfaces an interpreted type must satisfy.

The observation that unblocks it: many of those distinct *Go* signatures share
one *ABI*.
`func(string) (fs.File, error)` and `func(string) (io.Reader, error)` both pass a
string as a (pointer, length) register pair and return two interface words plus
an error interface; at the register level they are the same function.
One stub family can serve every signature that classifies to the same sequence
of registers.

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
| `_i` | `func() <scalar>` | `fs.FileInfo.Size`/`Mode`, `fs.DirEntry.Type` |
| `_pp` | `func() <iface>` | single-interface getter |
| `_pppp` | `func() (iface, iface)` | `fs.DirEntry.Info`, `fs.File.Stat` (X, error) |
| `_iip` | `func() <word-leaf struct>` | `fs.FileInfo.ModTime` (`time.Time`) |
| `pi_pppp` | `func(string) (iface, iface)` | `fs.FS.Open` (File, error) |
| `pi_piipp` | `func(string) (slice, iface)` | `ReadDirFS.ReadDir`, `GlobFS.Glob`, `ReadFileFS.ReadFile` |
| `i_piipp` | `func(int) (slice, iface)` | `fs.ReadDirFile.ReadDir` |

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

`vm/synth_bridge.go`'s `allSynthMethods` tries `detectShape` first and falls back
to `detectWordShape`; a method matching neither is dropped, exactly as before the
fallback existed.
`stubs.Method` gains `WordKey`/`Core`, and `acquireSlot` routes a non-empty
`WordKey` to `acquireWordSlot` (the word-shape pool) instead of the typed
handler pools.

### Why keep the typed shapes

The typed shapes `S1`..`S21` are preferred, not replaced, for three reasons.
A typed handler calls the method with no per-call reflect marshaling, so it is
faster on hot interfaces (`Stringer`, `error`, `io.Reader`).
The typed handlers also carry hand-tuned error policy (e.g. `reflectToError` for
the Marshal/Unmarshal shapes, the `Is`/`As`/`IsBoolFlag` shapes) that the generic
path does not reproduce.
And the typed shapes are architecture-independent, where the word path is gated
off (below).
The former typed `time.Time` shape was retired into the generic `_iip` word path,
since it carried no special error semantics.

### Architecture gate

`wordShapesSupported` restricts the whole path to a 64-bit little-endian target.
The classifier treats each scalar or pointer as one 8-byte register word (wrong
for a multi-register `int64` on a 32-bit target) and the integer-word packing is
little-endian.
The size check is a compile-time constant and the endianness check a one-time
init probe, so the path is enabled on amd64/arm64/riscv64/ppc64le and disabled
elsewhere; when disabled, `detectWordShape` drops every method and only the
arch-independent typed shapes attach -- correct, just less capable.
`maxWordIO` (6) caps the words on each side, conservatively below the smaller
(amd64) nine-integer-register argument budget so the receiver plus arguments
never spill to the stack, where the stub's ABI would diverge; an over-budget
signature drops.
CI now runs the suite on both `ubuntu-latest` (amd64) and `ubuntu-24.04-arm`
(arm64) to verify the register marshaling on each supported architecture.

## Consequences

**Easier:**
- The stub catalog stops growing per signature; one word-shape serves a whole
  signature family, so adding `io/fs`-style interfaces needs no new shape code.
- `io/fs`'s `FS`/`File`/`DirEntry`/`ReadDirFS`/`GlobFS`/`ReadFileFS` interfaces
  now attach and dispatch, and `io/fs` passes under `mvm test` (two `struct{FS}`
  `Incompat` skips remain -- a pre-existing `reflect.StructOf` limit, independent
  of this change).

**Harder / limitations:**
- Floats, complex, arrays, sub-word-packed structs, and signatures over six words
  per side still drop (no attach); float (`f`-class) words are a deferred
  extension.
- The path is gated to 64-bit little-endian; on other targets only the typed
  shapes work.
- The word pools are finite and consumed monotonically like the typed pools.
- Per-call cost is higher than a typed shape (reflect marshaling plus a
  `reflect.New` per argument), so a hot interface still warrants a typed shape.

**Retained:**
- The typed shapes `S1`..`S21` are unchanged and tried first.
- ADR-021's `runtype`/`stubs` split and the C1--C5 synth-rtype invariants (see
  [synth-types](../modules/synth-types.md)) still hold; the word path adds a
  second slot source inside `stubs`, not a new rtype mechanism.

## Files

- `vm/synth_word.go` -- `classifyType`/`classifyStruct`, the arch gate,
  `detectWordShape`, and `makeWordCore` plus the word<->value marshaling.
- `stdlib/stubs/word.go` -- `CoreFunc`, the `wordPool` registry, and
  `acquireWordSlot`/`HasWordShape`/`registerWordPool`.
- `stdlib/stubs/gen_pools.go` -- the `wordShapes` catalog and the `emitWord`
  generator.
- `stdlib/stubs/pool_w*.go` -- generated per-shape stub pools and dispatchers.
- `stdlib/stubs/registry.go` -- `Method.WordKey`/`Core`; `acquireSlot` routes
  `WordKey`.
- `vm/synth_bridge.go` -- `allSynthMethods` prefers `detectShape`, falls back to
  `detectWordShape`; `toSynthMethods` builds the word-path `Method`.
- `.github/workflows/go.yml` -- amd64 + arm64 build matrix.
- `stdlib/incompat.go` -- two pre-existing `io/fs` `struct{FS}` skips.
