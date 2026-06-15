package interp_test

import (
	"bytes"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/vm"
)

// capWriter records the first cap bytes written and discards the rest, so a
// flood of dumps does not balloon memory. Mutex-guarded for use under -race.
type capWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	cap int
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() < w.cap {
		w.buf.Write(p)
	}
	return len(p), nil
}

func (w *capWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestRequestStateDump verifies that a state-dump request raised from another
// goroutine (as a signal handler would) prints the current position and mvm
// call stack of the running interpreter, mid-run, without stopping it.
func TestRequestStateDump(t *testing.T) {
	intp := interp.NewInterpreter(golang.GoSpec)
	sink := &capWriter{cap: 8192}
	intp.SetIO(nil, sink, sink)

	if _, err := intp.Eval("setup",
		`func spin(n int) int { s := 0; for i := 0; i < n; i++ { s += i % 7 }; return s }`); err != nil {
		t.Fatal(err)
	}

	// Arm the flag a bounded number of times from another goroutine for
	// cross-goroutine coverage under -race (each re-arm yields, so the dump
	// does not fire on every loop back-edge). Arm once here too so at least
	// one dump is guaranteed regardless of scheduling.
	stop := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			select {
			case <-stop:
				return
			default:
			}
			vm.RequestStateDump()
			runtime.Gosched()
		}
	}()
	vm.RequestStateDump()

	if _, err := intp.Eval("run", "spin(200000)"); err != nil {
		close(stop)
		t.Fatal(err)
	}
	close(stop)

	out := sink.String()
	for _, want := range []string{"mvm execution state", "mvm stack:", "spin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dump missing %q; got:\n%s", want, out)
		}
	}
}
