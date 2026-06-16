package main

import "fmt"

// Reading an unexported field off a non-addressable struct (a map index result)
// kept reflect's read-only flag; the := store's .Interface() then panicked.
type meta struct {
	loc  *int
	prio int
}

func main() {
	n := 42
	m := map[uint32]meta{1: {loc: &n, prio: 5}}
	got := m[1].loc
	p := m[1].prio
	fmt.Println(got == nil, *got, p)
}

// Output:
// false 42 5
