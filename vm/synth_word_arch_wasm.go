//go:build wasm

package vm

// wordABI0 selects the stack-ABI (ABI0) word classifier and marshaling. Go's
// wasm target passes args/results in contiguous Go-stack memory, not registers,
// so classifyWordSig/makeWordCore use the ABI0 path (synth_word_abi0.go) and the
// register-word path is dead code the compiler eliminates.
const wordABI0 = true
