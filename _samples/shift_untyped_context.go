package main

// Regression: an untyped-const left operand of a non-constant shift takes its
// type from the surrounding context (spec: Operators), e.g. uint64 from the
// comparison with mant below, not the default int. The compiler typed it int
// and rejected the comparison as "mismatched types uint64 and int"
// (shopspring/decimal rounding.go:67).
func main() {
	var mant uint64 = 1 << 52
	var bits uint = 52
	if mant > 1<<bits {
		println("gt")
	} else {
		println("le")
	}
	if mant >= 1<<bits {
		println("ge")
	}
	var u uint64 = 18446744073709551615
	var one uint = 1
	if u > 1<<one {
		println("big")
	}
	// Chained shift: the context type reaches through both shifts.
	var a, b uint = 30, 30
	if u > 1<<a<<b {
		println("chained")
	}
	// Unary ^ over a context-typed shift result.
	if u&^(1<<one) == u-2 {
		println("masked")
	}
	// Context narrower than int: the shift wraps at the context width.
	var f uint8 = 8
	var s uint = 9
	println(f & (1 << s))
}

// Output:
// le
// ge
// big
// chained
// masked
// 0
