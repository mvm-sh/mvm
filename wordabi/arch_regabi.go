//go:build !wasm

package wordabi

// WordABI0 selects the stack-ABI (ABI0) word classifier and marshaling. It is
// false on the register-ABI targets, so ClassifyWordSig uses the register-word
// path (regabi.go) and the ABI0 path is dead code the compiler eliminates.
const WordABI0 = false
