package main

import "fmt"

// make(T) for a named map keeps T's identity, so it stays assignable to a T
// field/elem. byName (named key + any elem, stored in a struct field) also
// needs the synth map to be direct-iface, else boxing panics "bad indir".
type FullName string
type byNumber map[int]string
type byMessage map[string]byNumber
type byName map[FullName]any

type registry struct {
	names byName
}

func main() {
	var outer byMessage
	if outer == nil {
		outer = make(byMessage)
	}
	if outer["a"] == nil {
		outer["a"] = make(byNumber)
	}
	outer["a"][7] = "seven"

	r := &registry{}
	if r.names == nil {
		r.names = make(byName)
	}
	r.names["pkg.M"] = 42

	fmt.Printf("%T %T %s %d\n", outer, outer["a"], outer["a"][7], len(outer))
	fmt.Printf("%T %v\n", r.names, r.names["pkg.M"])
}

/*
Output:
main.byMessage main.byNumber seven 1
main.byName 42
*/
