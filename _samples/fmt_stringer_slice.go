package main

import "fmt"

// A slice whose element type defines String()/Error() must render each element
// through that method under fmt's value verbs (%v, %s, Println), matching Go.
// fmt is a native bridge and the reflect element type of an interpreted type
// carries no Go-level methods, so without per-element bridging the elements
// printed with default struct formatting (e.g. {1 2} instead of (1,2)).

type point struct{ x, y int }

func (p point) String() string { return fmt.Sprintf("(%d,%d)", p.x, p.y) }

type wrapErr struct{ msg string }

func (e wrapErr) Error() string { return e.msg }

func main() {
	pts := []point{{1, 2}, {3, 4}}
	fmt.Println(pts)
	fmt.Printf("%v\n", pts)
	fmt.Printf("%s\n", pts)
	errs := []error{wrapErr{"boom"}, wrapErr{"bang"}}
	fmt.Println(errs)
}

// Output:
// [(1,2) (3,4)]
// [(1,2) (3,4)]
// [(1,2) (3,4)]
// [boom bang]
