# Contributing

Mvm is a free and open-source project, and your feedback and
contributions are needed and always welcome.

[Issues](https://github.com/mvm-sh/mvm/issues) and
[pull requests](https://github.com/mvm-sh/mvm/pulls) are opened at
<https://github.com/mvm-sh/mvm>.

## Building and testing

```
make test    # tests with race detector and coverage
make lint    # golangci-lint (gofumpt + the linters in .golangci.yaml)
```

Single-package and single-test forms used while iterating:

```
go test -v ./interp
go test -run TestExpr ./interp
go test -run "TestExpr/#05" ./interp
go test -bench Fib ./interp
```

## Adding a regression test

Most language- and stdlib-level bugs are reproduced by adding a small
program under `_samples/` and asserting its output against `go run`'s.
The interpreter test in `interp/interpreter_test.go` walks `_samples/`
and runs each file.

- A `_samples/*.go` file is expected to compile and run, and to print
  its `// Output:` block.
- To skip a known-broken sample without removing it, add a top-level
  comment `// skip: reason here`. The runner will leave it in place
  but not execute it. **Do not delete a failing sample**. Keeping it
  visible is how we track outstanding bugs.
- For tests defined directly in `interp/interpreter_test.go` (the
  `etest` table), set `skip: true` on the entry rather than commenting
  it out.

## Style

- Code is formatted with `gofumpt` (a stricter superset of `gofmt`),
  enforced by golangci-lint.
- Generated files (`vm/op_string.go`, `lang/token_string.go`,
  `symbol/kind_string.go`, and the bindings under `stdlib/core/` and
  `stdlib/ext/`) are produced by `make generate`. Don't edit them by
  hand.

## Documentation

- [docs/index.md](docs/index.md): documentation entry point
- [docs/architecture.md](docs/architecture.md): pipeline, memory model, key design decisions
- [docs/modules/](docs/modules/): per-package reference
- [docs/decisions/](docs/decisions/): architecture decision records (ADRs)

If you change behavior covered by an ADR, please update the ADR (or
add a new one) in the same PR.
