package main

import (
	"fmt"
	"iter"
	"maps"
	"slices"
)

func gen() iter.Seq[int] {
	return func(yield func(int) bool) { yield(1); yield(2); yield(3) }
}

func first[T any](f func() T) T { return f() }

func main() {
	// Type param nested in a func-typed parameter: slices.Collect[E](iter.Seq[E])
	// where iter.Seq[E] is func(func(E) bool). Covers a typed var and a call RHS.
	var s iter.Seq[int] = gen()
	fmt.Println(slices.Collect(s))
	fmt.Println(slices.Collect(gen()))

	// Type param in a func return position.
	fmt.Println(first(func() string { return "hi" }))

	// Untypeable local-closure arg: EqualFunc infers V from the maps and skips
	// the closure (whose type can't be inferred standalone).
	a := map[string]int{"x": 1, "y": 2}
	b := map[string]int{"x": 1, "y": 2}
	fmt.Println(maps.EqualFunc(a, b, func(v1, v2 int) bool { return v1 == v2 }))
}

// Output:
// [1 2 3]
// [1 2 3]
// hi
// true
