package main

import "unsafe"

func main() {
	a := [4]int32{10, 20, 30, 40}
	p := unsafe.Pointer(&a[0])
	q := unsafe.Add(p, unsafe.Sizeof(int32(0))*2)
	v := *(*int32)(q)
	println(v)
}

// Output:
// 30
