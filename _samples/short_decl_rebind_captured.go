package main

import "fmt"

func ptr(n int) (*int, error) { return &n, nil }

// A closure captures sc, then `sc, err :=` rebinds it. Go reuses the existing
// binding, so the closure must observe the post-rebind value. A fresh heap
// cell on the rebind would orphan the capture (closure keeps seeing the old
// value). This is the bug that made grpc client RPCs hang: a balancer's
// subconn-state map key was set from a var the closure had captured before a
// `:=` rebind, so lookups never matched.
func localRebind() bool {
	var sc *int
	f := func() bool { return sc == nil }
	sc, err := ptr(1)
	_ = err
	return f() // false: closure sees the rebound non-nil sc
}

// Same, but the captured/rebound variable is a parameter, and the closure
// reads through it so an orphaned cell is observable by value.
func paramRebind(sc *int) int {
	f := func() int { return *sc }
	sc, err := ptr(42)
	_ = err
	return f() // 42: closure sees the rebound pointer, not the original
}

func main() {
	fmt.Println(localRebind(), paramRebind(ptrZero()))
}

func ptrZero() *int { z := 0; return &z }

// Output:
// false 42
