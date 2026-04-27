# mvm

Mvm is an experimental interpreter for Go. Source code flows through a
four-stage pipeline (`scan` → `goparser` → `comp` → `vm`) producing
bytecode that runs on a stack-based virtual machine.

## Language coverage

Most of Go works, including features people often expect to be missing
from an interpreter:

- generics (monomorphization) with `~T`, union, `comparable`, and
  interface-with-union constraints; generic methods, generic type aliases
- goroutines, channels, full `select` (default, send, receive, ok-form)
- `defer` / `panic` / `recover`, closures, named returns
- interfaces with type assertions and type switches; native-Go interface
  bridging so interpreted values satisfy Go interfaces (e.g. `fmt.Stringer`)
- `range` over slices, maps, strings, channels, integers, and functions
- `iota`, embedded structs, methods on value and pointer receivers
- `//go:build` build constraints, `//go:embed`
- a large slice of the standard library (`fmt`, `strings`, `bytes`,
  `encoding/json`, `reflect`, `sync`, `context`, `time`, ...)

Known gaps:

- not every range-over-func iterator shape is supported
  (`comp/compiler.go` has a FIXME)
- compile-time `const` expressions accept only a small set of calls
  (most `len(...)` and arbitrary function calls in const context aren't
  folded)
- some parser diagnostics are missing (label validity, missing-return
  detection); programs that would compile with `go build` work, but
  some invalid programs are accepted silently
- inline cgo (`import "C"` with C in adjacent comments) and hand-written
  assembly are out of scope by design

See [`_samples/`](_samples/) for working programs and
[`docs/architecture.md`](docs/architecture.md) for the design.

Note: this is experimental and the API is unstable.

## Usage

From a clone of the repository:

```
go run .                          # start the REPL
go run . _samples/fib.go          # run a Go source file
go run . run _samples/fib.go      # same — "run" is the default subcommand
go run . run -e "1+2"             # evaluate an inline expression
go run . test ./pkg               # run TestX functions in a package directory
go run . help                     # list subcommands
```

A `trap()` builtin drops the program into an interactive debug REPL where
you can inspect the call stack and memory.

## Documentation

- [docs/index.md](docs/index.md) — documentation entry point
- [docs/architecture.md](docs/architecture.md) — pipeline, memory model, key design decisions
- [docs/modules/](docs/modules/) — per-package reference
- [docs/decisions/](docs/decisions/) — architecture decision records (ADRs)

## Build

```
make test    # tests with race detector and coverage
make lint    # golangci-lint
```

## License

Mvm is distributed under the BSD-3-Clause license. See [LICENSE](LICENSE)
for the full text. The vendored Go standard library packages under
`stdlib/src/` remain under their original BSD-3-Clause license (see
[stdlib/src/LICENSE](stdlib/src/LICENSE)).
