package main

import "iter"

func squares(n int) iter.Seq[int] {
	return func(yield func(int) bool) {
		for i := 1; i <= n; i++ {
			if !yield(i * i) {
				return
			}
		}
	}
}

func main() {
	sum := 0
	for v := range squares(4) {
		sum += v
	}
	println(sum)
}

// Output:
// 30
