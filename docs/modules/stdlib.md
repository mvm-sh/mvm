# stdlib

> Standard library wrappers, interface bridges, and mvm-native shadow
> packages for importing native Go packages into mvm.

## Overview

The `stdlib` package is the bridge between mvm programs and the Go
standard library. It has three responsibilities:

1. **Wrap native Go symbols** so interpreted code can `import "fmt"` and
   call `fmt.Println` as if it were local.
2. **Register interface bridges** so interpreted values satisfy Go
   interfaces (`fmt.Stringer`, `io.Writer`, ...) at native call boundaries.
3. **Host mvm-native shadows and patchers** for stdlib packages whose
   reflection-based walking would otherwise bypass interpreted methods
   (currently `encoding/json` via `stdlib/jsonx`).

## Key types and functions

- **`Values`** (`map[string]map[string]reflect.Value`) -- the native-symbol
  registry. Outer key: package path (`"fmt"`). Inner key: symbol name
  (`"Println"`). Each value is a `reflect.Value` wrapping the Go function,
  variable, or type.
- **`PackagePatcher`** (`func(*vm.Machine, map[string]vm.Value)`) -- a
  callback that mutates a package's exported symbol map. Used to splice
  mvm-native types in place of stdlib ones.
- **`RegisterPackagePatcher(importPath, fn)`** -- append-only registration.
  Called from shadow packages' `init()`.
- **`PackagePatchers() map[string][]PackagePatcher`** -- patcher list,
  consulted once by `Interp.patchStdlibOverrides` on the first `Eval`.
- **`SrcFS() fs.FS`** -- filesystem rooted at the embedded `stdlib/src/`
  tree. Used by `goparser.Parser.stdlibfs` as a fallback when importing
  generics-first packages that cannot be reflected through
  `cmd/extract -gen`.

## Internal design

### Generated native bindings

Each supported package has its own source file (e.g. `fmt.go`). Most are
produced by `go run ./cmd/extract -gen <import-path> <src-dir> -o stdlib/<pkg>.go`
and contain only an `init()` that inserts entries into `Values`. As of
writing, 161 of 192 non-internal stdlib packages are generated; exclusions
(umbrella dirs, generics-only, build-tag-gated) are listed in `stdlib/gen.go`.

Generated files carry a "Code generated ... DO NOT EDIT" marker.
`make clean_generate` deletes any file matching that marker; hand-written
files (`unsafe.go`, `bridges.go`, `patcher.go`, `srcfs.go`) must not carry
it.

### Embedded generic-first packages (`stdlib/src/`)

`cmp`, `iter`, `maps`, and `slices` cannot be extracted as `reflect.Value`
entries because their generic functions never materialise until
instantiation. Instead, their upstream source is embedded under
`stdlib/src/<pkg>/<pkg>.go`. The interpreter sets `Parser.stdlibfs` to
`stdlib.SrcFS()` at construction; `ParseAll` falls back to it when
`pkgfs` does not contain the requested import path. Interpreted code
parses these packages through the normal generic-instantiation path.

### Hand-written bindings

- **`unsafe.go`** -- the `unsafe` pseudo-package cannot be extracted. It
  registers `Pointer`, `Sizeof`, `Alignof`, `Offsetof`, `Add`, `Slice`,
  `String`, `SliceData`, `StringData`. `Sizeof`/`Alignof`/`Offsetof` are
  intercepted at compile time in `goparser.evalConstExpr` (const contexts)
  and `comp.compileBuiltin` (runtime). `Slice`/`SliceData` are also
  intercepted to compute pointer-element-dependent result types. The
  stub implementations panic if reached at runtime.

### Interface bridges (`bridges.go`)

A bridge is a Go struct with `Fn func(...)` fields and pointer-receiver
methods that delegate to `Fn`. At native call boundaries the VM allocates
a bridge pointer with `Fn` set to a closure that re-enters the VM via
`CallFunc`. New bridges are added here (or in any compiled package
binding) without touching `vm/` or `comp/`.

Four bridge families, all consumed through registries declared in
`vm/bridge.go`:

#### Display bridges (`vm.Bridges` + `vm.DisplayBridges`)

Carry a `Val any` field holding the original concrete value. `Format`
implements `fmt.Formatter`: `%v`/`%s` print the bridge's display string,
other verbs (`%d`, `%x`, ...) format the concrete `Val` directly so
named numeric types still work with non-string verbs.

| Bridge | Method | Interface |
|--------|--------|-----------|
| `BridgeString` | `String() string` | `fmt.Stringer` |
| `BridgeError` | `Error() string` | `error` |
| `BridgeGoString` | `GoString() string` | `fmt.GoStringer` |

`DisplayBridges` is the subset applied when the target parameter is
`interface{}`/`any`. Behavioural bridges (Read, Write, Close, ...) are
excluded from that path because wrapping a value as `io.Writer` where
`any` was requested would change its identity without benefit.

#### Behavioural bridges (`vm.Bridges` only)

