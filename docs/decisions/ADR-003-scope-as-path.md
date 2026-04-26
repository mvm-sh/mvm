# ADR-003: Scope as slash-separated path

**Status:** accepted
**Date:** 2024-01-15

## Context

The symbol table needs to support nested scopes (functions inside functions,
blocks inside loops, etc.) and allow lookup from inner scopes to outer ones.
Traditional approaches use a linked list of scope frames or a stack of hash
maps.

## Decision

Scopes are represented as slash-separated path strings (e.g.
`main/foo/for0/if1`). The symbol table is a single flat map keyed by
`scope/name` (e.g. `main/foo/x`).

Lookup for name `x` in scope `main/foo/for0` tries:
1. `main/foo/for0/x`
2. `main/foo/x`
3. `main/x`
4. `0/x` (builtins)

Scope `0` is reserved for builtin types and functions.

## Consequences

**Easier:**
- Single flat map -- no scope stack to manage.
- Scope path is a natural debug identifier (visible in symbol dumps).
- Label scoping (e.g. `for0`, `if1`) falls out naturally from the path.

**Harder:**
- Lookup requires string splitting and multiple map probes (though in
  practice scopes are shallow, typically 3-4 levels).
- Removing all symbols for a scope requires iterating the map (mitigated
  by `SymTracker` for rollback).
