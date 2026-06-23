# Changelog

All notable changes to this project are documented in this file. The
format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.5.0] - 2026-06-23

94 of 169 bridged standard-library packages and 104 of 108 curated
external modules now pass their full upstream suite, newly including
`gonum`, gRPC and Protocol Buffers.

### Added

- Arithmetic and comparison operators on complex numbers (`+`, `-`, `*`,
  `/`, `==`, `!=`), completing the 0.4.0 complex-number support.
- Float and sub-word struct shapes in word-class dispatch (ADR-022), so an
  interpreted type whose methods take or return floats and small packed
  structs satisfies a native interface (e.g. `gonum/plot`).
- `//go:embed` directive support.
- On-disk caching of network imports under `<UserCacheDir>/mvm/download`;
  `MVMCACHE=off` disables it, `MVMCACHE=<dir>` relocates it.
- Ctrl-C in `mvm run`/`test` dumps the interpreter stack; a second exits
  with status 130.
- `MVM_DEBUG_COMP` enables compile-phase tracing.

### Changed

- Native method dispatch falls back to word-class shapes keyed on ABI
  register layout, so new interface surface no longer needs a
  per-signature stub. See
  [ADR-022](docs/decisions/ADR-022-word-class-dispatch.md).
- Compatibility matrix groups external modules into labelled families and
  adds `gonum`, gRPC and Protocol Buffers.
- `stdlib.Incompat` gained allocation- and stress-dependent tests.
- An untyped floating constant with no context defaults to `float64`.

### Fixed

- Reflect materialization of complex, recursive and self-referential
  types: forward-declared pointer/array fields, self-embedding structs,
  and mutual pointer cycles materialize without panicking or looping.
- Interface method resolution goes through the symbolic type, fixing
  embedded interfaces and methods promoted through an embedded interface
  field.
- Generics: instantiation in the template's package, self-referential and
  forward-referenced types, reused type names, qualified composite type
  args, and interface-method/constraint-type inference.
- Promoted methods (named, unexported, on a nil pointer receiver) and
  method-bearing derived structs dispatch correctly; a method receiver no
  longer crashes the VM.
- Numerics: `float32`/`complex64` constant overflow, `math.MaxInt32`
  overflow, comparison operand-type misinference, and new
  `GreaterEqualFloat`/`LowerEqualFloat` opcodes.
- Maps/iterators: range over a map, `iter.Pull2` with fewer than two
  variables, deep-interface map values, and interface-typed map keys.
- A nil dereference raises a recoverable panic; typed-nil comparison and
  asserting against a nil pointer are correct.
- VM: funcval-field lifetime, reentrant-call trampolines, `runtime.Goexit`
  interception, aliased parameters, `defer` receiver detachment, and named
  return-slot zeroing.
- `comp`: cross-package `init` ordering, `return` before a logical
  expression, stack overflow in deferred-call expressions, and composite
  literals with nil slice/map elements.
- Parser: multi-assignment forms, spread calls, the global blank, a blank
  `const`, `type`/`var` collisions from auto-import, forward types,
  package qualifiers, deref type identity, and evaluating an op-assign LHS
  once.

### Performance

- Faster local-variable indexing and method resolution; local symbols
  refreshed at unit entry.

## [0.4.2] - 2026-06-11

### Added

- Assign-form range loops (`for x, y = range e`).
  Loop variables may now be existing variables, including captured ones,
  indexed elements (`a[i]`) and pointer derefs (`*p`).
  Previously only the `:=` form compiled correctly.
- Source overlays for assembly-backed declarations.
  Body-less functions such as `syscall.RawSyscall` now resolve through a
  registered Go source shim, enabling `golang.org/x/sys/unix` and its
  dependents (`go-isatty`, `fatih/color`).
- New synthetic dispatch shapes (S32-S38), so interpreted types can satisfy
  `log/slog.Handler`, `slog.LogValuer`, `io.RuneReader` and niladic marker
  methods across the native bridge.
- `panic(nil)` compatibility: `mvm test` of a module whose `go.mod` declares
  a Go version before 1.21 recovers `nil` (not `*runtime.PanicNilError`),
  mirroring the runtime's `panicnil` `GODEBUG` default.

### Fixed

- Data race in rtype materialization when two compilations run concurrently.
  The materialization pass now runs under a single lock.
  No measurable effect on the interpreter hot path.
- `mvm test fmt` now fully passes.
  Method-bearing interpreted values nested in a composite (e.g. a `[]any`
  literal) no longer leak their internal interface box into native reflect
  walks such as `reflect.DeepEqual` and `fmt` formatting.
  Pointers, slices and maps unbox in place, preserving identity, so a
  callee mutating through such an argument reaches the caller's data
  (zerolog and logrus hooks rely on this).
