package main

import "fmt"

// Fn is a named func type with a String method (the fmt_test.Fn pattern).
type Fn func() int

func (fn Fn) String() string { return "String(fn)" }

var fnValue Fn

func main() {
	fmt.Printf("%v\n", fnValue)
	fmt.Printf("%#v\n", fnValue)
	fmt.Printf("%v\n", Fn(func() int { return 1 }))
}

// Output:
// String(fn)
// (main.Fn)(nil)
// String(fn)
