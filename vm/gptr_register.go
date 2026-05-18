// Fast path: read the goroutine's *g pointer directly from the
// architecture-reserved register. Go 1.17+ on amd64 keeps g in R14, and
// the arm64 ABI keeps g in R28 (aliased as `g` in plan9 assembly). The
// returned pointer is unique per goroutine and stable for its lifetime,
// so it makes a sound per-goroutine map key. The cost is one register
// move plus a function call -- on the order of a nanosecond, vs.
// ~1us for the runtime.Stack-based fallback.
//
// This file applies to amd64 and arm64; see gptr_fallback.go for other
// architectures. The exported helper `gid()` returns the same value as
// uintptr so the map-key type is identical on every build.

//go:build amd64 || arm64

package vm

import "unsafe"

//go:noescape
func gptr() unsafe.Pointer

//go:nosplit
func gid() uintptr { return uintptr(gptr()) }
