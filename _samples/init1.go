package main

var a, b string

func init() {
	a = "first"
}

func init() {
	b = "second"
}

func main() {
	println(a, b)
}

// Output:
// first second
