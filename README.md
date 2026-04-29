# mvm

Mvm is a fast interpreter and virtual machine for Go and beyond.

## Features

- Fast and portable bytecode virtual machine
- Aims to be fully compatible with Go
- Embeddable in Go, C or others (see examples)
- Integrated REPL, debugger, test engine
- One single static binary, all battery included (full stdlib)

## Usage

Intall the `mwm` command by: `go install github.com/mvm-sh/mvm@latest`

Or from a clone of the repository:

```
go run .                            # start the REPL
go run . _samples/fib.go            # run a Go source file
go run . run _samples/fib.go        # same — "run" is the default subcommand
go run . run -e "fmt.Println(1+2)"  # evaluate an inline expression
go run . test ./pkg                 # run TestX functions in a package directory
go run . help                       # list subcommands
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

## Contributing

See the [Contributing Guide](CONTRIBUTING.md)

## License

Mvm is distributed under the BSD-3-Clause license. See [LICENSE](LICENSE)
for the full text. The vendored Go standard library packages under
`stdlib/src/` remain under their original BSD-3-Clause license (see
[stdlib/src/LICENSE](stdlib/src/LICENSE)).
