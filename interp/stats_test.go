package interp_test

import (
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// TestStatsAccumulate checks that Stats counters advance across Eval calls
// and that FormatStats renders the expected header and labels.
func TestStatsAccumulate(t *testing.T) {
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.AutoImportPackages()

	if _, err := i.Eval("first", `1 + 2`); err != nil {
		t.Fatalf("first Eval: %v", err)
	}
	compile1, run1 := i.Stats.CompileTime, i.Stats.RunTime
	if compile1 <= 0 || run1 <= 0 {
		t.Fatalf("after first Eval: CompileTime=%v RunTime=%v, both want >0", compile1, run1)
	}

	if _, err := i.Eval("second", `3 + 4`); err != nil {
		t.Fatalf("second Eval: %v", err)
	}
	if i.Stats.CompileTime <= compile1 || i.Stats.RunTime <= run1 {
		t.Errorf("counters did not advance: compile %v->%v, run %v->%v",
			compile1, i.Stats.CompileTime, run1, i.Stats.RunTime)
	}

	out := interp.FormatStats(i)
	for _, want := range []string{"mvm stats:", "packages:", "sources:", "lines:", "code:", "data:", "compile:", "execute:"} {
		if !strings.Contains(out, want) {
			t.Errorf("FormatStats output missing %q:\n%s", want, out)
		}
	}
}