- Writes through `&v` of a captured variable are no longer lost after the
  variable is reassigned (e.g. `b = nil` then `fmt.Sscanf(..., &b)`).
  The closure heap cell now always keeps addressable cell-owned storage.
- A bare `return nil` now yields a typed nil of the declared result type.
  Passing such a call result directly to a native variadic
  (e.g. `fmt.Printf("%q", f())`) used to panic in reflect.
- Deferred-call panics: calling a nil deferred function panics at the
  defer site with a proper location, and a panic raised inside a deferred
  call is catchable by an enclosing `recover`.
  Goroutine, subtest and re-raised panics now carry source locations and a
  full interpreter stack across native boundaries.
- Calling a variadic native function through a function value (rather
  than by name) now packs its arguments into the variadic slice.
- Assigning a struct field to a package-level function variable is no
  longer a silent no-op.
- Named-type identity across the native boundary: methodless named basic,
  slice and func types keep their own reflect identity (`fmt %#v`,
  `go-cmp`); named `[]byte` types such as `net.IP` and `json.RawMessage`
  keep theirs in type switches and interface conversions; a typed nil func
  value keeps its named type so `fmt` still dispatches its `String` method.
- Methods promoted from an embedded native concrete type now satisfy
  native interfaces (e.g. `struct{*bytes.Buffer}` used as an `io.Writer`).
- Interface boxing of map-literal values and of promoted method values;
  two sibling closures declaring same-named func-local anonymous structs
  no longer share a type symbol.
- A function parameter captured by a closure is promoted to its heap cell
  at function entry, so writes through earlier-captured pointers are no
  longer lost.
- An untyped-constant shift operand now adopts the type of its context
  instead of defaulting to `int`.
- A failed `Eval` rolls back compiler and symbol state, so a REPL session
  survives an erroneous line that touched generics.
- Deep-unboxing a cyclic data structure no longer hangs.
- Dot-imported symbol qualification, and method lookup on synthesized
  rtypes: a method attached from interpreted code no longer preempts the
  qualified symbol lookup (which lost pointer-receiver write-backs).
- Generics: a named constraint interface embedded as a union term
  contributes its type elements; a package-qualified type whose bare name
  matches a type parameter no longer binds it.
- Parser: labels no longer collide with same-named variables or with
  compiler-synthesized labels, including labels on `case` and `select`
  clause bodies; a `switch` without `default` no longer leaks an operand
  stack slot; `:=` in an `if` body no longer leaks into the `else if`
  condition; each `case` clause scopes its own declarations; variadic
  parameters and spread calls accept a trailing comma; `<-chan T` parses
  as a receive-only channel type, not a receive.
- A captured pointer variable now zeroes to a nil pointer instead of a
  pointer to a fresh element; an embedded pointer field keeps its field
  name in the materialized reflect layout.
- `mvm test` chdirs into the package directory before loading, so test
  files that read `testdata/...` in package-level `var` initializers work.

### Changed

- Skiplist pruned: limitations lifted by earlier synth-type work let
  `flag.TestPrintDefaults`, `flag.TestUserDefinedBoolUsage`,
  `slog.ExampleHandler_levelHandler` and `slog.ExampleLogValuer_secret`
  run and pass; 7 long-skipped interpreter regression tests
  (named-const method identity, `errors` Is/As bridge cases) re-enabled.
- Sources now parse with the conventional `purego` and `safe` build tags
  enabled, selecting pure-Go, reflect-friendly fallbacks in third-party
  code (e.g. `go-spew`, `x/crypto`).
- `stdlib.Incompat` gained entries for tests that re-exec the binary,
  depend on allocation counts, or stress random inputs at native speed.
- External suites newly passing since 0.4.1: `fatih/color`,
  `rs/zerolog`, `sirupsen/logrus`, `shopspring/decimal`, `tidwall/gjson`,
  `tidwall/sjson`, `tidwall/pretty`, `yuin/goldmark` and
  `valyala/fastjson`.

## [0.4.1] - 2026-06-09

### Added

- External test units. `mvm test` now compiles and runs a package's external
  `package X_test` suite as a second unit alongside the internal `package X`
  tests, matching `go test`'s internal/external split.

### Changed

- `stdlib.Incompat` now also lists long-running stress tests that ignore
  `-short`: the `golang.org/x/text/unicode/norm` `Writer`/`Reader` suites
  (which re-run the corpus across 16 buffer sizes) and several
  `github.com/oklog/ulid/v2` quick-checks now show `SKIP`, so the
  compatibility matrix no longer times out.
