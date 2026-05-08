# ADR-016: Runtime introspection via `*runtime.Func` sentinels

**Status:** accepted (partial -- 18/19 pkg/errors tests pass)
**Date:** 2026-05-08

## Context

Several Go libraries -- pkg/errors, runtime/debug, error-wrapping
helpers in user code -- format their output by capturing a stack via
`runtime.Callers` and later resolving each PC with `runtime.FuncForPC`
+ `(*runtime.Func).Name()` / `FileLine()`. Until this change those
calls were straight reflect passthroughs in `stdlib/ext/runtime.go`,
so when interpreted code captured a stack the PCs landed inside
`vm.(*Machine).Run`, `vm.CallFunc`, etc. -- the host frames driving
the interpreter, not the interpreted call chain. `pkg/errors` `%+v`
formatting under `mvm test github.com/pkg/errors` looked like:

```
error
github.com/mvm-sh/mvm/vm.(*Machine).Run
    /home/.../vm/vm.go:578
...
```

instead of the expected interpreted frames.

The straightforward fix -- patch pkg/errors source -- doesn't scale:
any user code using `runtime.Callers` for diagnostics has the same
problem, and we don't want to fork third-party libraries.

mvm has the data needed (`m.fp`, `m.mem`, `DebugInfo.Sources`,
`DebugInfo.Labels`); the question was how to surface it through
host-typed APIs without breaking their contracts.

## Decision

Virtualize `runtime.Callers` and `runtime.FuncForPC` at the
`stdlib.RegisterPackagePatcher("runtime", ...)` boundary. Three
primitives in `vm/runtime_intercept.go` make it possible without
touching the interpreter's hot path.

### 1. Active-machine slot

```go
var activeMachine atomic.Pointer[Machine]
```

`SetActiveMachine(m)` does an atomic `Swap` and returns the previous
value. `Run` calls it on entry and restores via `defer`. Bridges
running on the same goroutine retrieve the live `Machine` via
`ActiveMachine()`.

Single-machine-at-a-time semantics: the slot is global, not
goroutine-local. Concurrent goroutines running independent
`Machine`s race here. The previous mutex+stack alternative had the
same race (its `idx` could point into the wrong slot under
interleaving) but at higher cost. True per-goroutine GLS would
require unsafe-G inspection or `runtime/pprof` labels and is not
required by current consumers (mvm's test runner serializes test
goroutines through `testing.Main`).

### 2. Synthetic PC sentinels

`*runtime.Func` is a zero-sized struct, so `&runtime.Func{}` returns
the same canonical pointer every time. `runtimeFuncSentinel` adds
one byte of padding so each `NewRuntimeFuncSentinel()` call gets a
unique address. The bridge stores
`(uintptr(unsafe.Pointer(rf)) + 1)` into the user's `pcs[]`; the
`+1` matches pkg/errors's `Frame.pc()` convention (`uintptr(f) - 1`)
and survives round-tripping.

`RegisterRuntimeFunc(rf, name, file, line)` puts the metadata into a
package-level `sync.Map` keyed by `*runtime.Func`. `LookupRuntimeFunc`
returns nil for non-mvm pointers, so a real host PC passed to the
bridge falls through to host `runtime.FuncForPC` cleanly.

### 3. Method intercept

The host `(*runtime.Func).Name` / `FileLine` methods can't be
overridden, but mvm dispatches them in interpreted code through
`nativeMethodLookup` -- the same lookup that returns bound-method
`reflect.Value`s for any native receiver. Adding a single line at
the top of that function:

```go
if shim := runtimeFuncShim(rv, name); shim.IsValid() { return shim }
```

is enough. The shim checks the receiver's type pointer against a
cached `*runtime.Func` `reflect.Type`, looks the receiver up in the
side table, and returns a `reflect.MakeFunc` closure with the
matching signature. Miss path is one type-pointer compare.

### Live-state sync before native calls

