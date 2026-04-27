package main

import "example.com/pkg5"

var v string

func init() {
	v = pkg5.V + " main-initialized"
}

func main() {
	println(v)
}

// Output:
// pkg5-initialized main-initialized
