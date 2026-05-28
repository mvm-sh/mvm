# interp

> Integration layer: wires scan, parse, compile, and execute into a single
> `Eval()` call.

## Overview

The `interp` package provides `Interp`, which embeds both `*comp.Compiler`
and `*vm.Machine`. It is the main entry point for evaluating Go source code
and powers the REPL. The mvm binary (`main.go`) is a thin subcommand
dispatcher around it.

## Key types and functions

- **`Interp`** -- embeds compiler and VM.
- **`NewInterpreter(spec *lang.Spec) *Interp`** -- create an interpreter
  for the given language spec.
- **`Eval(name, src string) (reflect.Value, error)`** -- compile and execute
  source code. `name` identifies the source (`"m:<content>"` for inline,
  `"f:<path>"` for file). Pushes new data and code to the VM incrementally.
  Calls `main()` automatically if defined.
- **`Repl(in io.Reader) error`** -- interactive read-eval-print loop.
  Feeds input line by line to `Eval`. When `Eval` returns `scan.ErrBlock`
  (the scanner detected an unbalanced block), the prompt switches to `>>`
  and the line is accumulated for retry on the next input.

## Internal design

### Incremental evaluation

`Eval` tracks the previous lengths of `Data` and `Code`. On each call it
removes the trailing `Exit` instruction added by the previous run
(`PopExit`), compiles new source, then pushes only the delta to the VM.
This allows the REPL to build up state across evaluations without
recompiling everything. The entry point for the new code is
`max(codeOffset, i.Entry)`, so module-level init code runs before `main`.

### Main function

If a `main` entry exists in `Compiler.Symbols` (the parser/compiler symbol
table), `Eval` emits a `Call` to it after pushing the compiled code. This
mirrors `go run` behavior for standalone programs.

### File-based tests

`interp/file_test.go` provides `TestFile`, which reads every `.go` file
under `_samples/` and runs it through the interpreter. Expected output or
expected error strings are encoded in the last block comment of the file
using the conventions `// Output:\n...` and `// Error:\n...`. This gives
a lightweight integration test suite that exercises the full pipeline end
to end on real Go programs.

### Stdlib patch pass

`patchStdlibOverrides` runs once, on the first `Eval` call (guarded by
`Interp.stdlibPatched`). It performs three jobs:

1. **`patchFmtBindings`** overrides `fmt.Print`, `fmt.Printf`, and
   `fmt.Println` in the parser's package registry with closures that call
   `fmt.Fprint`/`Fprintf`/`Fprintln` via `m.Out()`. This redirects formatted
   output to the machine's configured writer (set by `SetIO`) instead of
   `os.Stdout`. The closures capture the `Machine` pointer and resolve
   `Out()` lazily at call time, so later `SetIO` changes take effect
   immediately. `fmt.Stringer` is also exported as a type so interpreted
   code can reference it.

2. **Package patchers.** For each import path registered via
   `stdlib.RegisterPackagePatcher`, every patcher in the list is called with
   the live machine and the package's `vm.Value` symbol map, which it may
   overlay with replacement symbols. The only remaining patcher is
   `stdlib/runtime_virt.go`, which overlays the `runtime` introspection entry
   points (see
   [ADR-016](../decisions/ADR-016-runtime-introspection-bridge.md)).

