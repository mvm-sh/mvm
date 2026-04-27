package main

func naturals() func(yield func(int) bool) {
	return func(yield func(int) bool) {
		for i := 0; ; i++ {
			if !yield(i) {
				return
			}
		}
	}
}

func main() {
	sum := 0
	for v := range naturals() {
		if v >= 5 {
			break
		}
		sum += v
	}
	println(sum)
}

// Output:
// 10
