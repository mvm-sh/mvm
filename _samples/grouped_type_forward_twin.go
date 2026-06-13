package main

// Regression: Item defers on Dur (a non-struct forward type), forcing the fixpoint
// to re-parse the whole group; Box must keep one *Type identity across that re-parse.
// Without the fix a twin Box is minted, Consumer captures the original, and the
// assertion below sees two distinct *Box and fails.

type (
	Item struct{ d Dur }
	Box  struct{ v int }
)

type Consumer struct{ b Box }

type Dur int

func main() {
	var c Consumer
	c.b = Box{v: 7}
	var i any = c.b
	bb, ok := i.(Box)
	if !ok {
		panic("Box assertion failed: grouped-decl twin *Type")
	}
	println(bb.v)
}

// Output:
// 7
