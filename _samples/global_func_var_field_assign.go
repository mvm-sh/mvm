package main

// Reassigning a global func var from a struct field must update the global,
// not just a stack copy, so later calls dispatch the new func.
// (rs/zerolog ErrorMarshalFunc)

var fn = func(s string) string { return "orig:" + s }

func read(s string) string { return fn(s) }

type box struct {
	f func(s string) string
}

func main() {
	b := box{func(s string) string { return "new:" + s }}
	fn = b.f
	println(read("x"))
}

// Output:
// new:x
