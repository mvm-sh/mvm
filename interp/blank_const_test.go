package interp_test

import "testing"

// A blank const (`_ = -iota`) must not register a symbol under the "_" key.
// It did, so the blank ident in a later tuple-assign LHS (`_, n = f()`)
// resolved to that const and compiled to a field-set on a non-addressable
// const value -> "reflect.Value.SetInt using unaddressable value".
// Was protobuf encoding/protowire consumeFieldValueD (TestGroup).
func TestBlankConstNoShadow(t *testing.T) {
	run(t, []etest{
		{
			n: "iota_blank_then_tuple_assign",
			src: `
				const ( _ = -iota; errA; errB )
				func two() (uint32, int) { return 7, 4 }
				func f() (n int) { _, n = two(); return n }
				f()
			`,
			res: "4",
		},
		{
			n: "blank_const_advances_iota",
			src: `const ( _ = iota; a; b ); a + b`,
			res: "3",
		},
		{
			n: "two_blank_consts",
			src: `
				const _ = 1
				const _ = 2
				func two() (int, int) { return 1, 9 }
				func g() (n int) { _, n = two(); return n }
				g()
			`,
			res: "9",
		},
	})
}
