package main

// Constant division/remainder fold at compile time; with a variable divisor the
// op stays at runtime. A zero constant divisor is deliberately NOT folded (the
// compiler would otherwise crash in go/constant) -- it is left to the runtime.

func main() {
	println(10 / 2) // folded
	println(10 % 3) // folded
	a, b := 17, 5
	println(a / b) // runtime
	println(a % b) // runtime
}

// Output:
// 5
// 1
// 3
// 2
