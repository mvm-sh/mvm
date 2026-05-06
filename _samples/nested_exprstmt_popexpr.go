package main

type T struct{}

func (t *T) Run(name string, fn func(*T)) bool {
	fn(t)
	return true
}

func (t *T) Logf(format string, args ...any) {}

// Regression: outer expression statement (t.Run with discarded bool return)
// contains a closure whose body is itself an expression statement (t.Logf
// with no return). The inner closure's PopExpr must not clobber the outer's
// stack-base tracking, so the outer's leftover bool gets popped on each
// iteration. Otherwise the for-range iterator at sp-1 ends up shifted.
func main() {
	var t T
	cases := map[string]int{"a": 1, "b": 2, "c": 3}
	for name, v := range cases {
		t.Run(name, func(t *T) {
			t.Logf("v=%d", v)
		})
		_ = name
	}
	println("ok")
}

// Output:
// ok
