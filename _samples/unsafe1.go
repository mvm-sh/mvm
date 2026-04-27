package main

import "unsafe"

func main() {
	var p unsafe.Pointer
	println(p == nil)
}

// Output:
// true
