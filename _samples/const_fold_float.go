package main

// Mixed int/float constant arithmetic folds in arbitrary precision and rounds
// once to float64, matching Go.

func main() {
	println(2.5 + 0.5)
	println(3.0 / 2) // untyped float division
	println(1.0 / 4)
	println(10 / 4.0) // int promoted to float by the float operand
}

// Output:
// 3
// 1.5
// 0.25
// 2.5
