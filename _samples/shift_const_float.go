package main

// Regression: `1<<52 - 0.5` (math's "largest fractional float64") was computed
// in int arithmetic, dropping the 0.5, because the constant shift result was
// marked a runtime Value rather than a Const. A shift of two constants is a
// constant, so mixed-constant arithmetic now promotes to float64.
func main() {
	println(1<<52 - 0.5)
	println(-1<<52 + 0.5)
}

// Output:
// 4.5035996273704955e+15
// -4.5035996273704955e+15
