//go:build !wasm

package vm

// wordABI0 selects the stack-ABI (ABI0) word classifier and marshaling. It is
// false on the register-ABI targets, so classifyWordSig/makeWordCore use the
// register-word path (synth_word_regabi.go) and the ABI0 path is dead code the
// compiler eliminates.
const wordABI0 = false
