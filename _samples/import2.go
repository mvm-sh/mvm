package main

import "example.com/pkg2"

func main() {
	println(pkg2.W, pkg2.G())
}

// Output:
// hello world 4
