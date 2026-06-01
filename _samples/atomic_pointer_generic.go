package main

// atomic.Pointer[T] is generic, so it can't be reflect-bridged; stdlib provides it as a generic shim (atomic_shim.go).
// This exercises cross-package instantiation: the shim's unsafe.Pointer field and bridged-op method bodies must resolve against the shim's owning package, not main.

import "sync/atomic"

type Cfg struct{ n int }

func main() {
	var p atomic.Pointer[Cfg]
	println(p.Load() == nil) // true
	p.Store(&Cfg{7})
	println(p.Load().n) // 7
	old := p.Swap(&Cfg{9})
	println(old.n, p.Load().n)                                // 7 9
	println(p.CompareAndSwap(p.Load(), &Cfg{11}), p.Load().n) // true 11
	println(p.CompareAndSwap(old, &Cfg{99}), p.Load().n)      // false 11
}

// Output:
// true
// 7
// 7 9
// true 11
// false 11
