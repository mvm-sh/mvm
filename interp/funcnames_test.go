package interp

import (
	"slices"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
)

// Exact prefix ("Test", as grpc/codes declares) and digit/"_" continuations
// match; lower-case continuations ("Testify") do not. Mirrors cmd/go's isTest.
func TestFuncNamesMatchesGoIsTest(t *testing.T) {
	i := NewInterpreter(golang.GoSpec)
	src := `func Test() {}
func TestFoo() {}
func Test1() {}
func Test_helper() {}
func Testify() {}
func Testable() {}
func Benchmark() {}
func helper() {}`
	if _, err := i.Eval("u", src); err != nil {
		t.Fatalf("eval: %v", err)
	}

	got := i.FuncNames("Test")
	want := []string{"Test", "TestFoo", "Test1", "Test_helper"}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("FuncNames(%q) missing %q; got %v", "Test", w, got)
		}
	}
	for _, bad := range []string{"Testify", "Testable"} {
		if slices.Contains(got, bad) {
			t.Errorf("FuncNames(%q) must not include lower-continuation %q; got %v", "Test", bad, got)
		}
	}

	if b := i.FuncNames("Benchmark"); !slices.Contains(b, "Benchmark") {
		t.Errorf("FuncNames(%q) must include the exact-prefix name %q; got %v", "Benchmark", "Benchmark", b)
	}
}
