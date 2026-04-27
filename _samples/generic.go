package main

func Max[T any](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func Min[T any](a, b T) T {
	if a < b {
		return a
	}
	return b
}

func main() {
	println(Max[int](3, 5))
	println(Max[string]("alpha", "beta"))
	println(Min[float64](1.5, 2.5))
	println(Min[int](3, 5))
}

// Output:
// 5
// beta
// 1.5
// 3
