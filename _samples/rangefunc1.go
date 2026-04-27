package main

func count(n int) func(yield func(int) bool) {
	return func(yield func(int) bool) {
		for i := 0; i < n; i++ {
			if !yield(i) {
				return
			}
		}
	}
}

func main() {
	sum := 0
	for v := range count(5) {
		sum += v
	}
	println(sum)
}

// Output:
// 10
