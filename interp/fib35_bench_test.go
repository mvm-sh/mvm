package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
)

func BenchmarkFib35(b *testing.B) {
	intp := interp.NewInterpreter(golang.GoSpec)
	if _, err := intp.Eval("fib", fibSrc); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := intp.Eval("bench", "fib(35)"); err != nil {
			b.Fatal(err)
		}
	}
}
