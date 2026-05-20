# ADR-019: `mvm test` drives `testing.MainStart(...).Run()` directly

**Status:** accepted
**Date:** 2026-05-20

Supersedes the "`mvm test -stat` placement: per-test `t.Cleanup` counter"
sub-decision of [ADR-018](ADR-018-virtualized-process-exit.md).
ADR-018's primary decision (virtualized process exit via `*interp.ExitError`)
still stands.

## Context

`mvm test` synthesizes a `_testmain` program and `Eval`s it to drive the loaded
package's `Test*`/`Example*` functions through the native `testing` package.

ADR-018 drove them via `testing.Main`, whose body is
`os.Exit(MainStart(...).Run())`.
Because that `os.Exit` is host-compiled (not the interpreter's virtualized
binding), it terminates the process and never returns control to mvm.
To still emit the `-stat` summary, ADR-018 wrapped every test in a host-side
`mvmtest.WrapTest` that registered `t.Cleanup(oneDone)`; an atomic counter
fired the flush when the last test's cleanup ran.

That worked but had two costs.

- **Placement.** The flush fired from a cleanup *inside* `M.Run()`, so it landed
  before testing prints the package `PASS`/`FAIL` line.
  Users asked for the stats *after* that line, where a normal `go test` summary
  would sit.
- **Fragility.** The counter had to be pre-sized to exactly the set of tests
  that would actually run (mirroring `-run`/`-skip` filtering), or it would
  never reach zero and the flush would never fire.

ADR-018 recorded the `testing.RunTests` alternative as "blocked": `RunTests`
iterates `cpuList`, which stays nil unless `M.before()`'s unexported
`parseCpuList()` runs, so standalone it executes zero tests.
That much is still true.
What ADR-018 missed is that `M.Run()` (not `testing.Main`) is what calls
`before()` -> `parseCpuList()`, and `M.Run()` itself only *returns* the exit
code -- it is `testing.Main` that wraps the return in `os.Exit`.
So `MainStart(...).Run()` runs the full lifecycle and hands the code back.

The remaining obstacle was `MainStart`'s first argument: the unexported
`testing.testDeps` interface.
It is implementable from outside `testing` after all, because its fuzzing
methods are typed over `corpusEntry`, which `testing` declares as a type *alias*
to an anonymous struct -- any package can spell that same anonymous struct and
match the method signatures exactly.

## Decision

Drive the suite with `testing.MainStart(statDeps{}, tests, nil, nil, examples).Run()`
and flush `-stat` after it returns.

- `statDeps` (in `test_deps.go`) implements `testing.testDeps`.
  Only `MatchString` does real work -- it delegates to `regexp.MatchString`,
  keeping `-run`/`-skip` matching fully native.
  The rest (profiling, coverage, testlog, fuzzing) are no-ops, since `mvm test`
  does not drive those features.
  A local `corpusEntry` type alias mirrors testing's so the fuzzing-method
  signatures line up.
- The `_testmain` driver shrinks to a single host call,
  `mvmtest.Run([]testing.InternalTest{...}, []testing.InternalBenchmark{...}, []testing.InternalExample{...})`,
  passing the interpreted `Test*`/`Benchmark*`/`Example*` funcs unwrapped.
  The host closure calls `MainStart(...).Run()`, records the exit code, then
  invokes the `-stat` flush.
- `runTestDriver` returns `*interp.ExitError{Code: code}` when the suite's exit
  code is non-zero, so the existing `main()` translation produces the right
  process exit status without printing a spurious error line.

The atomic counter, `mvmtest.WrapTest`/`WrapExample`, and the pre-sizing of the
counter against the filtered test list are all removed.

### Benchmarks and fuzzing

Benchmarks ride the same driver: `M.Run()` calls
`runBenchmarks(importPath, deps.MatchString, m.benchmarks)`, which needs nothing
from `statDeps` beyond the native `MatchString`.
`runTestDriver` passes the full `Benchmark*` list unfiltered (testing gates them
on `-bench`), and counts them in the "no tests to run" guard so a
benchmarks-only package still drives.

Fuzz targets are deliberately *not* passed.
Their seed corpus would run for free (via `runFuzzTests`), but active fuzzing
(`-fuzz`) routes through `deps.CoordinateFuzzing`/`RunFuzzWorker`, which
`statDeps` stubs to no-ops -- so `-fuzz` would report `PASS` without fuzzing
anything, a silent false success.
Real fuzzing is infeasible regardless: the engine lives in the std-internal
`internal/fuzz`, and Go drives it by re-exec'ing the test binary as worker
subprocesses, which has no analogue in mvm's single-process interpreted model.
Passing fuzz targets only to get the seed corpus would expose the misleading
`-fuzz` no-op, so they stay out until `-fuzz` can be rejected explicitly.

### Why `statDeps` is acceptable maintenance

If a future Go revises the `testDeps` method set, `MainStart(statDeps{}, ...)`
stops compiling -- a loud, build-time failure, caught by CI, not a silent
runtime drift.
This is the same version-coupling mvm already accepts for its hand-maintained
stdlib bindings.

## What changes in observable behavior

- `mvm test -stat` prints the stats block *after* the package `PASS`/`FAIL`
  line, matching where a `go test` summary sits.
  Previously it printed just before that line.
- An interpreted test that reassigns the host `os.Stderr` (e.g.
  `spf13/pflag`'s `TestBytesHex` points it at `/dev/null`) no longer swallows
  the summary: `setupStats` snapshots `os.Stderr` up front and flushes there.
- Failure modes are unchanged from ADR-018's table: `t.Errorf`, `t.Fatal`, and
  `t.Skip` all flush stats (their paths return through `M.Run()`); a panicking
  test still loses stats, because testing re-panics and crashes the process
  before `Run()` returns.
- `mvm test -bench <re>` now runs `Benchmark*` functions; without `-bench` they
  are ignored, exactly as `go test` behaves.

## Files

- `test_deps.go` -- `statDeps` and the `corpusEntry` alias.
- `test_cmd.go` -- `runTestDriver` rewritten around `mvmtest.Run` +
  `testing.MainStart(...).Run()`.
- `main.go` -- `setupStats` snapshots `os.Stderr` before tests run.
- `stdlib/ext/testing.go` -- already exports `MainStart` (no change).
