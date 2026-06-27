# ADR-023: Index-based method dispatch

**Status:** accepted -- phase 1 (native tables) and phase 2' (fused method frame)
landed; the embedding-flatten and direct-dispatch phases were already realized in
the compiler; phase 4 deferred
**Date:** 2026-06-27

Concerns `IfaceCall`: interpreted code calling a method on an interpreted or
native receiver.
This is the opposite direction from [ADR-021](ADR-021-synthesized-rtypes.md) and
[ADR-022](ADR-022-word-class-dispatch.md) (native code dispatching an interpreted
method), which are unchanged here.

The native receiver was the cost and phase 1 fixed it.
Inspecting the compiler then showed the interpreted-receiver phases (2-3) were
already in place, so only phase 1 was implemented; see the phased plan.

## Context

A method call compiles to one `IfaceCall` carrying a `methodID`: a global, dense,
compile-time index per method *name* (`comp/compiler.go`), so the same id
addresses that name on every type and interface.

At runtime `IfaceCall` branches on the receiver:

- Interpreted (mvm `Iface`): `ifc.Typ.ResolveMethodType(methodID)` then
  `Methods[methodID]` -- O(1) index; the resolve walks the `Base`/pointer chain
  per call only when the direct slot is empty (embedding is already flattened in,
  see below).
- Native: no table. `nativeMethodLookup` calls `reflect.Value.MethodByName`,
  binding a fresh method value (`makeMethodValue` + `FuncOf`) every call.

The native path is the cost.
A `time.Add` loop spends ~40% of a crossing in method resolution, ~22% in the
allocations it drives, ~17% in the actual `reflect.Call` -- ~13 allocs/call,
~0.7 us native but ~20 us on wasm (slow allocator), dominating method-heavy code.
It is also redundant: the compiler already calls `MethodByName` at the call site
to validate the method, then discards it and emits only the name index.

A per-call-site inline cache (prototype) cut allocs 13 -> 7 and wasm ~20 -> 14 us,
but memoizes a name lookup that should not exist at runtime rather than removing
it.

## Decision

Give every type a method table indexed by the global `methodID`, holding a
uniform *descriptor*, and resolve as much as possible at compile time.
Dispatch becomes `type.methods[methodID] -> descriptor`: no name, no
`MethodByName`, no per-call embedding walk.

### Keep the global index; no itabs

Go's per-type-dense indices force an itab (per interface x concrete) to remap an
interface-method index onto the concrete method.
mvm's global name-index addresses a method uniformly across all types, so concrete
and interface calls share it with no itab.
Worth keeping.
The cost is sparse per-type tables; that is memory, not speed, compactable later
without changing the dispatch contract.

### Method descriptor

Two shapes: an interpreted method is a bytecode address (jump, as today); a native
method is a callable.
For native, resolve `name -> reflect index` once per type and store the *unbound*
func (`Method(i).Func`); the call prepends the receiver.
This removes `MethodByName`/`makeMethodValue`; the leaf `reflect.Call` remains
(a later phase replaces it with a thunk).

### Flatten embedding at construction

Populate each table with the full accessible method set, promotion included, when
the type is built -- not per call.
Native types are free: `reflect`'s method set is already that flattened closure.
Interpreted types should carry their promoted methods directly.

Already realized: the compiler's promotion pass (`comp/compiler.go`, the type
`visit`) recursively copies each embedded method into the outer type's
`Methods[id]` with the receiver `Path` pre-adjusted.
The per-call `ResolveMethodType` no longer walks embedding; it only does the
`*T`<->`T` value/pointer method-set bridging and the `Base`-clone fallback
(~2%, alloc-free).
Dispatch through an embedded *interface* field stays runtime-dynamic by nature
(`EmbedIface`); flattening cannot remove it.

### Compile-time vs runtime

Static concrete receiver: the compiler resolves the method and emits a direct
`Call` -- no runtime lookup.
Dynamic / interface receiver: emit the indexed `IfaceCall`.

