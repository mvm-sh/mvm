package main

import (
	"example.com/pkg2"
	"example.com/pkg3"
)

func main() {
	println(pkg2.W, pkg3.H())
}

// Output:
// hello world hello!
