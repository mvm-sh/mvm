package main

import "unsafe"

type valueT struct {
	a, b int64
	c    [4]byte
}

// unsafe.Sizeof / Alignof of an address-of expression: a pointer, whose size
// and alignment are the platform pointer width regardless of the pointee.
// Was modernc.org/sqlite: `const sqliteValPtrSize = unsafe.Sizeof(&sqlite3.Sqlite3_value{})`
// used in pointer arithmetic (vtab.go), which left the const untyped.
const (
	ptrSize  = unsafe.Sizeof(&valueT{})
	ptrAlign = unsafe.Alignof(&valueT{})
	valSize  = unsafe.Sizeof(valueT{})
)

func main() {
	var base uintptr = 100
	cols := base + uintptr(2)*ptrSize
	println(ptrSize, ptrAlign, valSize, cols)
}

// Output:
// 8 8 24 116
