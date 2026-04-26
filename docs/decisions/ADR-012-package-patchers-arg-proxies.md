# ADR-012: Package patchers and argument proxies for mvm-native stdlib shadows

**Status:** accepted
**Date:** 2026-04-18

## Context

The `vm.Bridges` mechanism (see ADR-009) lets native Go code call interpreted
methods on values passed through an interface boundary. It works well for
single-method interfaces discovered at a direct call site (`fmt.Println(x)`
finds `x.String()`).

It falls down in three related situations:

1. **Nested values.** `encoding/json.Marshal` walks struct fields via
   `reflect`. A field whose mvm type has `MarshalJSON` is wrapped by
   `reflect.StructOf` into a type with no methods. `bridgeArgs` only inspects
   the top-level argument, not arbitrary field paths reached during walking.

2. **Streaming and stateful APIs.** `json.NewEncoder(&buf).Encode(v)` holds
   state across calls; a bridge on `Encode`'s single argument cannot see that
   the encoder came from interpreted code and needs mvm-aware handling.

3. **Full-struct walking.** Custom (de)serializers iterate over `Type.Fields`
   and dispatch mvm methods via `Machine.CallFunc`. They are not a single
   method on a wrapper -- they are a whole package reimplementation that
   needs to be spliced in place of the stdlib one.

## Decision

Two cooperating mechanisms, both independent of `vm`, `comp`, and `goparser`:

### Package patchers (`stdlib.RegisterPackagePatcher`)

A patcher is `func(m *vm.Machine, values map[string]vm.Value)`. The
interpreter calls each registered patcher once, on the first `Eval`, after
imports have resolved. The patcher overlays entries in the package's exported
symbol map. Interpreted code importing `encoding/json` then resolves
`json.Encoder` to the replacement type.

A shadow package (`stdlib/jsonx`) registers its patcher in `init()`:

```go
stdlib.RegisterPackagePatcher("encoding/json", patchEncodingJSON)
```

No changes to the interpreter are required for each new shadow.

### Argument proxies (`vm.RegisterArgProxy`, `vm.RegisterArgProxyMethod`)

A `ProxyFactory` is `func(m *Machine, ifc Iface) reflect.Value`. It builds
a pointer-to-struct wrapper whose methods (`MarshalJSON`, `UnmarshalJSON`,
...) re-enter the interpreter via `Machine.CallFunc` with the full mvm
`Iface` metadata preserved.

Registrations are keyed by either:

- `(fnPtr, arg)` -- plain function, via `reflect.ValueOf(fn).Pointer()`.
- `(recvType, methodName, arg)` -- native method. Bound method pointers all
  share a single `methodValueCall` trampoline, so keying by pointer would
  collide across every method on every type -- `(type, name)` avoids this.

At the native call boundary, `bridgeArgs` checks both tables. If a factory
is registered for an argument slot, it replaces the mvm `Iface` with the
proxy. Native reflection then sees an ordinary `json.Marshaler` etc.

### Val-preserving bridges (`vm.ValBridgeTypes`)

Some bridges (display bridges) carry a `Val any` field holding the original
concrete value. `ValBridgeTypes` is the set of bridge pointer types that do
so. `unbridgeValue` inspects an interface argument during type assertions
and unwraps the `Val` field if present, so `v, ok := x.(MyNamedInt)` works
after `x` passed through a display bridge.

## Consequences

**Easier:**
- Nested mvm methods on struct fields now dispatch correctly for JSON
  (see `stdlib/jsonx`). The pattern transfers to any other reflect-walking
  stdlib package (`encoding/xml`, `encoding/gob`, `text/template`).
- Adding a shadow requires no VM or compiler changes -- one patcher, one
  or more arg-proxy registrations.
- Streaming APIs (`Encoder`/`Decoder`) are covered: registrations attach to
  method slots by receiver type and method name.

**Harder:**
- Each shadow duplicates part of the stdlib's behaviour. The duplication is
  targeted (only the reflect-walking path; pure-native values are delegated
  to the original stdlib).
- Registrations are global (package-level maps). Concurrent `Machine`s share
  them; the factories themselves take the machine as an argument so this is
  safe.
- The patcher fires only once per interpreter (guarded by
  `Interp.stdlibPatched`). Tests that build interpreters in parallel each
  get their own patch pass.

## Alternatives considered

- **Shadow-struct via `reflect.StructOf` rewriting.** Would attach methods
  to dynamically-generated types so nested fields "just work" for all
  reflect-based packages. Rejected: `reflect.StructOf` cannot attach
  methods, so the implementation would be a large patch on the reflect
  rtype internals. See the memory note
  `project_shadow_struct_plan.md` for the detailed weighing.

- **Per-method VM hook (`MvmAware`).** Tried first for the Encoder/
  Decoder case. Carried machine-level state and required VM changes for
  each new hookable method. Replaced same-day by arg proxies, which push
  the same machinery into a generic registration API.
