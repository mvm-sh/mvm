package interptest

import (
	"fmt"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

func TestChannelSendBareNil(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"chan_error", `ch := make(chan error, 1); ch <- nil; e := <-ch; e == nil`, "true"},
		{"chan_iface", `ch := make(chan interface{}, 1); ch <- nil; v := <-ch; v == nil`, "true"},
		{"select_send", `ch := make(chan error, 1); select { case ch <- nil: }; e := <-ch; e == nil`, "true"},
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

func TestTypeSwitchConcurrency(t *testing.T) {
	const src = `
type reader interface{ read() int }
type box struct{ v int }
func (b *box) read() int { b.v++; return b.v }

func dispatch(e interface{}) int {
	switch r := e.(type) {
	case reader:
		return r.read()
	}
	return -1
}

func run() int {
	done := make(chan int, 8)
	for g := 0; g < 8; g++ {
		go func() {
			var e reader = &box{}
			bad, prev := 0, 0
			for i := 0; i < 20000; i++ {
				n := dispatch(e)
				if n != prev+1 {
					bad++
				}
				prev = n
			}
			done <- bad
		}()
	}
	total := 0
	for i := 0; i < 8; i++ {
		bad := <-done
		total += bad
	}
	return total
}
run()`
	i := newAutoImportInterp(t)
	r, err := i.Eval("test", src)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got != "0" {
		t.Errorf("concurrent type-switch corruption: %s out-of-sequence reads, want 0", got)
	}
}

func TestRangeFuncEarlyReturnStopsIterator(t *testing.T) {
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	if _, err := intp.Eval("setup", `
var cleaned bool
func seq(yield func(int) bool) {
	defer func() { cleaned = true }()
	for i := 0; ; i++ {
		if !yield(i) {
			return
		}
	}
}
func find() int {
	for v := range seq {
		if v == 3 {
			return v
		}
	}
	return -1
}
func run() bool {
	cleaned = false
	find()
	return cleaned
}
`); err != nil {
		t.Fatal(err)
	}
	res, err := intp.Eval("run", "run()")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Bool() {
		t.Fatal("iterator cleanup skipped on early return: stop() not called, coroutine leaked")
	}
}
