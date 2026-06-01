package main

// (*Generic[T])(x) must parse as a pointer-type conversion, not a deref of the instantiated type.
// A generic type name has symbol kind Generic, not Type.

type Box[T any] struct{ v T }

func main() {
	b := &Box[int]{v: 5}
	p := (*Box[int])(b)
	println(p.v) // 5
}

// Output:
// 5
