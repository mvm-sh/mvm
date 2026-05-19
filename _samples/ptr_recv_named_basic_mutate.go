package main

import "fmt"

type myInt int

func (v *myInt) Set(n int) { *v = myInt(n) }

type Setter interface{ Set(n int) }

func main() {
	// Direct call: (&t).Set mutates t.
	var t myInt = 0
	(&t).Set(42)
	fmt.Println("direct:", int(t))

	// Via interface: pointer identity must survive boxing.
	var u myInt = 0
	var s Setter = &u
	s.Set(99)
	fmt.Println("via iface:", int(u))
}

// Output:
// direct: 42
// via iface: 99
