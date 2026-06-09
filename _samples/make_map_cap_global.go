package main

// make(map, sizeHint) assigned to a package global must produce a usable map.
// MkMap left the size hint orphaned on the stack, so the following store wrote
// the hint into the global instead of the map (it read as nil). Locals happened
// to survive because their store reads the stack top. (x/text/unicode/norm
// recompMap bug.)

import "fmt"

var gm map[int]int

func main() {
	gm = make(map[int]int, 4)
	gm[1] = 10
	gm[2] = 20
	fmt.Println(gm == nil, len(gm), gm[1], gm[2])
}

// Output:
// false 2 10 20
