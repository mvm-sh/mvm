// A map whose value type is a struct that (transitively) embeds the map itself
// forces the map to materialize while the struct is still an in-flight,
// word-sized placeholder. Its slot geometry must be recomputed once the struct
// finalizes to its real, larger layout; otherwise inserting a value overruns the
// slot and corrupts the runtime map (a wrong read here, an infinite mapaccess
// loop in larger programs). Covers both an unnamed and a named (carrier) map.
package main

import "fmt"

type Node struct {
	children      map[string]Node
	a, b, c, d, e int64
}

type EMap map[int32]Ext

type Ext struct {
	sub           EMap
	x, y, z, w, v int64
}

func main() {
	n := map[string]Node{}
	n["p"] = Node{a: 1, b: 2, c: 3, d: 4, e: 5}
	n["q"] = Node{a: 6, b: 7, c: 8, d: 9, e: 10}
	gp, gq := n["p"], n["q"]

	e := EMap{}
	e[1] = Ext{x: 11, y: 12, z: 13, w: 14, v: 15}
	ge := e[1]

	fmt.Println(len(n), gp.a, gp.e, gq.a, gq.e)
	fmt.Println(len(e), ge.x, ge.v)
}

// Output:
// 2 1 5 6 10
// 1 11 15