| Bridge | Method | Interface |
|--------|--------|-----------|
| `BridgeWrite` | `Write([]byte) (int, error)` | `io.Writer` |
| `BridgeRead` | `Read([]byte) (int, error)` | `io.Reader` |
| `BridgeClose` | `Close() error` | `io.Closer` |
| `BridgeWriteTo` | `WriteTo(io.Writer) (int64, error)` | `io.WriterTo` |
| `BridgeReadFrom` | `ReadFrom(io.Reader) (int64, error)` | `io.ReaderFrom` |
| `BridgeMarshalJSON` | `MarshalJSON() ([]byte, error)` | `json.Marshaler` |
| `BridgeUnmarshalJSON` | `UnmarshalJSON([]byte) error` | `json.Unmarshaler` |

#### Composite bridges (`vm.CompositeBridges`)

Keyed by sorted `[2]string` pair of method names. Preserve two
capabilities at once so internal type assertions in `io.Copy` and similar
functions still succeed through the bridge:

| Bridge | Methods | Used for |
|--------|---------|----------|
| `BridgeReaderWriterTo` | `Read` + `WriteTo` | reader with optional `WriteTo` fast path |
| `BridgeWriterReaderFrom` | `Write` + `ReadFrom` | writer with optional `ReadFrom` fast path |

#### Interface bridges (`vm.InterfaceBridges`)

Keyed by the native `reflect.Type` of the target interface. Implement
all methods of a multi-method interface in one bridge struct. Used when
the destination parameter's type is the interface itself (not `any`),
so the bridge can be typed precisely.

| Bridge | Interface |
|--------|-----------|
| `BridgeSortInterface` | `sort.Interface` (`Len`, `Less`, `Swap`) |
| `BridgeHeapInterface` | `heap.Interface` (embeds `BridgeSortInterface`, adds `Push`, `Pop`) |
| `BridgeFlagValue` | `flag.Value` (`String`, `Set`) |

#### `ValBridgeTypes`

Display bridges register themselves in `vm.ValBridgeTypes`. The VM's
`unbridgeValue` inspects a type-assertion target: if the runtime value
is a known bridge, it unwraps `.Val` so `x.(MyNamedInt)` still matches
after the value has passed through a display bridge.

### Package patchers (`patcher.go`)

```go
stdlib.RegisterPackagePatcher("encoding/json", patchEncodingJSON)
```

Patchers are consulted by `Interp.patchStdlibOverrides` on the first
`Eval`. Each registered patcher for an import path is called with the
live `*vm.Machine` and the package's `vm.Value` symbol map. Typical use:
overlay replacement types or factory functions into the map so
interpreted code resolves `json.Encoder` to the shadow implementation.

The registry is populated only from `init()` functions, so no locking is
required. See [ADR-012](../decisions/ADR-012-package-patchers-arg-proxies.md).

### mvm-native shadow packages

Shadows provide mvm-aware replacements for reflect-walking stdlib
packages where nested interpreted methods would otherwise be missed.

Currently one shadow ships: [`stdlib/jsonx`](#stdlibjsonx).

## stdlib/jsonx

mvm-aware implementation of the parts of `encoding/json` that need
to dispatch interpreted `MarshalJSON` / `UnmarshalJSON` methods on
nested struct fields.

**Scope.**

- `Marshal`, `MarshalIndent`, `Unmarshal`: dispatched via `vm.RegisterArgProxy`
  so their single data argument is wrapped as a proxy whose
  `MarshalJSON` / `UnmarshalJSON` method re-enters the jsonx walker.
- `Encoder`, `Decoder`: mvm-native types; `NewEncoder`/`NewDecoder`
  constructors are installed via the `encoding/json` package patcher.
  `Encode`/`Decode` are dispatched via `vm.RegisterArgProxyMethod`.

**Fallback.** Values whose mvm type is unknown (pure native) are
delegated to the upstream `encoding/json` implementation. Only values
carrying `vm.Iface` metadata trigger the walker.

**Walker.** `marshalValue` / `unmarshalValue` iterate `vm.Type.Fields`
and dispatch mvm methods via `Machine.MethodByName` +
`MakeMethodCallable`. Pointer receivers are handled by allocating via
`reflect.New` when the destination is nil. Struct tags (`json:"name"`,
`omitempty`, `-`) are respected.

**Adding new shadows.** The same pattern transfers to any reflect-walking
stdlib package:

1. Implement the walker using `vm.Type.Fields` + `Machine.CallFunc`.
2. Register arg proxies with `vm.RegisterArgProxy` /
   `RegisterArgProxyMethod` for top-level entry points.
3. Install a `PackagePatcher` if replacement types or constructors need
   to be spliced into the original package.

## Dependencies

- `reflect` -- wrapping Go values.
- `vm/` -- `Bridges`, `ValBridgeTypes`, `CompositeBridges`,
  `InterfaceBridges`, `RegisterArgProxy*`, `Machine`, `Value`, `Iface`,
  `Type`.
- `symbol/` -- `BinPkg` creates package descriptors (called by the parser
  via `ImportPackageValues`).

## Open questions / TODOs

- `encoding/xml`, `encoding/gob`, `text/template`: same reflect-walk
  issue as JSON. Each would need its own shadow to honour interpreted
  methods.
- `fmt.Print*` already route to the Machine's writer via
  `interp.patchFmtBindings`. Nested `fmt.Stringer` / `fmt.Formatter`
  methods on struct fields are not yet covered.
