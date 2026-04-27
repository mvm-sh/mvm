package main

import "unsafe"

const (
	Word    = unsafe.Sizeof(uintptr(0))
	IntSize = unsafe.Sizeof(int(0))
	Align8  = unsafe.Alignof(int64(0))
)

func main() {
	var a [IntSize]byte
	println(Word, IntSize, Align8, len(a))
}

// Output:
// 8 8 8 8
