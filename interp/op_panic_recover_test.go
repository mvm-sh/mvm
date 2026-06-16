package interp_test

import (
	"fmt"
	"testing"
)

// A Go runtime panic raised by an interpreted opcode (index out of range,
// divide by zero) must be catchable by an interpreted recover(), like a
// native-call panic (gonum/mat's panics() helper around SubsetSym).
func TestOpPanicRecoverable(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"index_oob", `s := []int{1}; f := func() (ok bool) { defer func() { ok = recover() != nil }(); _ = s[3]; return }; f()`, "true"},
		{"index_set_oob", `s := []int{1}; f := func() (ok bool) { defer func() { ok = recover() != nil }(); s[3] = 9; return }; f()`, "true"},
		{"div_zero", `d := 0; f := func() (ok bool) { defer func() { ok = recover() != nil }(); _ = 1 / d; return }; f()`, "true"},
		{"alive_after", `s := []int{1}; f := func() (ok bool) { defer func() { ok = recover() != nil }(); _ = s[3]; return }; f(); len(s)`, "1"},
		// gc message shape: gonum/mat checks HasPrefix(msg, "runtime error: index out of range").
		{"index_oob_msg", `s := []int{1}; f := func() (m string) { defer func() { m = fmt.Sprint(recover()) }(); _ = s[3]; return }; f()`, "runtime error: index out of range"},
		{"index_oob_is_error", `s := []int{1}; f := func() (ok bool) { defer func() { _, ok = recover().(error) }(); _ = s[3]; return }; f()`, "true"},
		// *nilptr read and write must raise a recoverable nil deref, not a raw
		// reflect panic ("Set/Type on zero Value") that escapes recover().
		{"deref_read_nil", `var p *int; f := func() (ok bool) { defer func() { ok = recover() != nil }(); _ = *p; return }; f()`, "true"},
		{"deref_set_nil", `var p *int; f := func() (ok bool) { defer func() { ok = recover() != nil }(); *p = 1; return }; f()`, "true"},
		{"deref_set_struct_nil", `type T struct{ x int }; var p *T; f := func() (ok bool) { defer func() { ok = recover() != nil }(); *p = T{}; return }; f()`, "true"},
		{"deref_nil_is_error", `var p *int; f := func() (ok bool) { defer func() { _, ok = recover().(error) }(); _ = *p; return }; f()`, "true"},
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
