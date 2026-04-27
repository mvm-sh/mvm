package main

import "unsafe"

func main() {
	b := [5]byte{'h', 'e', 'l', 'l', 'o'}
	s := unsafe.String(&b[0], 5)
	println(s, len(s))
}

// Output:
// hello 5
