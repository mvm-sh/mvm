package main

// Constant comparisons fold to a boolean constant at compile time.

func main() {
	const ok = 3 < 5
	println(ok)
	if 1 == 1 {
		println("eq")
	}
	if 2 != 3 {
		println("ne")
	}
	println(10 >= 10)
	println(4 <= 3)
}

// Output:
// true
// eq
// ne
// true
// false
