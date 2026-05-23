package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
)

// sieveSrc is a classic Eratosthenes sieve bounded by a named constant. The
// inner loop's `j <= sieveN` comparison is the const-bounded hot path that
// const-identifier inlining turns into a fused immediate compare-and-branch.
const sieveSrc = `
const sieveN = 1000000

func sieve() int {
	s := make([]bool, sieveN+1)
	count := 0
	for i := 2; i <= sieveN; i++ {
		if !s[i] {
			count++
			for j := i * i; j <= sieveN; j += i {
				s[j] = true
			}
		}
	}
	return count
}
`

func BenchmarkSieve(b *testing.B) {
	intp := interp.NewInterpreter(golang.GoSpec)
	if _, err := intp.Eval("sieve", sieveSrc); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := intp.Eval("bench", "sieve()"); err != nil {
			b.Fatal(err)
		}
	}
}
