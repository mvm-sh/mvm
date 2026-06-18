package main

import "fmt"

// A compound assignment or ++/-- to an index target (`x[i] op= v`, `x[i]++`)
// must evaluate the index expression exactly once (Go spec).
var calls int

func next() int {
	calls++
	return calls % 3
}

func main() {
	// Slice target, ++.
	s := make([]int, 3)
	calls = 0
	s[next()]++
	fmt.Println("slice ++ calls:", calls) // 1, not 2

	// Map target, += (the wrr shape).
	m := map[int]int{}
	calls = 0
	m[next()] += 10
	fmt.Println("map += calls:", calls)

	// Repeated increments land on the right buckets: next() yields 1,2,0,1,2,0.
	c := make([]int, 3)
	calls = 0
	for i := 0; i < 6; i++ {
		c[next()]++
	}
	fmt.Println("histogram:", c) // [2 2 2], not skewed by double-eval
}

// Output:
// slice ++ calls: 1
// map += calls: 1
// histogram: [2 2 2]
