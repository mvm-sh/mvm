package main

import "unsafe"

func main() {
	s := []int{7, 8, 9}
	p := unsafe.SliceData(s)
	println(*p)

	str := "go"
	bp := unsafe.StringData(str)
	println(*bp)
}

// Output:
// 7
// 103
