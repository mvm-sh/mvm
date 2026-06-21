package interptest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/vm"
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

func TestPanicErrorShape(t *testing.T) {
	src := `func boom() {
	a := []int{1, 2}
	_ = a[5]
}
boom()`
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	_, err := intp.Eval("boom.go", src)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var pe *vm.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *vm.PanicError, got %T: %v", err, err)
	}
	if pe.Pos == 0 {
		t.Errorf("expected non-zero Pos, got 0")
	}
	if len(pe.Frames) == 0 {
		t.Errorf("expected non-empty Frames, got 0")
	}
	out := pe.Error()
	for _, want := range []string{"boom.go", "boom", "a[5]", "^"} {
		if !strings.Contains(out, want) {
			t.Errorf("Format output missing %q:\n%s", want, out)
		}
	}
}

// TestCompileErrorSourceContext checks that a compile/load failure carries
// file:line:col plus a source snippet and caret (matching the runtime panic
// diagnostics), rendered at the interp.Eval chokepoint via ErrPos.
func TestCompileErrorSourceContext(t *testing.T) {
	src := "func main() {\n\tundef()\n}"
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	_, err := intp.Eval("prog.go", src)
	if err == nil {
		t.Fatal("expected compile error, got nil")
	}
	out := err.Error()
	for _, want := range []string{"prog.go:2:", "undefined: undef", "undef()", "^"} {
		if !strings.Contains(out, want) {
			t.Errorf("error missing %q:\n%s", want, out)
		}
	}
}

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

// TestGoroutineClosureCaptureStackDepth guards a closure-capture stack
// under-reservation: on a goroutine's tight stack the worker overran mem and
// died before wg.Done(), hanging wg.Wait(). See compiler reserveDepth.
func TestGoroutineClosureCaptureStackDepth(t *testing.T) {
	i := newAutoImportInterp(t)
	r, err := i.Eval("gostack", `
		func pairs(n int) func(func(int, int) bool) {
			return func(yield func(int, int) bool) {
				for i := 0; i < n; i++ {
					if !yield(i, i*i) {
						return
					}
				}
			}
		}
		func work(n int) int {
			total := 0
			for k, v := range pairs(n) {
				total += k + v
			}
			return total
		}
		res := make([]int, 5)
		wg := sync.WaitGroup{}
		wg.Add(5)
		for i := 0; i < 5; i++ {
			go func(i int) {
				res[i] = work(i + 1)
				wg.Done()
			}(i)
		}
		wg.Wait()
		sum := 0
		for _, v := range res {
			sum += v
		}
		sum
	`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	// work(n) = sum_{k<n} (k + k*k): 0,2,8,20,40 for n=1..5 -> total 70.
	if got := fmt.Sprintf("%v", r); got != "70" {
		t.Errorf("got %q, want %q", got, "70")
	}
}

// An unrecovered panic in a spawned goroutine must surface with mvm source
// context (position + stack), not a bare one-line message. The child machine
// inherits the parent's DebugInfo so capturePanic can render the location.
func TestGoroutinePanicCarriesSourceContext(t *testing.T) {
	i := newAutoImportInterp(t)
	var out, errBuf bytes.Buffer
	i.SetIO(os.Stdin, &out, &errBuf)

	// The goroutine derefs a nil pointer; main blocks on a channel so the
	// fault aborts the wait and surfaces (propagate policy).
	src := `
type T struct{ v int }
func boom() int {
	var p *T
	return p.v
}
go func() { _ = boom() }()
ch := make(chan int)
<-ch
`
	if _, err := i.Eval("gtest", src); err == nil {
		t.Fatal("expected a non-nil error from the goroutine fault")
	}
	got := errBuf.String()
	if !strings.Contains(got, "panic in goroutine") {
		t.Fatalf("missing goroutine-panic header:\n%s", got)
	}
	// The DebugInfo-backed render includes an "mvm stack:" section and the
	// panicking function's source location; the bare fallback has neither.
	if !strings.Contains(got, "mvm stack:") {
		t.Fatalf("missing mvm stack (no source context inherited):\n%s", got)
	}
	if !strings.Contains(got, "boom") || !strings.Contains(got, "gtest:") {
		t.Fatalf("missing source location of the panic:\n%s", got)
	}
}
