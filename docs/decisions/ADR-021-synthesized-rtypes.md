# ADR-021: Synthesized rtypes for native method dispatch

**Status:** accepted (supersedes ADR-009; supersedes the argument-proxy half of ADR-012)
**Date:** 2026-05-28

## Context

Interpreted types are built at runtime with `reflect.StructOf` (and the
named-primitive/slice/map equivalents).
Go's reflect package cannot attach methods to such types: the runtime's `itab`
dispatch reads the method set from the type descriptor, and there is no public
API to add methods after type creation.

So when an interpreted value with `String() string` reached `fmt.Println`, Go's
interface dispatcher could not find `fmt.Stringer`.
[ADR-009](ADR-009-interface-bridging.md) worked around this with per-call
*interface bridges* (wrapper structs whose method re-enters the interpreter) and
[ADR-012](ADR-012-package-patchers-arg-proxies.md) extended it with *argument
proxies* + mvm-native *shadow packages* (`stdlib/jsonx`, `xmlx`, `gobx`) for
stdlib code that walks struct fields via reflection.

That approach had three costs:
- A per-call bridge tax (allocation + `reflect.MakeFunc` + a fresh re-entrant
  `Machine`) on every native boundary crossing.
- Only one method could be bridged per value, so a type satisfying both
  `Stringer` and `json.Marshaler` lost one of them at any given call.
- Each reflect-walking stdlib package needed a hand-written shadow
  (~1800 lines across jsonx/xmlx/gobx/errorsx), and nested interpreted methods
  on struct fields only dispatched inside a shadow that knew to look.

ADR-012's "Alternatives considered" explicitly weighed and *rejected* attaching
methods to dynamically-generated rtypes ("would be a large patch on the reflect
rtype internals").
This ADR is the realization of that rejected alternative -- the patch turned out
to be tractable and is isolated in one package.

## Decision

Synthesize a real Go rtype that carries the interpreted methods, so native
`itab`/reflect dispatch finds them directly with no per-call wrapper.

The machinery is split across two packages for a one-directional dependency:

- **`runtype`** (top-level) -- the runtime mechanism.
  Byte-for-byte mirrors of `internal/abi` types, `addReflectOff` via
  `//go:linkname`, and `Attach{Struct,Primitive,Slice,Array,Map,Ptr}Methods`
  that clone a layout rtype and overlay an `UncommonType` + method array whose
  `Ifn`/`Tfn` point at pre-built dispatch stubs.
  `runtype` knows nothing about method shapes: `Attach*` take a `MethodSpec`
  carrying an already-resolved stub PC.
- **`stdlib/stubs`** -- the shape catalog.
  a catalog of method-signature *shapes* (S1 `func() string` onward; see
  `stdlib/stubs/gen_pools.go` for the authoritative list),
  each with a generated pool of dispatch-stub functions (`pool_s*.go`) and a
  hand-written handler registry/dispatcher (`registry_s*.go`).
  The `stubs.Attach*` wrappers resolve a method's shape to a free stub slot,
  then delegate rtype synthesis to `runtype`.

`stubs` imports `runtype`; `runtype` imports nothing internal.
A naive single package deadlocks (attach needs the shape pools; the pools need
`runtype.FuncPC`), so the attach API is inverted to take resolved PCs -- see
[runtype](../modules/runtype.md) and [stubs](../modules/stubs.md).

The interpreter calls `Machine.AttachSynthMethods` for every compiled type
(`interp/synth.go`, unconditional).
The vm-side glue stays in `vm/synth_bridge.go`: `detectShape` matches a method
signature to a shape, and `makeHandlerS*` builds the closure that re-enters the
interpreter via `CallFunc` when the stub fires.

## Consequences

**Easier:**
- One synthesized rtype carries *all* of a type's matched methods, so a value can
  satisfy `Stringer` and `json.Marshaler` simultaneously.
- Native reflect-walking packages (`encoding/json`, `encoding/xml`,
  `text/template`, ...) see interpreted methods on nested struct fields with no
  shadow package. ~1800 lines of `jsonx`/`xmlx`/`gobx`/`errorsx` plus the whole
  `RegisterArgProxy` API were deleted.
- Per-call dispatch is native speed with zero per-call allocation (was
  ~65 ns + 1 alloc through the bridge); end-to-end `json.Marshal`/`Unmarshal`
  matches native and roughly halves the bridge's time and allocations.
- Replaces `patchRtype` and its go1.26 GC bad-pointer flake.

**Harder / limitations:**
- `runtype` mirrors unexported `internal/abi` layouts and uses `//go:linkname`;
  `runtype/abi_test.go` probes a live rtype to catch layout drift across Go
  releases.
- Per-shape stub pools are finite and consumed monotonically (S1 = 2048 slots,
  others 256); a process that attaches more distinct methods of one shape than
  its pool holds errors out.
- Synthesized rtypes pollute the process `itab` cache; REPL redefinition leaks.
- `Type.Method(i).Func.Call` uses a type's natural ABI and does not work for
  non-direct kinds (interface/`Value.MethodByName().Call` dispatch, the common
  stdlib path, does work).

**Retained from the superseded ADRs:**
- `IfaceWrap` still boxes interface arguments with their mvm `*Type` identity at
  native call sites; `bridgeArgs` now only unwraps the `Iface` to its concrete
  (synthesized) value, or retypes a pointer-to-interpreted-interface to the
  synthesized interface rtype for `errors.As`.
- The `RegisterPackagePatcher` primitive from ADR-012 survives, now used only by
  the runtime-virtualization shadow (`stdlib/runtime_virt.go`, see
  [ADR-016](ADR-016-runtime-introspection-bridge.md)).
