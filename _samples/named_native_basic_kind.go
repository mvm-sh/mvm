package main

import "time"

// A named type defined over a native basic-kind type.
type Duration time.Duration

func valid(d Duration) bool { return d > 0 }

func main() {
	println(valid(Duration(5 * time.Second)))
	println(valid(Duration(-1)))
}

// Output:
// true
// false
