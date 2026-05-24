package main

import "fmt"

// A type may reference a sibling declared later in the same type group; all
// names of a block are in scope regardless of order (issue #18).
type (
	One struct {
		Two Two
	}
	Two string
)

func main() {
	fmt.Println(One{Two: "hi"}.Two)
}

// Output:
// hi
