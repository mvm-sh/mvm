package main

import "fmt"

func g() (int, bool) { return 9, true }

func f(x int) int {
	x, ok := g()
	_ = ok
	return x
}

func main() { fmt.Println(f(1)) }

// Output:
// 9
