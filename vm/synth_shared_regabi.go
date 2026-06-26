//go:build !wasm

package vm

// synthSharedPC selects the per-signature stub-pool dispatch path: each
// interpreted method resolves to its own pool slot so native callers reach it.
const synthSharedPC = false
