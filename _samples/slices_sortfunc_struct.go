package main

import (
	"cmp"
	"fmt"
	"slices"
)

// slices.SortFunc over a slice of a named struct, with the comparator closure
// itself calling generics (cmp.Or, cmp.Compare). Exercises two monomorphization
// fixes together: the []Order type arg must re-resolve to the real struct (not
// an opaque placeholder), and the closure must be typed from its signature so
// the inner cmp.* instantiations are not discarded. This is cmp's ExampleOr_sort.
type Order struct {
	Product  string
	Customer string
	Price    float64
}

func main() {
	orders := []Order{
		{"foo", "alice", 1.00},
		{"bar", "bob", 3.00},
		{"baz", "carol", 4.00},
		{"foo", "alice", 2.00},
	}
	slices.SortFunc(orders, func(a, b Order) int {
		return cmp.Or(
			cmp.Compare(a.Customer, b.Customer),
			cmp.Compare(a.Product, b.Product),
			cmp.Compare(b.Price, a.Price),
		)
	})
	for _, o := range orders {
		fmt.Printf("%s %s %.2f\n", o.Product, o.Customer, o.Price)
	}
}

// Output:
// foo alice 2.00
// foo alice 1.00
// bar bob 3.00
// baz carol 4.00
