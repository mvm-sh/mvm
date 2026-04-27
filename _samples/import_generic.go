package main

import "example.com/pkg6"

func main() {
	// Explicit type arguments with comparable constraint.
	println(pkg6.Max[int](3, 5))
	println(pkg6.Max[string]("alpha", "beta"))

	// Implicit type inference.
	println(pkg6.Id(42))
	println(pkg6.Id("hello"))

	// Generic type instantiation.
	b := pkg6.Box[int]{Value: 7}
	println(b.Value)
}

// Output:
// 5
// beta
// 42
// hello
// 7