The interpreter's hot loop holds `mem`, `fp`, `ip` in stack-allocated
locals; `m.fp` and `m.ip` are otherwise stale until `Run` returns.
For bridges that introspect the live VM (`WalkCallStack`, the
runtime bridge) we sync just before each native `rv.Call(in)`:

```go
m.mem, m.fp, m.ip = mem, fp, ip+1
```

One-way push: the local copies remain authoritative. Cost is three
pointer-sized stores per native function call from interpreted code.

### Bridge wiring

`stdlib/runtime_virt.go` is pure composition over the primitives:

- `mvmCallers(skip, pcs)` -- resolves `ActiveMachine`, walks via
  `WalkCallStack`, drops the top `skip-1` frames (mvm has no
  `runtime.Callers` vm-frame to count), allocates a sentinel per
  retained frame, registers metadata, fills `pcs`.
- `mvmFuncForPC(pc)` -- reverses the encoding (try `pc-1`, then
  `pc`); falls through to host `runtime.FuncForPC` on miss.
- `qualifyFuncName(label, file)` -- cosmetic: turns
  `"TestFormatNew"` into `"github.com/pkg/errors.TestFormatNew"`
  using the source file's directory.

## Consequences

**Easier:**

- pkg/errors-style libraries work unmodified. 18/19 pkg/errors tests
  pass under `mvm test github.com/pkg/errors` after this change,
  including all four core format tests (`TestFormatNew`, `Errorf`,
  `Wrap`, `Wrapf`).
- The primitives (`WalkCallStack`, `DebugInfo.FuncAt`,
  `SetActiveMachine`) are reusable. The planned sampling profiler
  is expected to use the same iterator and symbolization layer; the
  interactive debugger's `bt` command can migrate onto
  `WalkCallStack` once `StackFrame` carries enough state for
  `DumpFrame`'s slot annotations.
- Adding new host-runtime bridges (e.g. `runtime.Stack`,
  `debug.Stack`) is straightforward composition over the primitives.

**Harder / weaker:**

- `runtimeFuncMeta` is a `sync.Map` that grows unbounded. Sentinels
  are allocated per captured frame; long-running programs that
  capture many stacks will leak. A sweep is the obvious next step
  but not yet implemented.
- Single-machine-at-a-time semantics on `ActiveMachine`. Acceptable
  for current uses; needs revisiting before the planned profiler
  runs concurrent with interpreted goroutines.
- The pre-call `m.mem/fp/ip` sync is a tiny cost on every native
  call, paid even by code that never calls `runtime.Callers`. ~3
  pointer stores; below noise on Fib/Append benchmarks.
- One pkg/errors test (`TestFormatWithStack`) still fails. The
  panic is unrelated -- a `bool not assignable to map[string]bool`
  inside the test's helper that this change exposed by letting the
  test progress further. Tracked as a follow-up; does not affect
  the bridge design.

## Alternatives considered

- **Patch pkg/errors source via a `stdlib.RegisterPackagePatcher`
  for `github.com/pkg/errors`.** Localized but doesn't scale to
  arbitrary user code or other libraries that use `runtime.Callers`
  for diagnostics. Rejected.

- **Return `*runtime.Func` whose `Entry()` PC matches a real host
  function.** Would let host `(*runtime.Func).Name` / `FileLine`
  work without a method intercept, but we'd need to allocate
  identity-mapped host functions per interpreted PC -- not feasible
  at runtime.

- **Make the interpreter sync `m.fp`/`m.ip` on every opcode.** Would
  remove the need for the pre-`rv.Call` push but adds work to the
  hot dispatch loop even when nothing is introspecting. Rejected:
  the current "sync at the boundary" pattern is the same trade-off
  the project_profiler design uses.

- **Mutex+stack instead of `atomic.Pointer` for `activeMachine`.**
  Tried first; cost +1 alloc and ~2% on `BenchmarkFib` from the
  closure returned by `PushActiveMachine`. Replaced with the
  atomic approach which has zero hot-path allocations.
