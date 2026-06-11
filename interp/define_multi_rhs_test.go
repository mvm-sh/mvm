package interp_test

import (
	"fmt"
	"strings"
	"testing"
)

// A multi-RHS `:=` must evaluate every RHS before assigning any LHS.
// Was govalidator IsCreditCard: `number, lastDigit := number/10, number%10`
// updated number before lastDigit's RHS read it.
func TestDefineMultiRHSEvalOrder(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"redeclare_reads_old", `f := func(n int64) (int64, int64) { n, d := n/10, n%10; return n, d }; a, b := f(129); fmt.Sprint(a, b)`, "12 9"},
		{"both_reuse", `x, y := 3, 4; x, y = y, x; fmt.Sprint(x, y)`, "4 3"},
		{"mixed_new_old", `f := func() string { x := 10; x, y := x%3, x*2; return fmt.Sprint(x, y) }; f()`, "1 20"},
		{"shadow_outer", `x := 5; r := ""; { x, y := x+1, x+2; r = fmt.Sprint(x, y) }; r`, "6 7"},
		{"blank_lhs", `f := func() int { x := 10; x, _ := x+1, x+2; return x }; f()`, "11"},
		{"global_redeclare", `x := 10; x, y := x%3, x*2; fmt.Sprint(x, y)`, "1 20"},
		{"global_blank", `x := 10; x, _ := x+1, x+2; x`, "11"},
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

// A non-name on the left side of := must be a compile error, as in gc
// (a misaligned Define used to nil-panic at runtime).
func TestDefineNonNameLHS(t *testing.T) {
	// A `*p, b :=` statement fails earlier in the parser; only assert it errors.
	cases := []struct {
		n, src string
		msg    string
	}{
		{"indexed", `a := []int{0}; a[0], b := 1, 2; _ = b`, "non-name"},
		{"field", `type s struct{ f int }; v := s{}; v.f, b := 1, 2; _ = b`, "non-name"},
		{"deref", `x := 1; p := &x; *p, b := 1, 2; _ = b`, ""},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			_, err := i.Eval(c.n, c.src)
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("want compile error containing %q, got %v", c.msg, err)
			}
		})
	}
}

// A spread call (f(s...)) through a func value that crosses the native
// boundary must use reflect.CallSlice, including when the callee symbol is a
// declared local (the flag was only set for non-spread variadic calls).
// Was govalidator typeCheck: validatefunc(field, ps[1:]...) on a
// ParamValidator pulled from a map panicked in reflect.Call.
func TestSpreadCallNativeBoundary(t *testing.T) {
	src := `
package main

import "fmt"

type pv func(s string, params ...string) bool

var m = map[string]pv{"k": func(s string, params ...string) bool {
	return fmt.Sprint(s, params) == "x[2 3]"
}}

func main() {
	ps := []string{"a", "2", "3"}
	vf := m["k"]
	if !vf("x", ps[1:]...) {
		panic("spread args mismatch")
	}
	fmt.Println("ok")
}
`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("spread_native", src); err != nil {
		t.Fatalf("eval: %v", err)
	}
}
