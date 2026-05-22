# Usage Guide

This guide covers the `mvm` command line tool: its subcommands, the program
forms it accepts, the REPL, execution tracing, the `trap()` debugger, remote
imports, and the environment variables it reads. For internals see
[architecture.md](architecture.md); for embedding mvm in a Go or C host program
see [`examples/`](../examples/).

## Install

```
go install github.com/mvm-sh/mvm@latest
```

Or run it straight from a clone of the repository with `go run .` in place of
`mvm` (all the examples below work either way).

## Commands

| Command   | What it does                                              |
|-----------|-----------------------------------------------------------|
| `run`     | run a Go source file, evaluate an expression, or start the REPL |
| `test`    | run `Test*` functions found in `*_test.go` files          |
| `version` | print the mvm version, Go version, and OS/arch            |
| `help`    | show the command list                                     |

`run` is the default command, so `mvm foo.go` is the same as `mvm run foo.go`.
Use `mvm <command> -h` for the flags of a command.

## run

```
mvm                                 # start the REPL
mvm run _samples/fib.go             # run a Go source file
mvm _samples/fib.go                 # same; "run" is the default
mvm run -e "fmt.Println(1+2)"       # evaluate an inline expression
mvm run -x _samples/fib.go          # run with line tracing (see below)
mvm github.com/mvm-sh/mvm/cmd/mvmlint .  # fetch and run a remote main package
```

