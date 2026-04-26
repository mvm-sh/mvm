# Mvm

Mvm is an experimental project to test a single parser to multiple
languages and virtual machines.

The first language definition is a subset of Go, enough to implement
simple benchmarks, as fibonacci numbers.

A byte-code based VM and the corresponding byte code generator are
provided.

Note: this is highly experimental and unstable.

## Usage

`go run . ./samples/fib`
