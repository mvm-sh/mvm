package main

// A captured (heap-cell) numeric variable whose value was written by a native
// call through its address (atomic.AddUint64(&count, ...)) must read back
// correctly as an arithmetic operand, not just as a direct argument. The native
// write updates the slot's reflect backing but not its cached num word, so
// CellGet/HeapGet re-sync num from ref for addressable numeric cells. Without
// the re-sync, `count + x` used a stale num (0) and dropped count -- the root of
// sync/atomic's TestValueSwapConcurrent failure (count + v.Load() == v.Load()).

import "sync/atomic"

func main() {
	var count uint64
	add := func(d uint64) { atomic.AddUint64(&count, d) }
	add(40)
	add(2)

	x := uint64(100)
	println(count)       // 42  (direct read, via ref)
	println(count + x)   // 142 (arithmetic operand, was 100)
	println(x + count)   // 142
	println(count == 42) // true
}

// Output:
// 42
// 142
// 142
// true
