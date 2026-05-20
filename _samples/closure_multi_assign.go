package main

// A multi-assignment (a, b = ...) to variables captured by a closure must write
// through the shared heap cells, like the single-assignment case already did.
// The closure also covers the swap form and named returns set via a deferred
// multi-assign (the recover-to-error idiom returning two values).

func swap() (a, b int) {
	defer func() { a, b = b, a }()
	return 1, 2
}

func main() {
	x, y := 1, 2
	f := func() { x, y = 10, 20 }
	f()
	println(x, y)

	a, b := swap()
	println(a, b)
}

// Output:
// 10 20
// 2 1
