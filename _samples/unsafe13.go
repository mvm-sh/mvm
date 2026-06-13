package main

// Edge cases for unsafe.Pointer(uintptr(p) +/- delta) pointer arithmetic.
// Elements of arr are contiguous, so &arr[i] +/- Sizeof(S) walks the slice.

import (
	"fmt"
	"unsafe"
)

type S struct {
	X int
	Y int
	Z int
}

func main() {
	arr := []S{
		{X: 10},
		{X: 20},
		{X: 30},
	}
	base := unsafe.Pointer(&arr[0])
	elem := unsafe.Sizeof(S{})

	// commutative: delta + uintptr(p)
	a := *(*S)(unsafe.Pointer(elem + uintptr(base)))
	fmt.Println(a.X)

	// multi-term: uintptr(p) + a + b
	b := *(*S)(unsafe.Pointer(uintptr(base) + elem + elem))
	fmt.Println(b.X)

	// subtraction: uintptr(p) - delta, last element back to the middle
	last := unsafe.Pointer(&arr[2])
	c := *(*S)(unsafe.Pointer(uintptr(last) - elem))
	fmt.Println(c.X)

	// pointer - pointer is a plain distance, never a rebased pointer
	d := uintptr(unsafe.Pointer(&arr[2])) - uintptr(unsafe.Pointer(&arr[0]))
	fmt.Println(d == 2*elem)

	// stored then converted: provenance need not survive a local round-trip
	u := uintptr(base)
	e := *(*S)(unsafe.Pointer(u + elem))
	fmt.Println(e.X)
}

// Output:
// 20
// 30
// 20
// true
// 20
