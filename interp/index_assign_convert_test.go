package interp_test

import (
	"fmt"
	"testing"
)

// An untyped const assigned through an index, map key, or pointer deref must
// adopt the element/key/pointee type (gonum/mat Permutation: `Data[i] = 1`
// into []float64 stored raw int bits, printing 5e-324). IndexAssign and
// DerefAssign now coerce operands like plain Assign does.
func TestIndexAssignConstConvert(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"slice", `f := func() float64 { d := make([]float64, 2); d[0] = 1; return d[0] }; f()`, "1"},
		{"array", `f := func() float64 { var a [2]float64; a[1] = 1; return a[1] }; f()`, "1"},
		{"computed index", `f := func() float64 { d := make([]float64, 4); i, v, s := 1, 0, 2; d[i*s+v] = 1; return d[2] }; f()`, "1"},
		{"map value", `f := func() float64 { m := map[string]float64{}; m["k"] = 1; return m["k"] }; f()`, "1"},
		{"map key", `f := func() string { m := map[float64]string{}; m[1] = "a"; return m[1.0] }; f()`, "a"},
		{"deref", `f := func() float64 { p := new(float64); *p = 1; return *p }; f()`, "1"},
		{"complex elem", `f := func() complex128 { d := make([]complex128, 1); d[0] = 1; return d[0] }; f()`, "(1+0i)"},
		{"typed var elem", `f := func() float64 { d := make([]float64, 1); n := 2; d[0] = float64(n); return d[0] }; f()`, "2"},
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
