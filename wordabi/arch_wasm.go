//go:build wasm

package wordabi

// WordABI0 selects the stack-ABI (ABI0) word classifier and marshaling. Go's
// wasm target passes args/results in contiguous Go-stack memory, not registers,
// so ClassifyWordSig uses the ABI0 path (abi0.go) and the register-word path is
// dead code the compiler eliminates.
const WordABI0 = true
