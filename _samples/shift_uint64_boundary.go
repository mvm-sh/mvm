package main

// Regression: 1<<63 is the untyped constant +2**63 (fits uint64, not int64).
// The compiler emitted a runtime int64 shift (bits 0x8000...) typed int, so
// float64(1<<63) read it as the negative -2**63. It is now typed uint64, so
// float and uint contexts read the positive value, while -1<<63 (which fits
// int64) stays signed.
func main() {
	println(float64(1 << 63))
	println(float64(-1 << 63))
	var u uint64 = 1 << 63
	println(u)
}

// Output:
// 9.223372036854776e+18
// -9.223372036854776e+18
// 9223372036854775808