Already realized: a concrete interpreted method call compiles to a direct `Call`
on the method's code address (`comp/compiler.go`); `IfaceCall` is emitted only for
interface-typed receivers (dynamic) and native concrete types (phase 1's domain).

### Awkward cases

Each must map to a descriptor variant or pre-table interception:

- the runtime/reflect shims (`reflectValueShim`, `reflectTypeShim`,
  `runtimeFuncShim`),
- named-basic erasure (`time.Duration` stored as bare `int64`),
- method values (`t.Add`) and expressions (`T.Add`),
- `defer`/`go` receiver capture.

## Consequences

- Native dispatch is now O(1) index with no per-call `MethodByName`/binding
  allocation (phase 1); the leaf `reflect.Call` remains.
- The interpreted path was already O(1) index with embedding flattened at compile
  time; its real cost was allocation, not resolution (profiling showed 4
  allocs/dispatch, all building the receiver closure, vs ~2% in
  `ResolveMethodType`). The fused method frame cut that to 1-2 allocs/dispatch
  (phase 2').
- Per-type native tables are sparse under the global index (memory until
  compacted).
- The wins landed behind the `BenchmarkNativeBridgeTimeAdd` /
  `BenchmarkIfaceDispatch*` baselines and the `interptest` suite.

## Alternatives considered

- **Per-type-dense indices + itabs (Go-exact).** Rejected: reintroduces itabs the
  global index avoids, for no dispatch-speed gain in an interpreter.
- **Inline cache over the name-based path (prototype).** Rejected as the end
  state: caches a lookup the design should not perform. Kept as the baseline.
- **Inline the interface in `Value`** (a `typ *Type` field to drop the
  `reflect.ValueOf(Iface{...})` box alloc). Rejected, measured: widening `Value`
  32 -> 40 bytes (field unused) costs +12% geomean time (+17.6% on `Fib`, which
  has no interfaces) -- a universal tax for a saving only interface-creation-heavy
  code sees. Carrying `Iface` as a pointer does not help (`&Iface{...}` still
  allocates).
- **Status quo.** Rejected: the measured bottleneck, worst on wasm.

## Phased plan

1. **Native method tables (landed).** Per-type table indexed by `methodID`,
   resolved lazily to an unbound-func descriptor; the `IfaceCall` native branch
   indexes it instead of `MethodByName`/`makeMethodValue`. No compiler change,
   default-on (`MVM_NATIVE_TABLE=off` kill switch), supersedes the prototype.
   Measured (`time.Add` x100k): native -34% time / 13 -> 7 allocs; wasm -33%,
   byte-identical. `vm/native_methods.go`.
2. **Flatten embedding (already realized).** The compiler's promotion pass
   pre-fills promoted methods into `Methods[id]`. The only residue is the per-call
   `ResolveMethodType` (`*T`<->`T` bridging + `Base`-clone fallback, ~2%,
   alloc-free); not worth the invasive change to remove. Skipped.
2'. **Fused method frame (landed).** Profiling the interpreted path found its cost
   was allocation, not resolution: every dispatch built a receiver `Closure`
   (cell + 1-entry heap slice + reflect box = 3-4 allocs). The fused frame packs
   the cell and its single-entry heap in one allocation carried to `Call` as a
   pointer, not a boxed `Closure`. Default-on (`MVM_FUSED_FRAME=off` kill switch).
   Measured: native -20% time / 4 -> 2 allocs (1 for non-struct/pointer
   receivers, the residue being the value-receiver struct copy); wasm -18%.
   `vm/method_frame.go`.
3. **Compile-time direct dispatch (already realized).** Concrete interpreted
   method calls already compile to a direct `Call`; `IfaceCall` is reserved for
   dynamic (interface) and native receivers. Skipped.
4. **(deferred) Thunks + opcode unification.** Phase 1 left the leaf
   `reflect.Call` (~17% of a native crossing). Removing it needs per-signature
   generated code -- on wasm, a precompiled stub-pool mechanism like
   [ADR-022](ADR-022-word-class-dispatch.md) in reverse. A separate project, not a
   quick follow-on. Opcode unification is cosmetic cleanup.

## Files

- `vm/native_methods.go` -- the table, descriptor, and resolution (phase 1).
- `vm/method_frame.go` -- the fused receiver frame (phase 2').
- `vm/vm.go` -- the `IfaceCall` native and interpreted branches, the
  `cachedNativeCall` and `methodFrame` markers consumed by `Call`.
