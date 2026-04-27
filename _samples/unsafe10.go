package main

import (
	"fmt"
	"math/bits"
	"unsafe"
)

const WSize = bits.UintSize / 8

type S struct {
	X int
	Y int
	Z int
}

func main() {
	arr := []S{
		{X: 1},
		{X: 2},
		{X: 3},
	}
	addr := unsafe.Pointer(&arr[0])
	s := *(*S)(unsafe.Pointer(uintptr(addr) + WSize*6))

	fmt.Println(s.X)
}

// skip: runtime checkptr rejects uintptr -> nsafe.Pointer under -race (VM reconstitutes the pointer from an integer, so Go's pointer-provenance tracking can't follow it). `go run . _samples/unsafe10.go` prints 3 correctly.
// Output:
// 3
