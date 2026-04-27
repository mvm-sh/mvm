package main

import "unsafe"

func main() {
	a := [5]int{1, 2, 3, 4, 5}
	s := unsafe.Slice(&a[0], 5)
	sum := 0
	for _, v := range s {
		sum += v
	}
	println(len(s), sum)
}

// Output:
// 5 15
