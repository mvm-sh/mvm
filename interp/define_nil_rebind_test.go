package interp_test

import (
	"fmt"
	"strings"
	"testing"
)

// `a, b := x, nil` where b is an existing same-scope var is an assignment to
// the already-typed b (gonum/mat list_test.go: `wasEmpty, empty := empty,
// nil`). The Define compile path errored "undefined" on the untyped-nil RHS;
// it now keeps the rebound var's type and coerces concrete nilables.
func TestDefineNilRebind(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"iface", `f := func() bool { var e error = fmt.Errorf("x"); was, e := e, nil; return was != nil && e == nil }; f()`, "true"},
		{"map", `f := func() bool { m := map[string]int{"a": 1}; was, m := m, nil; return len(was) == 1 && m == nil && len(m) == 0 }; f()`, "true"},
		{"slice", `f := func() bool { s := []int{1, 2}; was, s := s, nil; return len(was) == 2 && s == nil && len(s) == 0 }; f()`, "true"},
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

// A bare-nil rebind to a non-nilable var stays a compile error, as in gc.
func TestDefineNilRebindNonNilable(t *testing.T) {
	src := `f := func() int { x := 1; x, y := nil, 2; return x + y }; f()`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("nonnilable", src); err == nil || !strings.Contains(err.Error(), "cannot use nil") {
		t.Errorf("got err %v, want containing %q", err, "cannot use nil")
	}
}
