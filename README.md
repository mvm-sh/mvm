# mvm

[![CI](https://github.com/mvm-sh/mvm/actions/workflows/go.yml/badge.svg)](https://github.com/mvm-sh/mvm/actions/workflows/go.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mvm-sh/mvm.svg)](https://pkg.go.dev/github.com/mvm-sh/mvm)
[![License: BSD-3-Clause](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](LICENSE)

Mvm is a Go interpreter that compiles source to bytecode and runs it on
a stack-based virtual machine. It ships as a single static binary with
the full Go standard library bundled in, and embeds in Go or C host
programs.

> **Status is alpha.** Language coverage is broad but not yet complete,
> and the embedding API will still change. Pin a commit if you depend
> on it.

## Features

- Fast, portable bytecode virtual machine
- Aims for full Go language compatibility
- Embeddable in Go and C host programs (see [`examples/`](examples/))
- Integrated REPL, debugger (`trap()` builtin), and test runner
- One single static binary, batteries included (full stdlib)

## Why mvm?

_Disclosure: I (@mvertes) also created yaegi at Traefik, in addition to mvm._

There are several Go interpreters; mvm fills a different slot:

- **[yaegi](https://github.com/traefik/yaegi)**: the most mature
  option, AST tree-walking, focused on plugin-style use. Mvm makes a
  different bet: compile once to bytecode, then run on a small VM.
- **[gomacro](https://github.com/cosmos72/gomacro)**: also AST-based,
  with a strong REPL and macro story. Mvm has no macros and
  concentrates on running ordinary Go code through a compact VM.
- **[neugram](https://github.com/neugram/ng)**, **[igo](https://github.com/sbinet/igo)**:
  both no longer maintained.

If you want a small, embeddable runtime that runs idiomatic Go fast
enough for non-trivial programs, mvm is built for that.

mvm starts with Go, but the design points further: the scanner/parser
front-end is built to host other languages, the bytecode compiler is
language-agnostic, and the VM leverages the Go runtime's memory
management and concurrency. Useful well beyond scripting.

## Usage

Install the `mvm` command:

    go install github.com/mvm-sh/mvm@latest

Or from a clone of the repository:

```
go run .                            # start the REPL
go run . _samples/fib.go            # run a Go source file
go run . run _samples/fib.go        # same. "run" is the default subcommand
go run . run -e "fmt.Println(1+2)"  # evaluate an inline expression
go run . test ./pkg                 # run TestX functions in a package directory
go run . help                       # list subcommands
```

A `trap()` builtin drops the program into an interactive debug REPL
where you can inspect the call stack and memory.

The repository contains two example trees: [`examples/`](examples/) for
embedding mvm in Go and C host programs, and [`_samples/`](_samples/)
for Go programs you can run directly with `mvm run`.

A static file server in one line, using the inlined stdlib:

```sh
mvm -e 'http.ListenAndServe(":8080", http.FileServer(http.Dir(".")))'
```

## Documentation

- [docs/index.md](docs/index.md): documentation entry point
- [docs/architecture.md](docs/architecture.md): pipeline, memory model, key design decisions
- [docs/modules/](docs/modules/): per-package reference
- [docs/decisions/](docs/decisions/): architecture decision records (ADRs)

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
