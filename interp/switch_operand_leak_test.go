package interp_test

import (
	"fmt"
	"testing"
)

func TestSwitchOperandLeak(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"no_match_loop", `n := 0; for i := 0; i < 2000; i++ { switch i % 251 { case -1: n++ } }; n`, "0"},
		{"rare_match_loop", `n := 0; for i := 0; i < 2000; i++ { switch i % 251 { case 7: n++ } }; n`, "8"},
		{"multi_value_loop", `n := 0; for i := 0; i < 2000; i++ { switch i % 251 { case 1, 2, 3: n++ } }; n`, "24"},
		{"last_case_match", "n := 0\nfor i := 0; i < 2000; i++ {\n\tswitch 7 {\n\tcase 6:\n\tcase 7:\n\t\tn++\n\t}\n}\nn", "2000"},
		{"with_default", "n := 0\nfor i := 0; i < 2000; i++ {\n\tswitch i % 251 {\n\tcase 7:\n\tdefault:\n\t\tn++\n\t}\n}\nn", "1992"},
		{"return_bodies", "f := func(s int) string {\n\tswitch s {\n\tcase 1:\n\t\treturn \"small\"\n\tcase 2:\n\t\treturn \"large\"\n\t}\n\treturn \"other\"\n}\nout := \"\"\nfor i := 0; i < 2000; i++ {\n\tout = f(3)\n}\nout", "other"},
		{"nested_default", "n := 0\nfor s := 0; s < 3; s++ {\n\tswitch s {\n\tcase 0:\n\t\tswitch s {\n\t\tdefault:\n\t\t\tn++\n\t\t}\n\tcase 1:\n\t\tswitch s {\n\t\tdefault:\n\t\t\tn += 10\n\t\t}\n\tcase 2:\n\t\tn += 100\n\t}\n}\nn", "111"},
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
