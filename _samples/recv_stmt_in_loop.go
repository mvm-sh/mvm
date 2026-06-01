package main

// A bare receive statement (<-ch) must drop its received value.
// Otherwise it leaks an operand-stack slot per iteration and corrupts an enclosing range's Pull2 iterator.

var m = map[string]int{"a": 1, "b": 2, "c": 3}

func main() {
	ch := make(chan int, 1)
	n := 0
	for k := range m {
		_ = k
		ch <- 1
		<-ch
		n++
	}
	println(n) // 3
}

// Output:
// 3
