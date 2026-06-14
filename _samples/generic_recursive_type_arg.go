package main

// A self-referential named type (Rec.ElemType == Rec) used as a generic type
// argument and as a constraint. The instance-name mangler and the core-type
// constraint walkers must break the cycle instead of recursing forever.

type Rec []Rec

func id[T any](x T) T { return x }

func size[S Rec](s S) int { return len(s) }

func main() {
	r := make(Rec, 3)
	println(len(id(r))) // plain generic, recursive type arg
	println(size(r))    // core-type constraint over the recursive type
}

// Output:
// 3
// 3
