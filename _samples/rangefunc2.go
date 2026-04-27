package main

func enumerate(words []string) func(yield func(int, string) bool) {
	return func(yield func(int, string) bool) {
		for i, w := range words {
			if !yield(i, w) {
				return
			}
		}
	}
}

func main() {
	for i, w := range enumerate([]string{"a", "bb", "ccc"}) {
		println(i, w)
	}
}

// Output:
// 0 a
// 1 bb
// 2 ccc
