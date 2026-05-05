package main

import "example.com/methodorder"

func main() {
	b := &methodorder.B{}
	println(b.Call())
}

// Output:
// 42
