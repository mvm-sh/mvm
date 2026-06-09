package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
)

// BenchmarkPtrRecvLocal exercises the pointer-receiver-method-on-a-local hot
// path: the receiver load is rewritten to AddrLocal and subsequent reads of the
// slot use GetLocalSync (no GetLocal2 fusion). Measures whether that costs.
const ptrRecvSrc = `
type acc struct{ n int }

func (a *acc) add(x int) { a.n += x }

func run() int {
	var a acc
	for i := 0; i < 1000000; i++ {
		a.add(i)
	}
	return a.n
}
`

func BenchmarkPtrRecvLocal(b *testing.B) {
	intp := interp.NewInterpreter(golang.GoSpec)
	if _, err := intp.Eval("setup", ptrRecvSrc); err != nil {
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
