package main

import "unsafe"

func main() {
	x := 42
	p := unsafe.Pointer(&x)
	q := (*int)(p)
	println(*q)
}

// Output:
// 42
