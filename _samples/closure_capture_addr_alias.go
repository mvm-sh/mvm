package main

// A local that is both captured by a closure and address-taken is ONE memory
// location: the closure's writes must be visible through the pointer (sync's
// testOncePanicWith reads *calls). setCell therefore writes the cell storage
// in place, and &captured compiles to AddrCell/AddrHeap aliasing it. A numeric
// snapshot read (CellGet) detaches its ref, so a defer running after `return n`
// must NOT leak into the already-captured return value.

import "fmt"

func helper(calls *int, f func()) {
	func() {
		defer func() { _ = recover() }()
		f()
	}()
	fmt.Println("via ptr:", *calls)
}

func snapshot() int {
	n := 0
	for range 2 {
		defer func() { n++ }()
	}
	return n // reads 0; the deferred increments happen after
}

func main() {
	calls := 0
	f := func() { calls++; panic("x") }
	helper(&calls, f)
	fmt.Println("direct:", calls)

	m := 0
	p := &m
	g := func() { m++ }
	g()
	fmt.Println("addr-then-closure:", *p, m)

	fmt.Println("snapshot:", snapshot())
}

// Output:
// via ptr: 1
// direct: 1
// addr-then-closure: 1 1
// snapshot: 0
