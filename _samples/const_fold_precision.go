package main

import "math"

// math.Pi is a ~60-digit untyped constant. Go folds `100000 * Pi` in arbitrary
// precision and rounds once when converted to float64, giving exactly
// 314159.26535897935. mvm now matches: cmd/extract records Pi's exact value
// (stdlib.ConstValues) and the compiler folds the multiply in arbitrary
// precision before rounding. Bridging Pi as a plain float64 and multiplying at
// runtime instead lands 1 ulp lower, which is what made math's
// TestLarge{Cos,Sin,Sincos,Tan} fail.
//
// (A pure *constant* comparison `100000*math.Pi == 314159.26535897935` stays in
// arbitrary precision and is false in Go too; the float64 rounding below is the
// behaviour math depends on.)
func main() {
	large := float64(100000 * math.Pi)
	println(large == 314159.26535897935)
}

// Output:
// true
