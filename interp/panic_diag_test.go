package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/vm"
)

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
