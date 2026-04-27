package main

type B[T any] struct {
	data T
}

type A struct {
	*B[string]
}

type C struct {
	B[string]
}

type Set[T any] int

type X struct {
	Set int
}

func main() {
	a := &A{B: &B[string]{data: "hi"}}
	println(a.data, a.B.data)

	c := C{B: B[string]{data: "ho"}}
	println(c.data)

	x := X{Set: 5}
	println(x.Set)
}

// Output:
// hi hi
// ho
// 5
