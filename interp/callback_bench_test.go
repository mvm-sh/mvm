package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// BenchmarkSortSliceComparator measures Machine.Run hot-path cost on a
// callback-heavy workload: sort.Slice driven by an interpreted less
// function re-enters Machine.Run once per comparator invocation via
// makeCallFunc -> callPooled. SetActiveMachine is paid per re-entry, so
// any regression there shows up here linearly with the number of
// comparator calls.
func BenchmarkSortSliceComparator(b *testing.B) {
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	if _, err := intp.Eval("setup", `
import "sort"
func run() {
	xs := make([]int, 256)
	for i := range xs { xs[i] = (i*7919)%256 }
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
}
`); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := intp.Eval("bench", "run()"); err != nil {
			b.Fatal(err)
		}
	}
}
