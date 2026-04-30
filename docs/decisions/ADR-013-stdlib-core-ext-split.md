# ADR-013: Split stdlib bindings into `core` and `ext`

**Status:** accepted
**Date:** 2026-04-27

## Context

Mvm's pitch is "single static binary, full Go stdlib bundled in." That
bundling has a cost: every package wired into `stdlib.Values`
contributes its transitive Go dependencies to the final binary, even
if the embedding host never imports it from interpreted code.

Early on, all generated bindings lived in a single `stdlib/` package.
A blank-import (`_ "github.com/mvm-sh/mvm/stdlib"`) pulled in
everything: `net/http`, `crypto/tls`, `image/jpeg`, `runtime/pprof`,
the full `syscall` matrix. For embedders who want to run untrusted
interpreted Go in a tight binary — or in a `js/wasm` build, or in
a sandbox where syscalls are off-limits — that "all or nothing" choice
was wrong.

## Decision

Split generated bindings into two sub-packages plus a convenience
aggregator:

- **`stdlib/core/`** — pure-compute, browser-safe packages with modest
  transitive footprint: `fmt`, `bytes`, `strings`, `strconv`,
  `encoding/json`, `regexp`, `time`, `math/*`, the container types,
  `sync`, `sort`, etc. ~40 packages.
- **`stdlib/ext/`** — host-coupled or transitively heavy packages:
  `net/*`, `os/*`, `crypto/*` (except hashes), `image/*`,
  `runtime/*`, `database/sql`, `syscall/*` (per platform), `testing`,
  `text/template`, etc. ~170 files (counting per-platform syscall
  variants).
- **`stdlib/all/`** — blank-imports `core`, `ext`, and `jsonx`. The
  full bundle, kept as one line for embedders that want everything.

The split is data-driven: `cmd/extract/categories.go` carries a `Core`
map listing the import paths that route to `core/`. Anything not
listed goes to `ext/`. Adding a package to `core` is a one-line
change followed by `make generate`.

The criterion for `core` membership is roughly: *would this package
work in a `js/wasm` build with cgo disabled, with no system access,
and without dragging in net/crypto stack?* If yes, it belongs in
`core`. The list is reviewed by hand, not automated.

## Consequences

**Easier:**

- An embedder who needs only computational packages imports
  `stdlib/core` and gets a much smaller binary (no net/crypto/image
  link cost).
- `js/wasm` and similar restricted targets become viable without
  build-tag gymnastics on individual stdlib packages.
- The `core` set is small enough that contributors can scan it; new
  packages get a deliberate routing decision rather than ending up in
  one undifferentiated bucket.

**Harder:**

- Two import paths to register instead of one. Embedders who want
  everything must import `stdlib/all`, not just `stdlib`. The mvm CLI
  itself does this in `main.go`.
- The split criterion is a judgement call. There is no automated
  "transitive footprint" analysis; a package on the `core` list could
  silently grow heavy if upstream Go adds dependencies. Periodic
  manual review is implied.
- `stdlib/jsonx` is its own sub-package and not part of `core`/`ext`,
  even though `encoding/json` is in `core`. The shadow walker needs
  the patcher pattern, which is conceptually separate. `stdlib/all/`
  pulls all three.
