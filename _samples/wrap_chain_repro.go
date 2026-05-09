// Regression test: closure stored in a struct field, iterated via
// for-range across nested recursion. Each wrap closure must run its own
// body; tag and fn must stay in sync.
package main

import "fmt"

type wrapper struct {
	wrap func(in string) string
	tag  string
}

var calls = map[string]int{}

func recur(before string, list []wrapper, depth int) {
	for _, w := range list {
		out := w.wrap(before)
		calls[w.tag]++
		_ = out
		if depth > 0 {
			recur(out, list, depth-1)
		}
	}
}

func main() {
	wrappers := []wrapper{
		{func(s string) string { calls["fnA"]++; return s + "+A" }, "A"},
		{func(s string) string { calls["fnB"]++; return s + "+B" }, "B"},
		{func(s string) string { calls["fnC"]++; return s + "+C" }, "C"},
		{func(s string) string { calls["fnD"]++; return s + "+D" }, "D"},
	}
	recur("seed", wrappers, 3)

	for _, t := range []string{"A", "B", "C", "D"} {
		if calls[t] != calls["fn"+t] {
			fmt.Printf("MISMATCH %s: tag=%d fn=%d\n", t, calls[t], calls["fn"+t])
		} else {
			fmt.Printf("OK %s: %d\n", t, calls[t])
		}
	}
}

// Output:
// OK A: 85
// OK B: 85
// OK C: 85
// OK D: 85
