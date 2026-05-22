package main

// Regression: an untyped shift constant wider than 64 bits used in a float
// context (e.g. 1<<120 = 2**120) was evaluated as a runtime int64 shift and
// overflowed to 0. The compiler now folds such shifts to a float64 constant.
// Mirrors math's huge_test.go trigHuge table.
var trigHuge = []float64{
	1 << 28,
	1 << 120,
	1234567891234567 << 300,
}

func main() {
	println(trigHuge[0] == 268435456)
	println(trigHuge[1] > 1.3e36 && trigHuge[1] < 1.4e36)
	println(trigHuge[2] > 2.5e105 && trigHuge[2] < 2.6e105)
}

// Output:
// true
// true
// true
