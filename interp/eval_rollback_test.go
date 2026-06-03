package interp_test

import "testing"

// A failed Eval must not corrupt a later Eval that reuses a generic the failed
// one had begun to instantiate (the failed unit left half-registered instance
// state on the shared, reused Compiler). Regression for the cross-eval leak.
func TestEvalRollback_GenericReuseAfterError(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("e1", `var p atomic.Pointer[int]; var bad = undefXYZ; _ = p`); err == nil {
		t.Fatal("eval1 expected to fail on undefXYZ")
	}
	if _, err := i.Eval("e2", `var q atomic.Pointer[int]; x := 9; q.Store(&x); println(*q.Load())`); err != nil {
		t.Fatalf("eval2 reusing atomic.Pointer[int] after eval1 error: %v", err)
	}
}

// A failed Eval must leave a clean slate: a later unrelated Eval still works.
func TestEvalRollback_PlainEvalAfterError(t *testing.T) {
	i := newAutoImportInterp(t)
	_, _ = i.Eval("e1", `var p atomic.Pointer[int]; var bad = undefXYZ; _ = p`)
	r, err := i.Eval("e2", `2 + 3`)
	if err != nil {
		t.Fatalf("eval2 after eval1 error: %v", err)
	}
	if got := r.Interface(); got != 5 {
		t.Fatalf("eval2 = %v, want 5", got)
	}
}

// Rollback must not discard state from PRIOR successful Evals (REPL semantics:
// good lines accumulate, only the failed line is undone).
func TestEvalRollback_KeepsPriorGoodState(t *testing.T) {
	i := newAutoImportInterp(t)
	if _, err := i.Eval("e1", `func add(a, b int) int { return a + b }`); err != nil {
		t.Fatalf("eval1: %v", err)
	}
	if _, err := i.Eval("e2", `var bad = undefXYZ`); err == nil {
		t.Fatal("eval2 expected to fail")
	}
	r, err := i.Eval("e3", `add(2, 40)`)
	if err != nil {
		t.Fatalf("eval3 using add from eval1 after eval2 error: %v", err)
	}
	if got := r.Interface(); got != 42 {
		t.Fatalf("eval3 = %v, want 42", got)
	}
}
