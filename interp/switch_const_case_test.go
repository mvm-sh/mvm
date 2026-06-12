package interp_test

import (
	"fmt"
	"strings"
	"testing"
)

// An untyped-const case value must fold to the switch operand's type
// (gonum/mat normLapack: `switch norm {case 2:}` with norm float64 fell to
// default). The operand type rides on the EqualSet token; the compiler's
// stack model loses it past the first case of the chain.
func TestSwitchConstCaseOperandType(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"float64_first", `x := 1.0; s := ""; switch x { case 1: s = "one"; case 2: s = "two"; default: s = "other" }; s`, "one"},
		{"float64_second", `x := 2.0; s := ""; switch x { case 1: s = "one"; case 2: s = "two"; default: s = "other" }; s`, "two"},
		{"float64_default", `x := 3.5; s := ""; switch x { case 1: s = "one"; case 2: s = "two"; default: s = "other" }; s`, "other"},
		{"float_case_int_operand_expr", `f := func(x float64) string { switch x { case 1: return "one"; case 2.5: return "half" }; return "other" }; f(2.5)`, "half"},
		{"literal_first_binary_operand", `f := 1.75; s := ""; switch 2 * f { case 3.5: s = "hit"; default: s = "miss" }; s`, "hit"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			r, err := i.Eval(c.n, c.src)
			if err != nil {
				t.Fatalf("eval %q: %v", c.src, err)
			}
			if got := fmt.Sprintf("%v", r); got != c.res {
				t.Errorf("got %q, want %q", got, c.res)
			}
		})
	}
}

// A case value gc rejects must not silently truncate-and-match.
func TestSwitchConstCaseErrors(t *testing.T) {
	cases := []struct{ n, src, errSub string }{
		{"overflow", `var b byte = 44; s := ""; switch b { case 300: s = "hit" }; s`, "overflows"},
		{"truncated", `i := 2; s := ""; switch i { case 2.5: s = "hit" }; s`, "truncated"},
		{"mismatched_var", `i := 2; f := 2.5; s := ""; switch i { case f: s = "hit" }; s`, "mismatched types"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			if _, err := i.Eval(c.n, c.src); err == nil || !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("eval %q: got err %v, want containing %q", c.src, err, c.errSub)
			}
		})
	}
}
