package interptest

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/vm"
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

// benchDispatch compares the two synth method-dispatch mechanisms -- the typed
// shape vs the word-class shape -- on the same interpreted method. src must do
// ~200 native->interpreted dispatches per run() so the per-Eval cost is dominated
// by dispatch, not compile; the method body is identical across both, so the
// typed/word difference is purely the stub-marshaling cost.
func benchDispatch(b *testing.B, src string) {
	run := func(b *testing.B, word bool) {
		vm.SetForceWordShape(word)
		defer vm.SetForceWordShape(false)
		intp := interp.NewInterpreter(golang.GoSpec)
		intp.ImportPackageValues(stdlib.Values)
		if _, err := intp.Eval("setup", src); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := intp.Eval("bench", "run()"); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("typed", func(b *testing.B) { run(b, false) })
	b.Run("word", func(b *testing.B) { run(b, true) })
}

// BenchmarkNativeBridgeTimeAdd measures the cost of an interpreted->native
// bridge crossing: each time.Add is a reflect.Call through invokeNative, with
// per-call arg marshaling (Reflect/bridgeArgs/coerceInterfaceArgs/wrapFuncArgs)
// and a make([]reflect.Value). On wasm this crossing is ~30x the native cost.
func BenchmarkNativeBridgeTimeAdd(b *testing.B) {
	run := func(b *testing.B, tbl bool) {
		vm.SetNativeMethodTables(tbl)
		defer vm.SetNativeMethodTables(true)
		intp := interp.NewInterpreter(golang.GoSpec)
		intp.ImportPackageValues(stdlib.Values)
		if _, err := intp.Eval("setup", `
import "time"
func run() int64 {
	t := time.Unix(0, 0)
	for i := 0; i < 100000; i++ {
		t = t.Add(time.Duration(i))
	}
	return t.UnixNano()
}
`); err != nil {
			b.Fatal(err)
		}
		if v, err := intp.Eval("check", "run()"); err != nil || v.Interface() != int64(4999950000) {
			b.Fatalf("wrong result %v err %v", v, err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := intp.Eval("bench", "run()"); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("off", func(b *testing.B) { run(b, false) })
	b.Run("on", func(b *testing.B) { run(b, true) })
}

// BenchmarkIfaceDispatchPlain: interface dispatch where the method sits in the
// concrete type's direct Methods slot (ResolveMethodType resolves on entry 1).
func BenchmarkIfaceDispatchPlain(b *testing.B) {
	benchIfaceDispatch(b, `
type Sounder interface{ Sound() int }
type Dog struct{ n int }
func (d Dog) Sound() int { return 4 }
func run() int {
	var s Sounder = Dog{}
	t := 0
	for i := 0; i < 100000; i++ { t += s.Sound() }
	return t
}
`)
}

// BenchmarkIfaceDispatchPromoted: interface dispatch where the method is promoted
// from an embedded field, so the direct slot may be empty and dispatch walks.
func BenchmarkIfaceDispatchPromoted(b *testing.B) {
	benchIfaceDispatch(b, `
type Sounder interface{ Sound() int }
type Base struct{ n int }
func (b Base) Sound() int { return 4 }
type Cat struct{ Base }
func run() int {
	var s Sounder = Cat{}
	t := 0
	for i := 0; i < 100000; i++ { t += s.Sound() }
	return t
}
`)
}

func benchIfaceDispatch(b *testing.B, src string) {
	run := func(b *testing.B, fused bool) {
		vm.SetFusedMethodFrame(fused)
		defer vm.SetFusedMethodFrame(true)
		intp := interp.NewInterpreter(golang.GoSpec)
		if _, err := intp.Eval("setup", src); err != nil {
			b.Fatal(err)
		}
		if v, err := intp.Eval("check", "run()"); err != nil || v.Interface() != int(400000) {
			b.Fatalf("wrong result %v err %v", v, err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := intp.Eval("bench", "run()"); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("off", func(b *testing.B) { run(b, false) })
	b.Run("on", func(b *testing.B) { run(b, true) })
}

// BenchmarkMarshalerDispatch: MarshalJSON() ([]byte, error) -- S2 vs _piipp.
func BenchmarkMarshalerDispatch(b *testing.B) {
	benchDispatch(b, `
import "encoding/json"
type Stamp struct{ v int }
func (s Stamp) MarshalJSON() ([]byte, error) { return []byte("12345"), nil }
var items = make([]Stamp, 200)
func run() { json.Marshal(items) }
`)
}

// BenchmarkStringerDispatch: String() string -- S1 vs _pi (the hottest shape,
// where a per-call alloc delta is the largest fraction).
func BenchmarkStringerDispatch(b *testing.B) {
	benchDispatch(b, `
import "fmt"
type Name struct{ v int }
func (n Name) String() string { return "name" }
var items = make([]Name, 200)
func run() { fmt.Sprint(items) }
`)
}

// BenchmarkReaderDispatch: Read([]byte) (int, error) -- S13 vs pii_ipp, exercised
// through io.Copy (one Read dispatch per byte for 200 bytes).
func BenchmarkReaderDispatch(b *testing.B) {
	benchDispatch(b, `
import "io"
type R struct{ n int }
func (r *R) Read(p []byte) (int, error) {
	if r.n >= 200 {
		return 0, io.EOF
	}
	r.n++
	p[0] = 'x'
	return 1, nil
}
func run() {
	r := &R{}
	io.Copy(io.Discard, r)
}
`)
}
