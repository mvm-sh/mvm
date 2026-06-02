package interp_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
)

// TestGoroutinePanicSurfacesAsExit checks an unrecovered goroutine panic is
// surfaced (logged) and turned into a non-zero exit instead of being silently
// swallowed -- here while main is blocked on a channel the dead goroutine owned,
// which would otherwise deadlock.
func TestGoroutinePanicSurfacesAsExit(t *testing.T) {
	i := newAutoImportInterp(t)
	var stderr bytes.Buffer
	i.SetIO(nil, &bytes.Buffer{}, &stderr)

	_, err := i.Eval("gopanic", `
		done := make(chan bool)
		go func() { panic("boom") }()
		<-done
	`)

	var ee *interp.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *interp.ExitError, got %T: %v", err, err)
	}
	if ee.Code != 2 {
		t.Errorf("Code = %d, want 2", ee.Code)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("goroutine panic not surfaced on stderr:\n%s", stderr.String())
	}
}
