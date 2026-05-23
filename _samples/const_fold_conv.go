package main

// Converting a numeric constant to a numeric type is itself a constant, so
// expressions built from typed conversions fold at compile time (e.g.
// `int32(7) * int32(6)` -> a single load). Non-constant conversions like
// `int32(x)` stay at runtime.

func main() {
	println(int32(7) * int32(6))
	println(float64(7) / 2)
	println(byte('A') + 1)
	println(uint64(1)<<40 + 5)
	x := 5
	println(int32(x) * 2) // non-const: computed at runtime
}

// Output:
// 42
// 3.5
// 66
// 1099511627781
// 10