- Generic instantiation mangling simplified.
- Type-switch and `test`-command handling refined, with richer
  `file:line:col` diagnostics on parse errors.
- Compatibility matrix refreshed.

### Fixed

- `make(map[K]V, n)` with a size hint, assigned to a package-level variable,
  no longer yields a nil map.
  The hint was left on the stack and the store wrote it in place of the map.
- Slicing an array whose element is a method-bearing named struct (`arr[:]`)
  preserves the named element type instead of degrading to the anonymous
  struct layout.
- A pointer-receiver method auto-addressing a local of a named scalar type now
  mutates the local in place rather than a detached copy.
- A legal mutual struct cycle broken by a pointer no longer exhausts memory
  while its reflect layout is materialized.
- Self-referential types: names on self-referential fields, promoted methods
  on recursive structs, and related layout issues.
- Generics: forward-reference instantiation, inference through a named generic
  instance, a type parameter colliding with a package symbol, and parameter
  handling in func-type declarations.
- Parser: a pointer in a literal composite, postfix index expressions,
  `switch`/`select` case clauses, and forward-declaration placeholders.
- Top-level redeclarations are detected and no longer clobber existing symbols.
- Iterator frames no longer leak memory.
- `defer` on an expression call; an `Eval` re-entrance issue.
- A value read from an unexported struct field is writable where Go allows.
- Channel send correctness, plus improved goroutine-panic diagnostics.
- Interpreted functions keep a stable `reflect.Value` identity across calls.
- Typed numeric conversions.
- File-by-file compilation and build-constraint handling in the test harness.

## [0.4.0] - 2026-06-05

### Added

