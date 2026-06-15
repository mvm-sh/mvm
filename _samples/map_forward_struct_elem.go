// A map/array whose element (or key) type is a struct that transitively embeds
// the map forces the derived type to materialize while the struct is still an
// in-flight, word-sized placeholder. Its geometry (map slot size, array stride)
// must be recomputed once the struct finalizes to its real, larger layout;
// otherwise inserting a value overruns the slot/element and corrupts the runtime
// container. Covers map elem (unnamed + named carrier), array elem, and a large
// struct map key (indirect-key flag).
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

type Holder struct {
	arr           map[string][3]Holder
	a, b, c, d, e int64
}

// K is large (>128B) so a correct map stores it indirectly; built while K is a
// placeholder, the indirect-key flag and slot size would otherwise stay wrong.
type K struct {
	next                                        *KNode
	a, b, c, d, e, f, g, h, i, j, k, l, m, n, o int64
}

type KNode struct {
	m    map[K]int
	self *KNode
}

func main() {
	n := map[string]Node{}
	n["p"] = Node{a: 1, b: 2, c: 3, d: 4, e: 5}
	n["q"] = Node{a: 6, b: 7, c: 8, d: 9, e: 10}
	gp, gq := n["p"], n["q"]

	e := EMap{}
	e[1] = Ext{x: 11, y: 12, z: 13, w: 14, v: 15}
	ge := e[1]

	h := map[string][3]Holder{}
	h["a"] = [3]Holder{
		{a: 21, b: 22, c: 23, d: 24, e: 25},
		{a: 26, b: 27, c: 28, d: 29, e: 30},
		{a: 31, b: 32, c: 33, d: 34, e: 35},
	}
	gh := h["a"]

	km := map[K]int{}
	k1 := K{a: 1, o: 99}
	km[k1] = 42

	fmt.Println(len(n), gp.a, gp.e, gq.a, gq.e)
	fmt.Println(len(e), ge.x, ge.v)
	fmt.Println(gh[0].a, gh[0].c, gh[0].e, gh[1].a, gh[1].c, gh[1].e, gh[2].a, gh[2].c, gh[2].e)
	fmt.Println(km[k1], len(km))
}

// Output:
// 2 1 5 6 10
// 1 11 15
// 21 23 25 26 28 30 31 33 35
// 42 1
