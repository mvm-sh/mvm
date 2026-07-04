//go:build wasm

package mtype

// reflect.StructOf's method promotion registers every promoted method's code
// PC in reflect's GC-scanned reflectOffs table (resolveReflectText), and wasm
// code PCs are plain integers >= 0x1000_0000 that alias heap addresses once
// the arena grows past them: the collector then throws "found bad pointer in
// Go heap" (or "found pointer to free object" from the sweeper's zombie
// check, which no GODEBUG disables). Demote method-bearing embeds to named
// fields before StructOf and re-flag the embedded bit on the clone; mvm
// promotes and dispatches embedded methods itself on wasm.
const demoteMethodEmbeds = true
