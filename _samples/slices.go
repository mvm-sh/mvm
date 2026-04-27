package main

import "slices"

func main() {
	a := []int{1, 2, 3, 4, 5}
	b := []int{1, 2, 3, 4, 5}
	c := []int{1, 2, 3, 4, 6}
	println(slices.Equal(a, b))
	println(slices.Equal(a, c))

	println(slices.Compare(a, b))
	println(slices.Compare(a, c))
	println(slices.Compare(c, a))

	println(slices.Index(a, 3))
	println(slices.Index(a, 99))
	println(slices.Contains(a, 4))
	println(slices.Contains(a, 99))

	println(slices.Max(a))
	println(slices.Min(a))

	idx, ok := slices.BinarySearch(a, 3)
	println(idx, ok)
	idx, ok = slices.BinarySearch(a, 99)
	println(idx, ok)

	r := []int{1, 2, 3}
	slices.Reverse(r)
	println(r[0], r[1], r[2])
}

// Output:
// true
// false
// 0
// -1
// 1
// 2
// -1
// true
// false
// 5
// 1
// 2 true
// 5 false
// 3 2 1
