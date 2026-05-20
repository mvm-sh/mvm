# ADR-018: Virtualized process exit via panic-based `ExitError`

**Status:** accepted
**Date:** 2026-05-19

## Context

Interpreted code paths that terminate the process -- `os.Exit`, `log.Fatal*`,
native bridges like `testing.Main` -- previously routed straight to the host
runtime's `os.Exit`.
That had three concrete costs:

- **REPL.** `os.Exit(0)` typed in the REPL killed the REPL session instead of
  returning the prompt.
- **Embedders.** Untrusted interpreted code from a `Eval` could terminate the
  host process; there was no error signal an embedder could catch.
- **`mvm test -stat`.** The recently-added `-stat` summary had to print
  *before* the test summary, via a `setupStats` once-guard +
  `InstallStatsExitHook` os.Exit wrapper, because native `testing.Main` ends
  in `os.Exit(MainStart(...).Run())` and bypasses host defers.

Two earlier alternatives were considered and rejected.

- **`//go:linkname` to replace `syscall.Exit`.** The runtime already publishes
  `syscall.Exit` via linkname; adding a second is a duplicate-symbol link
  error.
  `os.Exit` itself cannot be replaced via linkname because it has a body.
- **Assembly-level monkey-patch.** Works on amd64/arm64 but fragile across Go
  versions and inlining.
  Not viable for a maintained codebase.

## Decision

Replace the `os.Exit` and `log.Fatal*` bindings with stubs that `panic` an
`*interp.ExitError`, and surface that panic cleanly through the VM's recover
path.

### `interp.ExitError`

```go
type ExitError struct{ Code int }
func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
```

`ExitError` is returned from `i.Eval` (and therefore from `i.Run`) whenever
interpreted code's exit path is taken.
Callers use `errors.As` (or a type assertion) to recover the exit code;
treating it as a generic error and printing it is fine for embedders that
don't care.

### Bindings (`interp/interpreter.go`)

`installExitVirtualization`, called from `patchStdlibOverrides` (which already
runs once on first `Eval` after `ImportPackageValues` has populated
`i.Packages`):

- `os.Exit(code)` -> `panic(&ExitError{Code: code})`.
- `log.Fatal(args...)` -> `log.Print(args...); panic(&ExitError{Code: 1})`.
  Same shape for `Fatalf` (`Printf`) and `Fatalln` (`Println`), preserving the
  configured logger's prefix/flags/Writer.

No-op for packages the embedder did not import (the `ok` guards keep
stripped-down bindings working).

### `vm.recoverPanic` shape check (`vm/vm.go`)

`recoverPanic` already returns `*PanicError` for in-VM panics and wraps
everything else via `capturePanic` (which adds source snippets and an mvm
stack).
`ExitError` should *not* be wrapped -- it's a clean signal, not a crash.
The recovery branch becomes:

```go
if e, ok := r.(error); ok {
    if _, isRuntimeErr := r.(runtime.Error); !isRuntimeErr {
        *err = e
        return
    }
}
*err = m.capturePanic(r)
```

The check is on shape, not concrete type: any `error` value that is not a
`runtime.Error` (i.e., not a nil deref / type-assert mismatch / etc.) flows
through unwrapped.
This keeps `vm` free of an `interp` dependency, and gives embedders the same
hook for any future signal type they define.
Genuine runtime crashes (`runtime.Error`) keep the `capturePanic` path with
full mvm diagnostics.

### CLI translation (`main.go`)

```go
func main() {
    if err := dispatch(os.Args[1:]); err != nil {
        var ee *interp.ExitError
        if errors.As(err, &ee) {
            os.Exit(ee.Code)
        }
        fmt.Fprintln(os.Stderr, err)
        os.Exit(1)
    }
}
```

mvm's own internal `os.Exit` calls (the host-side dispatch error path) stay
-- they are not interpreted code.

## `mvm test -stat` placement: per-test `t.Cleanup` counter

> **Superseded by [ADR-019](ADR-019-test-runner-mainstart-driver.md).**
> The counter approach below was replaced by driving
> `testing.MainStart(...).Run()` directly, which returns the exit code and lets
> the `-stat` flush land after the package `PASS`/`FAIL` line.
> The primary decision of this ADR (virtualized exit via `*ExitError`) still
> stands; only this sub-section is obsolete.

`testing.Main` is a host-compiled native function (bridged into the
interpreter via `stdlib/ext/testing.go`).
Its body is literally:

```go
os.Exit(MainStart(matchStringOnly(matchString), tests, benchmarks, nil, examples).Run())
```

The `os.Exit` there is the host-compiled os.Exit, not the interpreter's
binding -- so the (A) virtualization does not intercept it, and a deferred
`flushStats()` in `testCmd` would never fire.

The originally-planned switch to `testing.RunTests` is blocked: `RunTests`
reads package-private state (`cpuList`, `*timeout`, `*count`, `*parallel`,
`*match`, `*skip`) that is only initialized by `M.Run()`'s unexported
`parseCpuList()` after `flag.Parse()`.
Calling `RunTests` standalone either nil-derefs the flag pointers or silently
runs zero tests (the outer `for procs := range cpuList` loop body never
executes when `cpuList` is nil).
None of the public testing entry points expose a way to drive that setup
without also calling `os.Exit`.

Instead, the `_testmain` driver wraps each interpreted test through a
host-side `mvmtest.WrapTest` binding:

