package main

import "unsafe"

func main() {
	var i32 int32
	var i64 int64
	var s struct {
		a byte
		b int64
	}
	println(unsafe.Sizeof(i32), unsafe.Alignof(i32))
	println(unsafe.Sizeof(i64), unsafe.Alignof(i64))
	println(unsafe.Sizeof(s))
}

// Output:
// 4 4
// 8 8
// 16
