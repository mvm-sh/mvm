// Fallback path for architectures without an inline register-read for
// the goroutine pointer: derive a stable per-goroutine identifier by
// parsing the leading "goroutine N [state]:" line from runtime.Stack.
// Slow (~1us, since runtime.Stack does a full traceback) but correct on
// every supported architecture.

//go:build !amd64 && !arm64

package vm

import (
	"runtime"
	"sync"
)

// goidBufPool amortizes the heap allocation that runtime.Stack's
// []byte parameter forces (the slice header escapes regardless of
// whether the underlying array is stack-local, so each call to a fresh
// `var buf [128]byte; runtime.Stack(buf[:], ...)` produces one 128-byte
// allocation). Reusing the buffer across calls drops that to amortized
// zero allocs.
var goidBufPool = sync.Pool{
	New: func() any { var b [128]byte; return &b },
}

// goid returns the current goroutine's ID by parsing the leading
// "goroutine N [state]:" line from runtime.Stack. buf is sized at
// 128 bytes -- comfortably larger than any realistic
// "goroutine N [<state>]:" header, so the guard against a truncated
// header collapsing every goroutine onto id 0 is unreachable in
// practice but kept for defensiveness.
func goid() int64 {
	bp := goidBufPool.Get().(*[128]byte)
	defer goidBufPool.Put(bp)
	n := runtime.Stack(bp[:], false)
	const prefix = "goroutine "
	if n < len(prefix)+1 {
		return 0
	}
	s := bp[len(prefix):n]
	var id int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + int64(c-'0')
	}
	return id
}

func gid() uintptr { return uintptr(goid()) }
