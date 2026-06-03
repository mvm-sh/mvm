package interp_test

import (
	"errors"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/vm"
)

func TestOsExitReturnsExitError(t *testing.T) {
	i := newAutoImportInterp(t)
	_, err := i.Eval("exit", "os.Exit(42)")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ee *interp.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *interp.ExitError, got %T: %v", err, err)
	}
	if ee.Code != 42 {
		t.Errorf("Code = %d, want 42", ee.Code)
	}
}

func TestLogFatalReturnsExitError(t *testing.T) {
	// log is interpreted (source-only), so it needs an explicit import and writes
	// via its own os.Stderr, not the test's native log -- discard that output and
	// assert the exit behavior: log.Fatal -> os.Exit(1) -> ExitError (os.Exit is
	// virtualized). The message formatting is covered by `mvm test log` examples.
	i := newAutoImportInterp(t)
	_, err := i.Eval("fatal", `import ("io"; "log"); log.SetOutput(io.Discard); log.Fatal("boom")`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ee *interp.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *interp.ExitError, got %T: %v", err, err)
	}
	if ee.Code != 1 {
		t.Errorf("Code = %d, want 1", ee.Code)
	}
}

// TestRuntimeErrorStillPanicError guards the vm.recoverPanic shape check:
// a runtime.Error (here a nil deref via out-of-bounds index) must keep the
// capturePanic wrapping with mvm diagnostics, not slip through as a bare
// error like ExitError does.
func TestRuntimeErrorStillPanicError(t *testing.T) {
	i := newAutoImportInterp(t)
	_, err := i.Eval("oob", `var a = []int{1, 2}; _ = a[5]`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *vm.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *vm.PanicError, got %T: %v", err, err)
	}
}
