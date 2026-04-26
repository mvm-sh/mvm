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
`Interp.stdlibPatched`). It performs two jobs:

1. **`patchFmtBindings`** overrides `fmt.Print`, `fmt.Printf`, and
   `fmt.Println` in the parser's package registry with closures that call
   `fmt.Fprint`/`Fprintf`/`Fprintln` via `m.Out()`. This redirects formatted
   output to the machine's configured writer (set by `SetIO`) instead of
   `os.Stdout`. The closures capture the `Machine` pointer and resolve
   `Out()` lazily at call time, so later `SetIO` changes take effect
   immediately. `fmt.Stringer` is also exported as a type so interpreted
   code can reference it.

2. **Shadow-package patchers.** For each import path registered via
   `stdlib.RegisterPackagePatcher`, every patcher in the list is called with
   the live machine and the package's `vm.Value` symbol map. This lets
   shadow packages (e.g. `stdlib/jsonx`) splice replacement types and
   constructors into the original `encoding/json` package before interpreted
   code resolves the import. See
   [ADR-012](../decisions/ADR-012-package-patchers-arg-proxies.md).

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

## CLI entry point (`main.go`)

The mvm binary dispatches on the first CLI argument:

| Argument | Action |
|----------|--------|
| (none) | `run` with no args -- enter the REPL |
| `run` | Run a Go source file, evaluate `-e "<expr>"`, or enter the REPL |
| `test` | Run Go tests in a package directory (see below) |
| `-h`, `--help`, `help` | Print usage |
| anything else | Treated as `run` with all args passed through |

`run` wraps stdout in a `newlineTracker` that appends a trailing newline
if the program did not emit one, so the shell prompt is not overwritten.
`stdlib/jsonx` is imported for side effects so its `init()` registers the
json patcher and arg proxies before any interpreter is constructed.

### `mvm test`

A lightweight `go test` analogue for package directories. It reads every
`.go` file in the given directory (default `.`), separates `_test.go`
files from non-test sources, scans the test files for
`func Test*(t *testing.T)` declarations, and stitches a synthetic
`package main` program:

```
package main
import "testing"
<pkg sources with package clauses stripped>
<test sources with package clauses stripped>
func main() {
    testing.Main(/* deps */, []testing.InternalTest{
        {Name: "TestFoo", F: TestFoo}, ...
    }, nil, nil)
}
```

This program is then run through the normal `Eval` path.
`os.Args` is overwritten so `testing.Main`'s flag parsing sees only the
`-test.*` flags that followed the directory argument.

This approach sidesteps having to implement `go test` package layout
rules or coverage instrumentation; it only requires that the package
compiles and that `Test*` signatures are regex-visible at the top of
each file.

## Dependencies

- `comp/` -- compiler (embedded).
- `vm/` -- virtual machine (embedded).
- `lang/` -- language spec.
- `stdlib/` -- for `SrcFS()` (generics-first package fallback) and
  `PackagePatchers()` (shadow-package overlays).