3. **`installExitVirtualization`** rebinds `os.Exit` and `log.Fatal*` in
   the parser's package registry so interpreted exit paths surface as a
   typed error instead of terminating the host.
   `os.Exit(code)` becomes `panic(&ExitError{Code: code})`; `log.Fatal*`
   logs through the configured logger, then panics with code 1.
   Each rebind is guarded by an `ok` check, so packages the embedder did
   not import are left alone.
   See [Process exit virtualization](#process-exit-virtualization) below
   and [ADR-018](../decisions/ADR-018-virtualized-process-exit.md).

### Method names for interface bridging

After each `Compile`, `Eval` copies the compiler's reverse method-ID mapping
(`MethodNames`) to the Machine. This allows the VM's `bridgeArgs` to look up
method names when wrapping interpreted values for native Go calls. See
[vm](vm.md#interface-bridging-at-the-native-call-boundary).

### Lazy DebugInfo

`Eval` registers a `debugInfoFn` closure on the VM via `SetDebugInfo`.
This closure calls `Compiler.BuildDebugInfo()` to produce a `*vm.DebugInfo`
populated with the `scan.Sources` registry, label names, global symbol
names, and per-function local variable mappings. The builder is only
invoked if the program hits a `trap()` call, so there is no cost for
normal execution.

### Process exit virtualization

Interpreted code that exits the process (`os.Exit`, `log.Fatal*`, and
native bridges such as `testing.Main`) must not kill the host: it would
take down the REPL and give embedders no catchable signal.
`installExitVirtualization` (above) makes those paths `panic` an
`*ExitError`:

```go
type ExitError struct{ Code int }
func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
func (e *ExitError) CleanExit()    {} // marks it as vm.CleanExit
```

`ExitError` implements `vm.CleanExit`, so the VM's recover path
propagates it unwrapped to the top level instead of wrapping it as a
crash (`*vm.PanicError`) -- see
[vm.md](vm.md#panics-defer-recover-and-diagnostics).
`Eval` (and therefore `Run`) returns it like any other error; callers
recover the code with `errors.As`.
The `main()` CLI translates `*ExitError` back into a host
`os.Exit(code)`, so the user-facing exit status is unchanged.
See [ADR-018](../decisions/ADR-018-virtualized-process-exit.md).

## CLI entry point (`main.go`)

The mvm binary dispatches on the first CLI argument:

| Argument | Action |
|----------|--------|
| (none) | `run` with no args -- enter the REPL |
| `run` | Run a Go source file or remote `main` package, evaluate `-e "<expr>"`, or enter the REPL |
| `test` | Run Go tests in a target package (see below) |
| `version`, `-v`, `--version` | Print module version, Go toolchain, and OS/arch |
| `-h`, `--help`, `help` | Print usage |
| anything else | Treated as `run` with all args passed through |

The `run` and `test` handlers live in `run_cmd.go` and `test_cmd.go`;
`main.go` is just the dispatcher. When `run`'s target is an import path
rather than a file, it fetches and executes the remote `main` package,
forwarding trailing arguments as the program's `os.Args` (see
[Remote imports](../usage.md#remote-imports)).

`run` wraps stdout in a `newlineTracker` that appends a trailing newline
if the program did not emit one, so the shell prompt is not overwritten.
A leading `#!` line on the source file is stripped before evaluation so
shebang-style scripts (`#!/usr/bin/env mvm`) work after `chmod +x`.
`stdlib/all` is imported for side effects so the `core` and `ext` bindings
register their native symbols before any interpreter is constructed.

Both `run` and `test` call `wireFS(i)` after constructing the
interpreter. `wireFS` builds a single `*modfs.FS` honoring `GOPROXY`
semantics, injects the embedded `github.com/mvm-sh/std` zip
(`stdlib.EmbeddedStd()`) so stdlib lookups never need the network, and
wires that one FS into both slots: the parser's stdlibFS via
`stdmod.FS(mfs)` (which redirects stdlib-shaped imports to
`github.com/mvm-sh/std/<pkg>`) and the parser's remoteFS for
third-party imports. One cache backs both. See
[ADR-017](../decisions/ADR-017-std-module-redirect.md).

`buildModFS` resolves `GOPROXY`: unset/empty uses the default public
proxy, `off` and `direct` produce an offline-only modfs (the embedded
zip stays resolvable), otherwise the first URL entry of the comma- or
pipe-separated list becomes the proxy. `NewInterpreter` itself
installs `stdmod.DefaultFS()` (offline-only, embedded zip) so embedders
and tests that don't go through `wireFS` still have a working stdlib;
`wireFS` overrides the slot when network imports are wanted.

Top-level errors are written to stderr verbatim (no `log.Lshortfile`
prefix), so a parser/compiler `file:line:col: msg` reaches the user
unaltered.

### `mvm test`

A lightweight `go test` analogue. Arguments are `mvm test [-x] [-stat]
[target] [test flags]`: `splitTestArgs` peels the mvm-owned leading flags
(`-x`, `-stat`, classified by `isMvmTestFlag`), takes the next non-flag
token as the target, and treats the rest as test flags. The target may be
a local directory (default `"."`) or a remote import path; both paths
share a single synthesized driver at the end.

| Target | Loader |
|--------|--------|
| existing local directory | `os.ReadDir` + per-file `i.Eval(path, content)` |
| import path (e.g. `github.com/google/uuid`) | `i.SetIncludeTests(true)` + `i.Eval(target, "")` (directory-mode `ParseAll`) |

The loader is selected by trying `filepath.Abs(target)` followed by
`os.ReadDir`; on miss the path is treated as an import and resolved
through the parser's FS chain (`pkgfs` -> `stdlibfs` -> `remotefs`),
fetching from the Go module proxy if needed. Test files are included
because `SetIncludeTests(true)` flips a Parser flag that
`LoadPackageSources` reads when the directory branch enumerates `.go`
files. The flag is saved/restored by `importSrc` so transitive
imports never pull in their own `_test.go` files.

After loading, `runTestDriver` collects the three kinds of test function:
`Test*` (via `i.FuncNames("Test")`, then filtered against `-run`/`-skip`
by `filterTopLevelTests`), `Benchmark*`, and `Example*` (via
`collectExamples`). It synthesizes and Eval's a final `_testmain` round
of the form

```
mvmtest.Run(
    []testing.InternalTest{{Name: "TestX", F: TestX}, ...},
    []testing.InternalBenchmark{...},
    []testing.InternalExample{...})
```

`mvmtest.Run` is a host (native) closure that calls
`testing.MainStart(statDeps{}, tests, benches, nil, examples).Run()`,
records the returned exit code, then invokes the `-stat` flush.
Driving `MainStart(...).Run()` directly -- rather than `testing.Main`,
whose body is `os.Exit(MainStart(...).Run())` -- is what lets mvm run the
full test lifecycle, get the exit code back, and flush `-stat` *after*
the package `PASS`/`FAIL` line. `statDeps` (in `test_deps.go`) supplies
the unexported `testDeps` argument `MainStart` requires; only its
`MatchString` does real work, delegating to `regexp.MatchString` so
`-run`/`-skip` matching stays native. `runTestDriver` returns
`*interp.ExitError{Code: code}` for a non-zero exit, which `main()`
translates into the host process status.

`os.Args` is overwritten beforehand with the test flags, run through
`rewriteTestFlags` so the `go test` spellings (`-v`, `-run`, ...) become
the `-test.*` names testing's flag parsing expects -- the same rewrite
`go test` itself does in its CLI wrapper. Benchmarks are passed
unfiltered (testing gates them on `-bench`); fuzz targets are
deliberately not passed. See
[ADR-019](../decisions/ADR-019-test-runner-mainstart-driver.md) and
[ADR-018](../decisions/ADR-018-virtualized-process-exit.md).

The local-directory branch sequentially Eval's files, so cross-file
references (e.g. a func in `a.go` referencing one in `b.go`) only
resolve if the file order happens to match the dependency order. The
import-path branch goes through directory-mode `ParseAll`, which runs
the Phase-1 fixed-point retry loop across the union of all files, and
therefore handles cross-file references uniformly. Promoting the
local-directory branch to the same dir-mode load is a planned cleanup
but currently held off to preserve existing build-tag-relaxed local
behavior.

## Dependencies

- `comp/` -- compiler (embedded).
- `vm/` -- virtual machine (embedded).
- `lang/` -- language spec.
- `stdlib/stdmod/` -- `DefaultFS()` for the offline embedded stdlib FS
  installed by `NewInterpreter`.
- `stdlib/` -- `PackagePatchers()` (shadow-package overlays) and (in
  `main.go` only) `EmbeddedStd()` for `wireFS`.
