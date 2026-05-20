package main

import (
	"fmt"
	"slices"
	"strings"
)

func pair() ([]int, []int) { return []int{1, 2}, []int{3, 4} }

func main() {
	// Generic inference must see the type of a local bound from a call's
	// return tuple (single, multi, and pkg-qualified callees).
	a := pair2()
	fmt.Println(slices.Equal(a, []int{9}))

	_, good := pair()
	fmt.Println(slices.Equal(good, []int{3, 4}))

	fields := strings.Fields("x y z")
	fmt.Println(slices.Equal(fields, []string{"x", "y", "z"}))
}

func pair2() []int { return []int{9} }

// Output:
// true
// true
// true
