package main

import "iter"

func pairs() iter.Seq2[string, int] {
	return func(yield func(string, int) bool) {
		if !yield("a", 1) {
			return
		}
		if !yield("b", 2) {
			return
		}
	}
}

func main() {
	// One iteration variable over a Seq2 binds only the key.
	for k := range pairs() {
		println("key:", k)
	}
	// Zero iteration variables over a Seq2 still drives the iterator.
	n := 0
	for range pairs() {
		n++
	}
	println("count:", n)
}

// Output:
// key: a
// key: b
// count: 2
