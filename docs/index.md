# Mvm Documentation

Mvm is an experimental Go interpreter built from a pipeline of composable packages.

## Architecture

- [Architecture Overview](architecture.md) -- pipeline design, data flow, and key design decisions

## Module Reference

- [scan](modules/scan.md) -- language-independent lexical scanner
- [lang](modules/lang.md) -- token types and language specification
- [goparser](modules/goparser.md) -- Go parser producing flat token stream (no AST)
- [symbol](modules/symbol.md) -- scoped symbol table
- [comp](modules/comp.md) -- bytecode compiler with peephole optimization
- [vm](modules/vm.md) -- stack-based bytecode virtual machine
- [interp](modules/interp.md) -- integration layer and REPL
- [stdlib](modules/stdlib.md) -- standard library wrappers for native Go imports
- [cmd/extract](modules/extract.md) -- generator for stdlib binding files

## Architecture Decision Records

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