- Compatibility matrix.
  `make compat` runs `mvm test` across the bridged standard library and a
  curated set of external packages, classifies each into a tier with a
  tests-passing ratio, and writes `compat/{compat,badge}.json` plus
  `compat/history.jsonl`.
  The generator (`compat/gen.go`) is plain Go run through mvm itself.
  A weekly (and per-release) GitHub Actions workflow refreshes the data, and
  the matrix is published at [mvm.sh/compat](https://mvm.sh/compat).
- Preliminary support for complex numbers: `complex`, `real`, `imag`
  builtins and untyped/typed complex literals.
  Arithmetic operators (`+`, `-`, `*`, `/`, `==`, `!=`) on complex values
  are not yet implemented.
- `mvm run a.go b.go ...` compiles all provided files as a single
  compilation unit, so sibling forward declarations across files resolve
  correctly.
- `mvm run -e 'expr'` prints the last result of the expression in
  addition to running it.
- `extract` subcommand: runs correctly from inside the target package
  directory, and the default mode now emits a binding `ImportPackageValues`
  map keyed by full import path for any package, not just the standard
  library.
- Cross-language benchmarks under `bench/` compare mvm against Go, Lua,
  and Python on `fib` and `sieve`.
- `errors.Is` and `errors.As` on interpreted interface types, including
  multi-error chains (`Unwrap() []error`).
- `reflect.TypeAssert[T]` generic shim.
- `%T` reports the interpreted type name instead of a bridge wrapper.
- Stubs for `testing` package so `flag` and similar tests run end-to-end.
- `stdlib.Incompat` skiplist: known architectural mismatches in stdlib
  tests are rewritten to `mvmtest.SkipFn(reason)` so they show `SKIP`
  rather than `FAIL`, keeping the compatibility matrix honest.
- Custom linter rules wired into `make vet` (symbol-key and pos/base
  invariants), plus broader linter coverage (`copyloopvar`,
  `gocheckcompilerdirectives`, `makezero`, `wastedassign`, `errorlint`,
  `nolintlint`); `nolint:gosec` sweep reduced from 189 to 5.

### Changed

- Native method dispatch now synthesizes a real Go `reflect` type that
  carries the interpreted method set, attached to every compiled type.
  This replaces the per-call interface-bridge and argument-proxy layer
  (ADR-009, ADR-012) and the hand-written shadow packages, so an
  interpreted type can satisfy several native interfaces at once (e.g.
  `Stringer` and `json.Marshaler`) and reflect-walking native code sees
  interpreted methods on nested struct fields.
  Synthesized rtypes run at native speed where the old bridges allocated.
  See [ADR-021](docs/decisions/ADR-021-synthesized-rtypes.md).
- Stdlib and external test coverage expanded markedly: 90 of 169 bridged
  packages and 16 of 50 curated external modules now pass their full
  upstream suite, with many more passing a majority.
  Newly green this cycle include `errors`, `text/tabwriter`,
  `log`/`log/slog`, `html/template`, `io/fs`, `runtime/debug`, and
  `go/types`.
- `slices` and `maps` standard-library packages are now fully
  interpreted rather than bridged.
  `testing/quick` is also interpreted now; its `_test.go` ships in the
  std mirror and exercises the interpreter end-to-end.
- Generics inference reworked.
  Type parameters are bound through the symbol table by identity rather
  than name-substitution; the old `substituteTokens` machinery and the
  `typeArgSources` source-tracking subsystem are gone.
  A cycle guard catches recursive generic types.
  Closes a class of inference gaps (notably the `slices` and `maps`
  packages now infer at parity with `go build`).
- Interface satisfaction is signature-aware.
  Previously a method was matched on name alone, so `Unwrap() []error`
  spuriously satisfied `interface{ Unwrap() error }`; both
  `Implements`/`MissingMethod` and `TypeAssert`/`TypeBranch` now compare
  full signatures.
- Generic interface constraints such as `[T error]` are satisfied by
  interpreted concrete types via a name-based method probe.
- Constant folding handles high-precision constants better and folds
  more aggressively.
- `mvm test` skip lists for stdlib tests with known incompatibilities
  now surface as `SKIP` with a reason instead of `FAIL`.

### Fixed

- Package `init` no longer re-runs on every re-entrant `Eval`.
  The start IP was taken from the compiler offset and skipped leftover
  init/main shims; `mvm test flag` went from 0 to 28 passing tests.
- `a, b = b, a` swap of pointer or reference locals.
  All non-define multi-RHS assignments now go through `_swap_` temps.
- Type assertions on the `type` type preserve the interpreted type
  identity.
- Native method expressions in all forms.
  `(*big.Int).Add` direct, stored, and passed through `reflect.Method.Func`
  now work; `mvm test math/big` passes.
- Top-level function redeclarations are rejected with
  "redeclared in this block" instead of hanging in an infinite loop.
- Bare `nil` passed to a slice, map, pointer, channel, or function
  parameter is coerced to a typed nil at the call site so `len` and
  `range` no longer panic.
- A variable that shadows its own type (`T := &T{}`) no longer corrupts
  the `:=` LHS token; nil-interface-var to native-param coercion and
  bridge `Is` on uncomparable values are also fixed.
- Dot-imported package vars now carry their type, so `CommandLine.Parse(...)`
  resolves through a dot import.
- Hexadecimal float literals scan correctly.
- Shift operations on wide (high-precision) constants.
- User functions whose names shadow Go builtins compile correctly.
- Constants from dot imports and typed constants generally.
- Generic type inference no longer collides when distinct type
  parameters share a name.
- Test files whose build tags exclude the current `GOARCH` are skipped
  at parse time instead of failing.
- Forward declarations within grouped `type ( ... )` blocks resolve
  through a shared `parseDeferring` helper.
- Interpreted method lookup is faster and more correct on receivers
  reached through native bridges.
- `Equal` unwraps native `interface{}` to the concrete value when
  comparing across the mvm/native boundary; `GetLocal`/`GetLocal2`
  re-sync `num` from `ref` for addressable numeric slots so writes
  through a native pointer are visible.
- Interface bridges: improved coverage of stdlib interface conversions
  in both directions.
- The zero value of a map or slice variable is now nil, matching Go.
  A bare `var m map[K]V` or `var s []T` is a nil container, while a
  composite literal or `make` stays non-nil even when empty, and writing
  to a nil map raises a recoverable panic.
  Two same-type composite literals in one function each get their own
  container (a regression where the first stayed nil is fixed).
- Remote modules with a major-version suffix (`v2+`) resolve correctly.
  Probing now verifies the candidate module owns the import sub-path, so
  semantic-import-versioning paths such as `github.com/blang/semver/v4`
  no longer mis-resolve to the v1-era module.

### Performance

- New superinstructions and peepholes in the VM.
  `Not+JumpFalse` peephole; `JumpTrue` fusion mirror; `AddLocalLocal`
  and `IntImm`; non-addressable numeric slots; wide-immediate jump
  fuse; `IndexSetBool`; `GetLocalSync` to sync `num` with `ref` on
  load.
- Sieve of Eratosthenes drops from 100 ms to 32 ms (-68%), beating
  `lua5.4` on the same benchmark by ~14%.
  `fib` is unchanged.

### Removed

- The hand-written interface-bridge shadow packages
  (`errorsx`, `fmtx`, `jsonx`, `xmlx`, `gobx`), superseded by
  synthesized rtypes (ADR-021).
- `substituteTokens` and its helpers from the generics-inference
  pipeline (replaced by symbol-table identity binding).
- `typeArgSources` source-tracking subsystem.
- `rotateRight` mirror patch from `slices` (no longer needed).

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
