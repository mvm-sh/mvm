//go:build wasm

package vm

// synthSharedPC routes every interpreted method to one shared stub PC on wasm.
// The all-interpreted wasm target has no native caller that dispatches an
// interpreted method through an itab or native-internal reflect: interpreted
// code uses IfaceCall, and interpreted reflect is intercepted (reflectValueShim).
// So the PC is never invoked -- it exists only so the synth rtype carries the
// method set for reflect introspection (Implements/NumMethod/MethodByName).
// This lets the wasm build drop the ~53k per-signature stub functions.
const synthSharedPC = true
