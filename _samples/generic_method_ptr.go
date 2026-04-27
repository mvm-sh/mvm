package main

type Slice[T any] struct{ x []T }

func (v Slice[T]) Len() int   { return len(v.x) }
func (v *Slice[T]) Set(x []T) { v.x = x }

type Wrap struct{ s Slice[int] }

func main() {
	w := Wrap{}
	w.s.Set([]int{1, 2, 3})
	println(w.s.Len())
}

// Output:
// 3
