package main

import "unsafe"

type T struct {
	a byte
	b uint64
}

var d T

var (
	b1 [unsafe.Sizeof(d)]byte        // bare ident (var)
	b2 [unsafe.Sizeof(T{})]byte      // composite literal
	b3 [unsafe.Sizeof(d.b)]byte      // field selector
	b4 [unsafe.Alignof(T{})]byte     // Alignof composite
	b5 [unsafe.Sizeof(int32(0))]byte // type-conversion call (existing path)
)

func main() {
	println(len(b1), len(b2), len(b3), len(b4), len(b5))
}

// Output:
// 16 16 8 8 4
