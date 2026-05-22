# Changelog

All notable changes to this project are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Compatibility matrix.
  `make compat` runs `mvm test` across the bridged standard library and a
  curated set of external packages, classifies each into a tier with a
  tests-passing ratio, and writes `compat/{compat,badge}.json` plus
  `compat/history.jsonl`.
  The generator (`compat/gen.go`) is plain Go run through mvm itself.
  A weekly (and per-release) GitHub Actions workflow refreshes the data, and
  the matrix is published at [mvm.sh/compat](https://mvm.sh/compat).

## [0.3.0] - 2026-05-22

### Added

- `clear` builtin, the last builtin mvm was missing.
  Works on maps and slices per the Go spec.
- `comparable` is supported as a generic type constraint, alongside
  broader improvements to generics parsing.
- `mvm run <import-path>` fetches and executes a remote `main` package.
  Trailing arguments are forwarded as the program's `os.Args`, the same
  way a local `main` is run.
- `mvm test` runs and validates `Example*` functions.
  Their output is compared against the `// Output:` comment via
  `go/doc`, in addition to running `Test*` and `Benchmark*`.
- `mvm test -bench REGEX` runs benchmarks through native `testing`.
- `-stat` prints compile and execute statistics, on both `run` and
  `test`.
- `interp.ExitError` lets embedders catch interpreted `os.Exit` and
  `log.Fatal*` as a typed error rather than having the host process
  terminate.
  `i.Eval` returns `*interp.ExitError` whenever interpreted code reaches
  an exit path; `errors.As` recovers the exit code.
  The `mvm run` CLI translates the error back into a host `os.Exit(code)`
  so the user-facing exit status is unchanged.
  See ADR-018.
- `cmd/mvmlint`, a small linter for mvm-specific code patterns.
  Runs locally or as a remote main package
  (`mvm github.com/mvm-sh/mvm/cmd/mvmlint .`).
- Standard-library bridges for `encoding/gob`.
- `encoding/xml` round-trips through the new `xmlx` shim.
  Interpreted types implementing `xml.Marshaler`/`xml.Unmarshaler` and
  `encoding.TextMarshaler`/`encoding.TextUnmarshaler` work through native
  `encoding/xml` in both directions.
- Native method hooks let stdlib bridges customize per-method behavior.
- Composite interface bridges, where one interpreted type satisfies
  several native interfaces at once, are supported.
- Test infrastructure to run the Go standard library's own external
  `_test.go` suites against mvm's reflect bridges, via a test overlay
  and a GOROOT filesystem view.

### Changed

- Exit virtualization (`os.Exit`, `log.Fatal*`) is now unconditional and
  wired automatically on the first `Eval`.
  See ADR-018.
- `mvm test -stat` now prints the stats block *after* the test output
  (just before the package-level `PASS`/`FAIL` line) instead of before
  the driver runs.
  The `_testmain` driver wraps each test in a `t.Cleanup` that
  decrements an atomic counter; when the last test completes, mvm
  flushes `-stat` to stderr before native `testing.Main` reaches
  `os.Exit`.
  See ADR-018 and ADR-019.
- Load, parse, and compile errors report `file:line:col` with a source
  snippet and a caret pointing at the offending token, instead of bare
  messages or late panics.
- Runtime panics print a unified stack that interleaves interpreted and
  native (reentrant) frames.
- `make test` runs the interp suite with the race detector only;
  cross-package coverage moved to `make cover` (combining `-race` with
  `-coverpkg=./...` was superlinear).
  A new `make fast` target runs the suite with `-short` and the race
  detector for a quick inner loop.

### Fixed

- Numerics: equality, conversion, and comparison are correct when mixing
  untyped constants with floats; `numSet` handles the zero value; the
  per-frame numeric-value cache stays consistent across frames.
- Pointer receivers: method lookup on a dereferenced pointer receiver,
  pointer-receiver resolution, and interface wrapping of pointer-only
  method sets are fixed.
- Type switches: the `TypeBranch` opcode and several interface-wrapper
  edge cases are fixed.
- Generics and inference: type information is no longer lost in
  multi-return expressions, and inference works on func-typed
  parameters.
- Closures: multi-assignment of captured closure variables, and a case
  where a closure was mis-qualified as a method, are fixed.
- Functions: named-return (out-parameter) parsing, setting a returned
  parameter inside a `defer`, and conversion of values returned from
  native functions are fixed.
- Native calls: panics raised inside native functions called from
  interpreted code are caught and surfaced as mvm panics; a `Call` with
  a nil function address now panics with a diagnostic instead of looping
  forever.
- Array slicing produces correct bounds.
- Interface and method dispatch into native code, stdlib interface
  bridges, and bridging a `Stringer` implemented on a struct field are
  fixed.
- `encoding/json` helper handles the cases needed to pass the stdlib
  `encoding/json` test suite; `cmp` and `errors` internal dependencies
  build for testing.
- `crypto/ecdh` bridges pass the benchmark suite.
- `vm.patchRtype` runs under a critical section, fixing a data race when
  multiple goroutines trigger rtype patching concurrently.

### Removed

- `interp.InstallStatsExitHook`.
  Exit virtualization is unconditional now (wired on first `Eval`), so
  the hook is obsolete.
  Embedders that called it should delete the call and catch exits via
  `interp.ExitError` instead.

## [0.2.0] - 2026-05-18

### Added

- Dynamic network imports via the Go module proxy. `mvm run` and `mvm
  test` can pull third-party modules on demand, respecting `GOPROXY`
  (including `off` for offline-only operation).
- `mvm test <pkgpath>` runs `Test*` functions from a local directory or
  a remote import path. Accepts `go test`-compatible flags: `-v`,
  `-run`, `-count`, `-short`, etc. With a remote target like
  `github.com/<user>/<repo>`, fetches and runs the third-party test
  suite end-to-end.
- VM execution tracing. Bare `-x` gives a per-line trace, `-x=op` an
  opcode trace, `-x=all` both. The `MVM_TRACE=1` environment variable
  enables tracing from within `Eval`.
- `mvm version` subcommand prints the module version, Go toolchain, and
  OS/architecture.
- Old-style `// +build` build constraints are now supported alongside
  `//go:build`.
- `runtime.Callers`, `runtime.FuncForPC`, and `runtime.CallersFrames`
  are virtualized so interpreted stack frames report proper file:line
  and function names. Stack traces and `runtime/debug.Stack()` now show
  user code, not VM internals.
- `fmt.Formatter` bridge: user types implementing `Format(fmt.State,
  rune)` drive every `%`-verb via their own interpreted code. Unblocks
  `pkg/errors` formatting including `%+v` with stack frames.
- `errors.Is` walks `Unwrap` chains that mix native and interpreted
  error types.
- `reflect.TypeFor[T]()` (Go 1.22+) is provided via a generic shim.
- `reflect.Value.MethodByName` works on mvm `Iface` values.
- Composite-literal struct fields can shadow builtin names
  (e.g. `T{len: 5}` where `len` is the field name).

### Changed

- The standard library now ships as the `github.com/mvm-sh/std`
  synthetic module via the `stdmod` package. Third-party imports and
  stdlib share the same module-resolution pipeline (ADR-017).
- Stdlib bindings track Go 1.26.3. Symbols introduced in Go 1.25+ and
  1.26+ are isolated into build-tagged `*_go12N.go` files so mvm still
  builds against the floor `go 1.24`.
- The cross-package symbol table now uses canonical package-qualified
  keys throughout (Phase 1 + Phase 2 path B refactor). Closes a class
  of bugs where sibling imports with same-named types, funcs, methods,
  vars, or consts would clobber each other. Notable beneficiary:
  `golang.org/x/text` dual-imports of `language` and
  `internal/language`.
- Comments are stripped at the scanner level; the parser no longer
  needs to filter them, retiring a class of "comment leaks into
  Split-loop" bugs.
- The test driver runs `Test*` functions in source-declaration order
  instead of alphabetical order. Matches `go test` behavior and avoids
  order-dependent failures (e.g. uuid's `TestRandPool` exhausting the
  rand pool before `TestRandomUUID`).
- `mvm test` applies dynamic network imports the same way `mvm run`
  does.
- Generated `op_string.go`, `token_string.go`, and `kind_string.go` are
  committed. `make generate` is idempotent; CI runs a periodic full
  regeneration check instead of regenerating stdlib on every commit.
- `mvm test`'s CLI flag layout follows `go test` conventions:
  mvm-specific flags appear before the target, test flags after.

### Fixed

- ActiveMachine concurrency races: multiple goroutines sharing the
  active-machine pointer no longer interleave incorrectly.
- `CallFunc` concurrency follows Go spec rules; per-callback allocation
  snowballing under repeated invocation is gone.
- Many parser edge cases: stray comments inside `var (...)` blocks and
  `switch` bodies; composite literals with non-constant keys,
  parenthesized type conversions in struct field values, and
  array-type keys.
- `(*time.Duration)(d).String()` no longer hangs in infinite recursion.
- Generic instantiation: pointer-type-arg names no longer break the
  mangling guard. Closes a `make generate` hang on `net/http`.
- Struct and array pass-by-value: callee field/index writes no longer
  leak back to the caller's storage. Value-receiver method bodies see
  their own copy of the receiver.
- Bitops: shift truncation, `&^` (and-not), and boolean `Not` now
  produce correct results.
- Numeric conversions are correctly applied in multi-return
  assignments.
- Interface bridging: identical structural types are distinguished
  correctly; pointer-receiver methods are reachable through
  user-defined interfaces; method-set metadata survives cross-package
  type registration.
- `reflect.DeepEqual` and `==` work across the mvm/native value
  boundary, including wrapped types from interface bridges.
- `runtime.Func` sentinel `pc-1` lookups are checkptr-clean: the
  intercept side table is keyed by `uintptr` rather than
  `unsafe.Pointer`.
- Closure naming follows Go's `OuterFunc.funcN` stack-trace convention.
- Mixed keyed/unkeyed slice composite literals (e.g. `[]int{2: 7, 9}`)
  produce the correct length and place each element at the right
  index.
- `runtime.Callers` file paths match between `mvm run` and the
  `interp.TestFile` harness.
- Range over invalid subjects, integer with two iteration variables,
  and channel with two iteration variables now emit clean compile-time
  errors with source locations instead of late VM panics.
- Malformed expressions report a parse error instead of panicking.
- `import "golang.org/x/text/language"` compiles end-to-end. The
  original blocker was a SIGSEGV in `vm.patchRtype`; subsequent fixes
  resolved a chain of cross-pkg resolution failures, reflect-via-mvm
  dispatch gaps, and named-return zero-init issues.

### Performance

- New `CallImmFast` opcode skips `detachByValueArgs` at direct-call
  sites whose callee has no Struct or Array parameter. fib(35) drops
  from 369 to 333 ms/op; similar wins on numeric-call-heavy workloads.
- `runtimeFuncMeta` is now interned per call site, bounding memory at
  `O(distinct call sites)` rather than `O(captures)`. Removes a slow
  leak under repeated `runtime.Callers` use.
- Hot-loop micro-optimizations in `vm.Run`: pointer-based instruction
  fetch, hoisted trace-flag check, and several opcode bodies extracted
  to release register pressure.
- Variable-dependency init-order analysis runs only when its result is
  actually needed.

### Removed

- `comp/dump.go` (dead experiment helper).
- `Symbol.RecvType` Phase-1 cache field. Receiver-type binding in
  Phase 2 method bodies now goes through the unified `symGet`
  qualified-probe path; the cache is obsolete after Phase 2 path B
  step 2.
- Stale `FIXME` in `comp/compiler.go`'s `lang.Range` handler ("handle
  all iterator types"). All official Go range subjects (integer,
  array, slice, string, map, channel, function) have been supported
  for some time.

## [0.1.0] - 2026-05-04

Initial public release. Imported from
[mvertes/parscan](https://github.com/mvertes/parscan) at commit
d7aa040.
