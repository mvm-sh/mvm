package main

// Regression: a local constant folded from a dot-imported (bridged) constant
// used to resolve to an empty Const, which then panicked the compiler
// ("numericOp: nil type") or failed at runtime ("reflect: ... Interface on
// zero Value"). Dot-import binds package symbols as Kind=Value, so the
// constant must be recovered from its reflect value in goparser evalConstExpr
// (the Ident case), the same way the package-selector path does.

import . "math"

const half = MaxInt32 / 2

func main() {
	println(half)
}

// Output:
// 1073741823
