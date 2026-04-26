// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package slices defines various functions useful with slices of any type.
//
// This is a curated subset of the Go 1.26 slices package, embedded for
// import by the mvm interpreter. Functions omitted here rely on
// interpreter features not yet implemented:
//
//   - Insert, Delete, DeleteFunc, Replace, Grow, Clip, Concat, Repeat,
//     Clone, Compact, CompactFunc: use overlaps()/unsafe.Sizeof
//   - Sort, SortFunc, SortStableFunc, IsSorted, IsSortedFunc, SortedFunc,
//     SortStableFunc, SortedStableFunc: pulled in from sort helpers that
//     depend on math/bits
//   - All, Values, Backward, Chunk, Sorted, Collect, AppendSeq: return
//     iter.Seq / iter.Seq2
package slices

import "cmp"

// Equal reports whether two slices are equal: the same length and all
// elements equal. If the lengths are different, Equal returns false.
// Otherwise, the elements are compared in increasing index order, and the
// comparison stops at the first unequal pair.
// Empty and nil slices are considered equal.
// Floating point NaNs are not considered equal.
func Equal[S ~[]E, E comparable](s1, s2 S) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			return false
		}
	}
	return true
}

// EqualFunc reports whether two slices are equal using an equality
// function on each pair of elements. If the lengths are different,
// EqualFunc returns false. Otherwise, the elements are compared in
// increasing index order, and the comparison stops at the first index
// for which eq returns false.
func EqualFunc[S1 ~[]E1, S2 ~[]E2, E1, E2 any](s1 S1, s2 S2, eq func(E1, E2) bool) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i, v1 := range s1 {
		v2 := s2[i]
		if !eq(v1, v2) {
			return false
		}
	}
	return true
}

// Compare compares the elements of s1 and s2, using [cmp.Compare] on each
// pair of elements. The elements are compared sequentially, starting at
// index 0, until one element is not equal to the other. The result of
// comparing the first non-matching elements is returned. If both slices
// are equal until one of them ends, the shorter slice is considered less
// than the longer one. The result is 0 if s1 == s2, -1 if s1 < s2, and
// +1 if s1 > s2.
func Compare[S ~[]E, E cmp.Ordered](s1, s2 S) int {
	for i, v1 := range s1 {
		if i >= len(s2) {
			return +1
		}
		v2 := s2[i]
		if c := cmp.Compare(v1, v2); c != 0 {
			return c
		}
	}
	if len(s1) < len(s2) {
		return -1
	}
	return 0
}

// CompareFunc is like [Compare] but uses a custom comparison function on
// each pair of elements. The result is the first non-zero result of
// cmp; if cmp always returns 0 the result is 0 if len(s1) == len(s2),
// -1 if len(s1) < len(s2), and +1 if len(s1) > len(s2).
func CompareFunc[S1 ~[]E1, S2 ~[]E2, E1, E2 any](s1 S1, s2 S2, cmp func(E1, E2) int) int {
	for i, v1 := range s1 {
		if i >= len(s2) {
			return +1
		}
		v2 := s2[i]
		if c := cmp(v1, v2); c != 0 {
			return c
		}
	}
	if len(s1) < len(s2) {
		return -1
	}
	return 0
}

// Index returns the index of the first occurrence of v in s, or -1 if
// not present.
func Index[S ~[]E, E comparable](s S, v E) int {
	for i := range s {
		if v == s[i] {
			return i
		}
	}
	return -1
}

// IndexFunc returns the first index i satisfying f(s[i]), or -1 if none
// do.
func IndexFunc[S ~[]E, E any](s S, f func(E) bool) int {
	for i := range s {
		if f(s[i]) {
			return i
		}
	}
	return -1
}

// Contains reports whether v is present in s.
func Contains[S ~[]E, E comparable](s S, v E) bool {
	return Index(s, v) >= 0
}

// ContainsFunc reports whether at least one element e of s satisfies
// f(e).
func ContainsFunc[S ~[]E, E any](s S, f func(E) bool) bool {
	return IndexFunc(s, f) >= 0
}

// Max returns the maximal value in x. It panics if x is empty. For
// floating-point E, Max propagates NaNs (any NaN argument forces the
// output to be NaN).
func Max[S ~[]E, E cmp.Ordered](x S) E {
	if len(x) < 1 {
		panic("slices.Max: empty list")
	}
	m := x[0]
	for i := 1; i < len(x); i++ {
		m = max(m, x[i])
	}
	return m
}

// MaxFunc returns the maximal value in x, using cmp to compare elements.
// It panics if x is empty. If there is more than one maximal element
// according to the cmp function, MaxFunc returns the first one.
func MaxFunc[S ~[]E, E any](x S, cmp func(a, b E) int) E {
	if len(x) < 1 {
		panic("slices.MaxFunc: empty list")
	}
	m := x[0]
	for i := 1; i < len(x); i++ {
		if cmp(x[i], m) > 0 {
			m = x[i]
		}
	}
	return m
}

// Min returns the minimal value in x. It panics if x is empty. For
// floating-point numbers, Min propagates NaNs (any NaN argument forces
// the output to be NaN).
func Min[S ~[]E, E cmp.Ordered](x S) E {
	if len(x) < 1 {
		panic("slices.Min: empty list")
	}
	m := x[0]
	for i := 1; i < len(x); i++ {
		m = min(m, x[i])
	}
	return m
}

// MinFunc returns the minimal value in x, using cmp to compare elements.
// It panics if x is empty. If there is more than one minimal element
// according to the cmp function, MinFunc returns the first one.
func MinFunc[S ~[]E, E any](x S, cmp func(a, b E) int) E {
	if len(x) < 1 {
		panic("slices.MinFunc: empty list")
	}
	m := x[0]
	for i := 1; i < len(x); i++ {
		if cmp(x[i], m) < 0 {
			m = x[i]
		}
	}
	return m
}

// BinarySearch searches for target in a sorted slice and returns the
// earliest position where target is found, or the position where target
// would appear in the sort order; it also returns a bool saying whether
// the target is really found in the slice. The slice must be sorted in
// increasing order.
func BinarySearch[S ~[]E, E cmp.Ordered](x S, target E) (int, bool) {
	n := len(x)
	i, j := 0, n
	for i < j {
		h := int(uint(i+j) >> 1)
		if cmp.Less(x[h], target) {
			i = h + 1
		} else {
			j = h
		}
	}
	return i, i < n && (x[i] == target || (x[i] != x[i] && target != target))
}

// BinarySearchFunc works like [BinarySearch], but uses a custom
// comparison function. The slice must be sorted in increasing order,
// where "increasing" is defined by cmp. cmp should return 0 if the
// slice element matches the target, a negative number if the slice
// element precedes the target, or a positive number if the slice
// element follows the target. cmp must implement the same ordering as
// the slice, such that if cmp(a, t) < 0 and cmp(b, t) >= 0, then a
// must precede b in the slice.
func BinarySearchFunc[S ~[]E, E, T any](x S, target T, cmp func(E, T) int) (int, bool) {
	n := len(x)
	i, j := 0, n
	for i < j {
		h := int(uint(i+j) >> 1)
		if cmp(x[h], target) < 0 {
			i = h + 1
		} else {
			j = h
		}
	}
	return i, i < n && cmp(x[i], target) == 0
}

// Reverse reverses the elements of the slice in place.
func Reverse[S ~[]E, E any](s S) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
