// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package iter provides basic definitions related to iterators over
// sequences. This is a reduced binding: only the Seq and Seq2 types
// are defined. The Pull and Pull2 helpers rely on runtime coroutine
// primitives (newcoro/coroswitch via go:linkname) that mvm cannot
// link; use `for v := range seq` directly, which is handled natively
// by the interpreter's range-over-func support.
package iter

// Seq is an iterator over sequences of individual values.
// When called as seq(yield), seq calls yield(v) for each value v in the sequence,
// stopping early if yield returns false.
type Seq[V any] func(yield func(V) bool)

// Seq2 is an iterator over sequences of pairs of values, most commonly key-value pairs.
// When called as seq(yield), seq calls yield(k, v) for each pair (k, v) in the sequence,
// stopping early if yield returns false.
type Seq2[K, V any] func(yield func(K, V) bool)
