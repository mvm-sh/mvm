# ADR-023: Index-based method dispatch

**Status:** proposed
**Date:** 2026-06-27

Concerns `IfaceCall`: interpreted code calling a method on an interpreted or
native receiver.
This is the opposite direction from [ADR-021](ADR-021-synthesized-rtypes.md) and
[ADR-022](ADR-022-word-class-dispatch.md) (native code dispatching an interpreted
method), which are unchanged here.

## Context

A method call compiles to one `IfaceCall` carrying a `methodID`: a global, dense,
compile-time index per method *name* (`comp/compiler.go`), so the same id
addresses that name on every type and interface.

At runtime `IfaceCall` branches on the receiver:

- Interpreted (mvm `Iface`): `ifc.Typ.ResolveMethodType(methodID)` then
  `Methods[methodID]` -- O(1), but `ResolveMethodType` walks the embedding chain
  per call when the direct slot is empty.
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
Interpreted types resolve their embedding chain once, retiring the per-call
`ResolveMethodType` walk.

### Compile-time vs runtime

Static concrete receiver: the compiler resolves the descriptor and emits a
direct-call opcode -- no runtime lookup.
Dynamic / interface receiver: emit the indexed `IfaceCall`.

### Awkward cases

Each must map to a descriptor variant or pre-table interception:

- the runtime/reflect shims (`reflectValueShim`, `reflectTypeShim`,
  `runtimeFuncShim`),
- named-basic erasure (`time.Duration` stored as bare `int64`),
- method values (`t.Add`) and expressions (`T.Add`),
- `defer`/`go` receiver capture.

## Consequences

- Dispatch is O(1) index for both receiver kinds, with no per-call reflect
  resolution, binding allocation, or embedding walk; the native/interpreted
  branch in `IfaceCall` collapses to one mechanism.
- Per-type tables are sparse under the global index (memory until compacted).
- Construction must flatten embedding correctly; migration touches the hot path
  and the awkward cases, so it lands behind the `BenchmarkNativeBridgeTimeAdd`
  baseline and the `interptest` suite.

## Alternatives considered

- **Per-type-dense indices + itabs (Go-exact).** Rejected: reintroduces itabs the
  global index avoids, for no dispatch-speed gain in an interpreter.
- **Inline cache over the name-based path (prototype).** Rejected as the end
  state: caches a lookup the design should not perform. Kept as the baseline.
- **Status quo.** Rejected: the measured bottleneck, worst on wasm.

## Phased plan

Each phase lands and is measured independently.

1. **Native method tables (landed).** Per-type table indexed by `methodID`,
   resolved lazily to an unbound-func descriptor; the `IfaceCall` native branch
   indexes it instead of `MethodByName`/`makeMethodValue`. No compiler change,
   default-on (`MVM_NATIVE_TABLE=off` kill switch), supersedes the prototype.
   Measured (`time.Add` x100k): native -34% time / 13 -> 7 allocs; wasm -33%,
   byte-identical. `vm/native_methods.go`.
2. **Flatten embedding** into tables at construction; drop the per-call
   `ResolveMethodType` walk.
3. **Compile-time direct dispatch** for static concrete receivers: a direct-call
   opcode bypassing `IfaceCall`.
4. **(later) Thunks + opcode unification.** Generated thunks remove `reflect.Call`;
   one opcode serves both receiver kinds.

## Files

- `vm/native_methods.go` -- the table, descriptor, and resolution (phase 1).
- `vm/vm.go` -- the `IfaceCall` native branch and the `cachedNativeCall` marker.
- `comp/compiler.go` -- direct-call emission for static receivers (phase 3).
- `vm/synth_bridge.go` -- shims rehomed as descriptor variants.