```go
testing.Main(regexp.MatchString, []testing.InternalTest{
    {Name: "TestX", F: mvmtest.WrapTest(TestX)},
    ...
}, nil, nil)
```

`mvmtest.WrapTest` is a native function `func(func(*T)) func(*T)` that
returns a host closure registering `t.Cleanup(oneDone)` before invoking the
interpreted test:

```go
func(f func(*testing.T)) func(*testing.T) {
    return func(t *testing.T) {
        t.Cleanup(oneDone)
        f(t)
    }
}
```

`oneDone` is an atomic counter decrement; when the last test's cleanup fires,
mvm flushes `-stat` to stderr before native `testing.Main` proceeds to
`m.after()` and `os.Exit`.

Wrapping happens *natively* rather than via an interpreted closure
(`func(t *testing.T) { t.Cleanup(mvmtest.OneDone); TestX(t) }`) because the
interpreted form perturbs the call convention for tests whose body reaches
native reflect paths through their `*testing.T` argument.
go-humanize's `TestBigByteParsing` is the canary: with an interpreted-closure
wrap it panics with `reflect: call of reflect.Value.Field on zero Value`;
unwrapped or wrapped natively it passes.
The native wrapper preserves the same `makeCallFunc` bridge path that
`testing.Main` itself uses to call interpreted test functions -- only one
bridge hop, identical to the unwrapped layout.

`t.Cleanup` (not a `defer`) so testing's runner picks up the callback for
every test-exit path -- return, panic, or `runtime.Goexit` from
`t.Skip`/`t.Fatal`/`t.FailNow`.
A defer registered in interpreted code would miss the Goexit cases: native
`runtime.Goexit` unwinds the goroutine through Go's runtime defer chain,
which has no visibility into mvm's VM defer registry.
testing's runner processes `t.Cleanup` callbacks regardless of how the test
exited.

The counter is sized to the number of tests that will *actually run*: mvm
pre-filters its `Test*` symbol list against `-test.run` / `-test.skip` (first
path segment, mirroring testing's top-level matcher) before constructing the
driver.
A test passed to `testing.Main` but skipped by its own filter would never
enter the wrapper and would leave the counter stuck above zero.

### Stat flush across failure modes

| Failure mode                          | Stats flush? |
|---------------------------------------|--------------|
| `t.Errorf` / `t.Fail`                 | yes          |
| `t.Fatal` / `t.FailNow` (Goexit)      | yes          |
| `t.Skip*` (Goexit)                    | yes          |
| Test panics (e.g. nil deref)          | no           |

The first three all flow through testing's `runCleanup`, so `oneDone`
decrements normally and the counter still reaches zero.
A panicking test is different: testing's `tRunner` recovers the panic,
reports `--- FAIL`, runs cleanups, then deliberately re-panics in the test
goroutine.
The unrecovered re-panic crashes the host process before subsequent tests
run, so the counter never reaches zero and stats are lost.
This matches native `go test`'s behavior on a panicking test (binary exits 2,
no test summary).
mvm cannot intercept this from outside without recovering inside the wrapper
*before* testing's recover gets a chance, which would suppress testing's own
`--- FAIL` reporting -- not worth the trade.

Deferred follow-ups -- vendor `internal/testdeps`, `//go:linkname
testing.parseCpuList` (needs `-ldflags=-checklinkname=0`, breaks
`go install`), or subprocess isolation -- remain available if a future need
(benchmarks, examples, fuzz, honoring `os.Exit(code)` from inside a test as
the host's exit code, or surviving panicking tests for the stats path) makes
the switch off `testing.Main` worth the maintenance cost.

## What changes in observable behavior

- Embedded `Eval` calls that hit interpreted `os.Exit` or `log.Fatal*` now
  return `*interp.ExitError` rather than terminating the host.
- `mvm run` still translates `*interp.ExitError` into a host
  `os.Exit(code)`, so the user-facing exit code is unchanged.
- REPL: an `os.Exit(0)` typed at the prompt currently returns an error and
  stays at the prompt (the `Repl` loop continues past `Eval` errors).
  Honoring the exit by terminating the REPL is a one-liner in `interp.Repl`
  and a separate change.
- `mvm test -stat` prints the stats block after the test output, just before
  testing's package-level `PASS`/`FAIL` line (via the `mvmtest.WrapTest`
  counter described above).
  Previously, stats printed before the driver ran.
  Panicking tests are the one exception -- see the failure-modes table.
- Goroutines: an interpreted `os.Exit` inside a `go func() { ... }()` panics
  that goroutine with `ExitError`, which mvm's per-goroutine `recoverPanic`
  surfaces.
  The host process is not terminated by that goroutine alone -- matching
  native Go's behavior where a panicking goroutine without recover kills the
  whole process from the *host's* perspective, but here mvm contains it.
  This is a behavior change worth knowing about; concurrency-heavy
  interpreted programs that relied on `os.Exit` from a worker goroutine to
  kill the host need to surface the exit code through the main goroutine.

## Files

- `interp/interpreter.go` -- `ExitError`, `installExitVirtualization`
  (replaces `InstallStatsExitHook`), wired from `patchStdlibOverrides`.
- `vm/vm.go` -- `recoverPanic` shape check.
- `main.go` -- `main()` translates `*interp.ExitError`; `setupStats` keeps
  the `sync.OnceFunc` flush.
