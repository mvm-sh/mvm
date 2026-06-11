package interp_test

import (
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/vm"
)

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
