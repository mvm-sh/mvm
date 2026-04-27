package main

import "cmp"

func main() {
	println(cmp.Less(3, 5))
	println(cmp.Less[string]("alpha", "beta"))
	println(cmp.Compare(1.5, 2.5))
	println(cmp.Or("", "", "hi"))
}

// Output:
// true
// true
// -1
// hi
