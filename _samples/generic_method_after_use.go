package main

import "fmt"

type (
	Slice[T any] struct{ x []T }
	Wrap         struct{ s Slice[int] }
)

func (v Slice[T]) Show() string { return fmt.Sprint(v.x) }
func (w Wrap) Show() string     { return w.s.Show() }

func main() {
	fmt.Println(Wrap{}.Show())
}

// Output:
// []