- **Source file.** The file is read and executed like `go run`.
  A leading `#!` line is stripped, and the file may drop `package main` and
  `func main` -- see [Program forms](#program-forms).
  Arguments after the path are forwarded as the program's `os.Args`.
- **Import path.** An argument that looks like an import path (contains `/`,
  no `.go` suffix, no matching local file) is fetched as a package and its
  `func main` is run -- see [Remote imports](#remote-imports).
  Arguments after the import path are forwarded as the program's `os.Args`.
  If the package has no `func main`, mvm runs its inits and warns that there
  was nothing to run.
- **`-e <expr>`.** Evaluates a string of Go: an expression, a statement, or
  several separated by `;`.
  The bundled stdlib is auto-imported, so `fmt.Println(...)` resolves with no
  explicit `import` -- see [Program forms](#program-forms).
  The value of a trailing expression is not printed; call `fmt.Println` (or the
  builtin `print`) yourself for output.
- **No arguments.** Starts the interactive [REPL](#repl).
- **`-x`.** Enables execution tracing -- see [Execution tracing](#execution-tracing).

## Program forms

mvm runs ordinary Go, but relaxes a few requirements so short programs and
scripts stay short.

**Standard program.**
A file with `package main`, its `import`s, and a `func main` runs exactly as
`go run` would.

**Scripts.**
A leading `#!` line is stripped before parsing, so a file can be made
executable:

```sh
$ cat hello
#!/usr/bin/env mvm
import "fmt"

fmt.Println("hello, world")
$ chmod +x hello
$ ./hello
hello, world
```

**`package main` is optional.**
If the source does not start with a `package` clause, mvm parses it as if
`package main` were present.
An explicit `package main` is still accepted; any other package name is not
(mvm runs a single file or a directory, not a named library).

**`func main` is optional.**
Without a `func main`, mvm runs every `func init` body and then any bare
top-level statements, in source order -- so the script above works without
wrapping its body in `func main`.

**Auto-imported stdlib.**
With `-e`, in the REPL, and under `mvm test`, every bundled standard-library
package is pre-registered under its base name, so `fmt.Println(...)`,
`strings.Split(...)`, `time.Now()` and friends resolve with no `import` line.
Running a source *file* does not auto-import: a script must `import` the
packages it uses (it just need not declare `package main` or `func main`).
A handful of base names map to more than one bundled package; auto-import binds
them as follows, and an explicit `import` always overrides:

| Base name  | Auto-import binds to | Also reachable via explicit import |
|------------|----------------------|------------------------------------|
| `rand`     | `math/rand`          | `crypto/rand`, `math/rand/v2`      |
| `template` | `text/template`      | `html/template`                    |
| `scanner`  | `text/scanner`       | `go/scanner`                       |
| `pprof`    | `runtime/pprof`      | `net/http/pprof`                   |

Any other base-name collision resolves to the import path with the fewest
segments, ties broken alphabetically.

## REPL

Run `mvm` with no arguments (or `mvm run`) to start an interactive
read-eval-print loop:

```
$ mvm
> x := 21
:  21
> x * 2
:  42
> import "strings"
> strings.ToUpper("go")
:  GO
>
```

A few things worth knowing:

- **It is line-oriented and unadorned.**
  Input is read with a plain line scanner: no history, no arrow-key editing, no
  tab completion.
  You can still feed it a session on stdin (`mvm < session.txt`).
- **Incomplete input continues on the next line.**
  An unbalanced `(`, `{`, or `[`, or an unterminated string or raw string,
  switches the prompt to `>>` and keeps reading until the construct closes; a
  complete line is evaluated immediately.
- **State persists across lines.**
  Variables, functions, types, and imports declared on one line stay in scope
  on the following lines -- the session is compiled and run incrementally, not
  restarted per line.
  `x := 1` is accepted at the prompt even though Go forbids `:=` at file scope.
- **The value of the line is echoed** after `: ` -- an expression shows its
  value; some lines (a `type` or `import` declaration) leave nothing and print
  nothing.
- **`package main` and `func main` are not needed** (see
  [Program forms](#program-forms)); a stray `package` line is parsed like any
  other line and is otherwise a no-op.
- **Errors do not end the session.**
  A parse or runtime error prints `Error: <message>`, discards whatever input
  had accumulated, and returns to the `>` prompt; only end-of-input (Ctrl-D)
  stops the REPL.
- **Auto-import is on**, so the one-liners in [Tips](#tips) work pasted straight
  at the prompt.

## test

```
mvm test                            # run tests in the current directory
mvm test ./pkg                      # run tests in a local package directory
mvm test github.com/google/uuid     # fetch a remote module and run its tests
mvm test ./pkg -v                   # verbose output
mvm test ./pkg -run TestFoo         # run only matching tests
mvm test -v                         # current directory, verbose
```

The target is either:

- **A local directory** (default `.`). Every `*.go` file in the directory is
  loaded; there must be at least one `*_test.go` file.
- **An import path** such as `github.com/google/uuid`. The module is fetched
  through the Go module proxy and held in memory -- see [Remote imports](#remote-imports).
  Its package is loaded as a whole so cross-file references resolve.

Test flags use the same names as `go test` (`-v`, `-run REGEX`, `-count N`,
`-short`, ...); mvm adds the `-test.` prefix `testing.Main` expects before
running. They follow the target (or stand alone when the target is omitted), so
the target, when given, comes first: `mvm test ./pkg -run TestFoo`, not
`mvm test -run TestFoo ./pkg`. Tests run in source-declaration order, not
alphabetical order.

`-x` enables execution tracing here too.

## Execution tracing

The `-x` flag (on `run` and `test`) and the `MVM_TRACE` environment variable
turn on a per-instruction trace printed to stderr. Both accept the same comma-
separated mode tokens:

| Want            | `-x` form                  | `MVM_TRACE` form          |
|-----------------|----------------------------|---------------------------|
| line tracing    | `-x`, `-x=line`            | `MVM_TRACE=1`, `MVM_TRACE=line` |
| bytecode tracing| `-x=op`, `-x=bytecode`     | `MVM_TRACE=op`            |
| both            | `-x=all`, `-x=line,op`     | `MVM_TRACE=all`           |

Tracing has effectively no cost when off: the VM hoists the trace state into a
register and the hot loop checks it with a single compare. See the
[Tracing](architecture.md#tracing) note in the architecture doc.

### Line tracing

One line per executed source line: `+ <file>:<line>: <source text>`. Consecutive
hits at the same position are deduplicated, and the prefix is indented by call
depth.

```
$ mvm run -x _samples/fib.go
+ _samples/fib.go:3: func fib(i int) int {
+ _samples/fib.go:10: func main() {
+ _samples/fib.go:11: 	println(fib(10))
+   _samples/fib.go:4: 	if i < 2 {
+   _samples/fib.go:7: 	return fib(i-2) + fib(i-1)
+     _samples/fib.go:4: 	if i < 2 {
+     _samples/fib.go:7: 	return fib(i-2) + fib(i-1)
...
```

### Bytecode tracing

One line per executed instruction: `+ [ip:.. sp:.. fp:..] [opcode operand] [top
of stack]`, where `ip` is the instruction pointer, `sp` the stack pointer, `fp`
the current frame pointer, and the trailing list is a snapshot of the top stack
slots.

```
$ mvm run -x=op -e "1+2"
+ [ip:0    sp:-1  fp:0  ]  [Push 1          ]  []
+ [ip:1    sp:0   fp:0  ]  [AddIntImm 2     ]  [0:1]
+ [ip:2    sp:0   fp:0  ]  [Exit            ]  [0:3]
```

## Interactive debugger: trap()

`trap()` is a builtin (no import needed). When the VM reaches it, execution
pauses and drops into an interactive prompt on stderr:

```
$ cat /tmp/t.go
package main

func main() {
	x := 1
	trap()
	_ = x
}
$ mvm run /tmp/t.go
trap at ip=7 (/tmp/t.go:5:6)
debug> help
  stack, bt  - dump call stack
  cont, c    - continue execution
  help, h    - show this help
debug> stack
=== Call Stack ===
--- Frame fp=4 ... (main) ---
  ...
debug> cont
```

Commands at the `debug>` prompt:

| Command       | Action               |
|---------------|----------------------|
| `stack`, `bt` | dump the call stack and memory |
| `cont`, `c`   | resume execution     |
| `help`, `h`   | show this list       |

Debug info (symbol names, source positions) is built lazily on the first
`trap()`, so programs that never call it pay nothing. See
[vm.md](modules/vm.md#trap-and-interactive-debug-mode) for the implementation.

## Remote imports

`run` and `test` accept a package import path where they would otherwise take a
file or directory, and an `import` statement inside interpreted code -- a file,
an `-e` string, or a REPL line -- may name a package that lives in another
module.
Either way mvm resolves the path through the Go module proxy and keeps the
fetched sources in memory; nothing is written to disk, and there is no `go.sum`
verification step (mvm trusts whatever the proxy returns).
For `run`, the import path must name a `main` package; mvm runs its `func main`
and forwards any trailing arguments as the program's `os.Args`.

```
mvm test github.com/google/uuid                              # whole package, run its tests
mvm github.com/mvm-sh/mvm/cmd/mvmlint .                      # fetch and run a remote main package
mvm run -e 'import "github.com/google/uuid"; println(uuid.NewString())'   # pull a dependency on the fly
```

**Versions.**
There is no `path@version` syntax.
mvm always resolves a module to its `@latest` version -- the same query
`go get <path>` makes when you give no version.
A package import path is mapped to its module by probing path prefixes
shortest-first against the proxy and taking the first one that resolves, so
`github.com/google/uuid` selects the module `github.com/google/uuid`, and
`github.com/foo/bar/sub/pkg` selects whichever of `github.com/foo/bar`,
`github.com/foo/bar/sub`, ... the proxy serves.

**`GOPROXY`.**
The `GOPROXY` environment variable is honored with (mostly) the usual Go
semantics:

- unset or empty: use the default public proxy, `https://proxy.golang.org`
- `off` or `direct`: offline -- no network fetches at all (mvm has no
  direct-from-VCS path); the embedded standard library still resolves
- a comma- or pipe-separated list: the *first* URL entry is used as the proxy;
  unlike `go`, mvm does not fall through to later entries on a miss

```
GOPROXY=off mvm test ./pkg                  # never touch the network
GOPROXY=https://goproxy.cn mvm run app.go   # use a mirror
```

## Environment variables

| Variable     | Effect                                                             |
|--------------|--------------------------------------------------------------------|
| `MVM_TRACE`  | enable tracing at startup; same tokens as `-x` (`1`/`line`, `op`/`bytecode`, `all`, comma list) |
| `MVM_DEBUG`  | any non-empty value enables the compiler's data/code dumps         |
| `GOPROXY`    | module proxy used for remote imports (see above)                   |
| `MVMSTD`     | internal: override the path to the embedded standard library source |

## Tips

A static file server in one line, using the bundled stdlib:

```sh
mvm -e 'http.ListenAndServe(":8080", http.FileServer(http.Dir(".")))'
```

`mvm test github.com/google/uuid` is a good sanity check that mvm runs real
third-party code, not just toy programs.

The repository ships [`_samples/`](../_samples/) (Go programs you can run
directly) and [`examples/`](../examples/) (embedding mvm in Go and C host
programs). For how the pipeline works under the hood, read
[architecture.md](architecture.md).
