package main

func fib(i int) int {
	if i < 2 {
		return i
	}
	return fib(i-2) + fib(i-1)
}

func main() {
	println(fib(10))
}

// Output:
// 55
