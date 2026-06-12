# Mvm Documentation

Mvm is an experimental Go interpreter built from a pipeline of composable
packages.

## Guides

- [Usage Guide](usage.md) -- CLI commands, execution tracing, the trap()
  debugger, remote imports, environment variables

## Module Reference

- [scan](modules/scan.md) -- language-independent lexical scanner
- [lang](modules/lang.md) -- token types and language specification
- [goparser](modules/goparser.md) -- Go parser producing flat token stream (no AST)
- [symbol](modules/symbol.md) -- scoped symbol table
- [comp](modules/comp.md) -- bytecode compiler with peephole optimization
- [vm](modules/vm.md) -- stack-based bytecode virtual machine
- [interp](modules/interp.md) -- integration layer and REPL
- [synth-types](modules/synth-types.md) -- cross-cutting: how interpreted types project onto real `reflect.Type`s and the invariants that keep them in sync
- [runtype](modules/runtype.md) -- synthesizes Go rtypes carrying interpreted methods for native dispatch
- [stdlib](modules/stdlib.md) -- standard library wrappers for native Go imports
- [stdlib/stubs](modules/stubs.md) -- method-shape catalog and dispatch-stub pools feeding runtype
- [stdmod](modules/stdmod.md) -- redirect FS that routes stdlib imports through the
  synthetic `github.com/mvm-sh/std` module
- [modfs](modules/modfs.md) -- in-memory `fs.FS` over the Go module proxy for dynamic network imports
- [cmd/extract](modules/extract.md) -- generator for stdlib binding files
- [cmd/mvmlint](modules/mvmlint.md) -- project-specific source linter built on mvm's own scanner

## Architecture

- [Architecture Overview](architecture.md) -- pipeline design, data flow, and
  key design decisions

Architecture Decision Records:

- [ADR-001: Flat token stream instead of AST](decisions/ADR-001-flat-token-stream.md)
- [ADR-002: Hybrid Value type](decisions/ADR-002-hybrid-value.md)
- [ADR-003: Scope as slash-separated path](decisions/ADR-003-scope-as-path.md)
- [ADR-004: Two-phase compilation with pre-allocated slots](decisions/ADR-004-lazy-fixpoint.md)
- [ADR-005: Per-type opcodes with immediate variants](decisions/ADR-005-per-type-opcodes.md)
- [ADR-006: Native Go function interop (WrapFunc / MvmFunc)](decisions/ADR-006-native-func-interop.md)
- [ADR-007: Super instructions and instruction fusion](decisions/ADR-007-super-instructions.md)
- [ADR-008: Goroutine and channel support](decisions/ADR-008-goroutines-and-channels.md)
- [ADR-009: Interface bridging for native Go calls](decisions/ADR-009-interface-bridging.md)
- [ADR-010: Compiler intrinsics for math and bit manipulation](decisions/ADR-010-intrinsics.md)
- [ADR-011: Generics via monomorphization](decisions/ADR-011-generics-monomorphization.md)
- [ADR-012: Package patchers and argument proxies](decisions/ADR-012-package-patchers-arg-proxies.md)
- [ADR-013: Split stdlib bindings into `core` and `ext`](decisions/ADR-013-stdlib-core-ext-split.md)
- [ADR-014: Dynamic network imports via Go module proxy](decisions/ADR-014-dynamic-network-imports.md)
- [ADR-015: Absolute token positions throughout the pipeline](decisions/ADR-015-absolute-token-positions.md)
- [ADR-016: Runtime introspection via *runtime.Func sentinels](decisions/ADR-016-runtime-introspection-bridge.md)
- [ADR-017: Synthetic `std` module + stdlib redirect FS](decisions/ADR-017-std-module-redirect.md)
- [ADR-018: Virtualized process exit via panic-based `ExitError`](decisions/ADR-018-virtualized-process-exit.md)
- [ADR-019: `mvm test` drives `testing.MainStart(...).Run()` directly](decisions/ADR-019-test-runner-mainstart-driver.md)
- [ADR-020: Type references resolved by identity slot, not by name](decisions/ADR-020-type-identity-slots.md)
- [ADR-021: Synthesized rtypes for native method dispatch](decisions/ADR-021-synthesized-rtypes.md) (supersedes ADR-009, part of ADR-012)
- [ADR-022: Word-class ABI register shapes for method dispatch](decisions/ADR-022-word-class-dispatch.md) (extends ADR-021)
