package main

// A pointer-receiver method auto-addressing a local of a named scalar type must
// mutate the local in place. The receiver load (GetLocal/GetLocal2) is rewritten
// to AddrLocal so the pointer aliases the slot; plain Addr boxes a detached copy
// and the mutation is lost (was the x/text/unicode/norm streamSafe counter bug).

import "fmt"

type counter uint8

func (c *counter) add(n uint8) bool {
	*c += counter(n)
	return *c > 30
}

func main() {
	c := counter(0)
	var over bool
	for i := 0; i < 31; i++ {
		over = c.add(1)
	}
	// Return value consumed via assignment fuses the receiver load into GetLocal2.
	r := c.add(0)
	fmt.Println(uint8(c), over, r)
}

// Output:
// 31 true true
