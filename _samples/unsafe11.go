package main

import "unsafe"

type inner struct {
	a int32
	b int32
}

type outer struct {
	pad byte
	_   [7]byte
	in  inner
	tag int
}

type A struct {
	_ [4]byte
	X int32
}

type B struct {
	pad int32
	A
	Y int32
}

func main() {
	var x outer
	o1 := unsafe.Offsetof(x.in)
	o2 := unsafe.Offsetof(x.tag)
	o3 := unsafe.Offsetof(x.in.b)

	var b B
	p1 := unsafe.Offsetof(b.X)   // promoted: 4 (A) + 4 (X in A) = 8
	p2 := unsafe.Offsetof(b.A.X) // explicit chain: offset of X in A = 4
	p3 := unsafe.Offsetof(b.Y)

	println(o1, o2, o3, p1, p2, p3)
}

// Output:
// 8 16 4 8 4 12
