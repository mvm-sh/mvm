//go:build !wasm

package mtype

// Native code PCs live outside the GC arena, so reflect.StructOf's method
// promotion is harmless; keep it for native-boundary interface satisfaction.
const demoteMethodEmbeds = false
