# ADR-017: Synthetic `std` module + stdlib redirect FS

**Status:** accepted
**Date:** 2026-05-09

## Context

A subset of Go's standard library has to be interpreted as source rather
than bridged natively (generic-first packages, packages whose semantics
the user wants the interpreter to track upstream byte-for-byte, packages
where bridges would lose interpreted-method dispatch). Until now, that
source lived in mvm itself: a hand-pruned tree at `stdlib/src/{cmp, iter,
maps, slices}/*.go`, exposed through `stdlib.SrcFS()` via `//go:embed
src` and plugged into the parser as the second-tier FS fallback.

That layout had three drawbacks:

- **Two source formats to maintain.** Embedded stdlib used a loose-file
  `embed.FS`; remote modules (ADR-014) used Go-proxy `<modPath>@<version>/`
  zips parsed by `modfs`. Two layouts, two code paths.
- **No versioning.** Fixing or extending an interpreted stdlib package
  required rebuilding mvm. Forks could not pin or swap the source set.
- **No way to commit local adaptations.** When upstream code reaches
  into `internal/race`, `//go:linkname` runtime entry points, or other
  primitives mvm can't provide (the `iter.Pull`/`Pull2` case), the only
  options were to extend mvm or to maintain a divergent hand-pruned
  copy in-tree. The first is sometimes massive work for one API; the
  second was the status quo and degraded under upstream churn.

## Decision

Publish the interpreted-stdlib subset as a **separate Go module**,
`github.com/mvm-sh/std`, hosted on its own GitHub repo and served by
`proxy.golang.org` like any other module. mvm consumes it through one
unified pipeline (`modfs`) along three paths:

1. **Embedded offline floor.** `stdlib/gen_stdzip.go` packs the std repo
   into a Go-proxy-format zip (`stdlib/src.zip`). `stdlib/srcfs.go`
   embeds it via `//go:embed`. At startup, `stdlib/stdmod.DefaultFS()`
   builds an offline-only `*modfs.FS`, injects the zip via the new
   `modfs.FS.Inject` method, and wraps it in a redirect FS. Stdlib
   resolution becomes a normal `modfs` lookup against an in-memory
   module.
2. **Network extension.** `main.go`'s `wireFS` builds a network-capable
   `*modfs.FS` (honoring `GOPROXY`), injects the same embedded zip, and
   wires the same instance as both the parser's stdlib FS (via the
   redirect) and the remote FS (for third-party imports). One cache
   serves both.
3. **Override.** `MVMSTD=<modpath>@<version>` swaps the active std
   module path and/or version, allowing forks or pinned snapshots
   without rebuilding mvm.

### Stdlib pattern detection

`stdmod.IsStdlibImport(path)` returns true when the import path's first
segment contains no dot, matching what `cmd/go` uses to distinguish
stdlib from module imports. The redirect FS rewrites such paths to
`<ModulePath>/<path>` and delegates to the backing modfs; everything
else returns `fs.ErrNotExist` so the parser falls through.

Native bridges still win: the parser checks `Packages[importPath]`
before consulting any FS (`goparser/decl.go`), so packages registered
in `stdlib/core` or `stdlib/ext` continue to use their pre-compiled
bindings even if the std module also publishes them. The choice of
which packages live in the std module is therefore a curation decision,
not a shadowing concern.

### Local patching of upstream packages

The std repo's `Makefile` syncs curated upstream packages from
`$GOROOT/src` and overlays mvm-specific patches from `patches/<pkg>/`
(full-file `*.go` overrides + `.delete` lists). The canonical example
is `iter`: upstream's `iter.go` pulls `internal/race`, `runtime`
linkname'd `newcoro`/`coroswitch`, and `unsafe` for `Pull`/`Pull2`,
none of which mvm can support. The patch keeps `Seq` and `Seq2` and
drops the rest. Patching is **stdlib-only**: third-party modules
fetched through the proxy are never patched -- mvm adapts to them via
native bridges or interpreter fixes, the same posture as before.

### Embedded zip is curated

The zip excludes test files (`*_test.go`, `example_test.go`) and repo
scaffolding (`.git`, `Makefile`, `README.md`, `patches/`). It carries
only the `*.go` implementation files plus `go.mod` and `LICENSE` -- the
shape `proxy.golang.org` would serve. Tests stay reachable through a
live clone or a real proxy fetch when running `mvm test <pkg>`.

## Consequences

**Easier:**

- **One source pipeline.** The embedded path and the network path go
  through the same `modfs` cache, the same zip parser, the same
  resolver. Bug fixes apply uniformly.
- **Versioned stdlib.** The std module can ship fixes and additions
  independently of mvm releases. Forks can pin or replace via
  `MVMSTD`. `proxy.golang.org` caches tags immutably so users on
  different machines get identical bytes.
- **Offline floor + network upgrade in one cache.** When `MVMSTD`
  points to a tagged version, modfs falls through from the embedded
  copy to the proxy without a separate code path.
- **Local adaptations live where they belong.** Stdlib source needing
  mvm-specific surgery is patched in the std repo, not by maintaining
  a parallel tree in mvm. Patches are reviewable as their own files
  and survive Go upgrades better than line-anchored diffs.

**Harder / weaker:**

- **`stdlib/src.zip` is a committed binary blob.** It is `//go:embed`-ed
  by `stdlib/srcfs.go`, so the file must exist for `go build` to
  succeed; rather than make every clone (and `go install ...@latest`)
  regenerate it, the ~30 KB zip is checked in. It is regenerated from
  `../../std` by `make generate` and re-committed only when the std
  module changes -- a small, deterministic blob in git history in
  exchange for a tree that builds with no setup step.
- **`stdmod.Version` is a hand-bumped const.** Out-of-sync with the
  embedded zip (or with the published proxy tag) leads to silent
  cache-miss-then-fetch on overrides. Process discipline applies:
  bump after each std-repo retag.
- **`MVMSTD` overrides bypass the embedded zip.** That is the point
  -- the embedded zip is keyed by `(ModulePath, Version)` and a
  different override goes to the proxy -- but it means an offline
  user cannot test a forked version without first fetching it once.

## Alternatives considered

- **Keep the loose `stdlib/src/` tree.** Tractable but locks the
  source set to mvm's release cadence and leaves the
  embed.FS-vs-proxy-zip split in place.
- **Unified diff (`*.patch`) overrides for stdlib adaptation.** A
  450-line removal in `iter.go` becomes a 450-line patch; rebases on
  every Go upgrade. Full-file overlay scales better.
- **Fetch the std module from the proxy at runtime, no embedded zip.**
  Smaller binary but every cold start hits the network; impossible
  for the WASM playground without a CORS-friendly proxy. Embedded
  zip + proxy fallback is strictly more capable.

## See also

- [ADR-013: stdlib core/ext split](ADR-013-stdlib-core-ext-split.md) --
  bridge-tier curation, complementary to the std module.
- [ADR-014: dynamic network imports](ADR-014-dynamic-network-imports.md)
  -- modfs and the FS chain this builds on.
- [stdmod module reference](../modules/stdmod.md).
- [stdlib module reference](../modules/stdlib.md).
- [modfs module reference](../modules/modfs.md).
