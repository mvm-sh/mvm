package vm

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestGoroutineFaultRecordFirstWins checks the sink keeps the first fault,
// surfaces it, and closes abort (matching Go, where the first panic crashes).
func TestGoroutineFaultRecordFirstWins(t *testing.T) {
	var buf bytes.Buffer
	g := newGoroutineFault(&buf, false)

	g.record(errors.New("first"))
	g.record(errors.New("second"))

	if got := g.pending(); got == nil || got.Error() != "first" {
		t.Fatalf("pending() = %v, want first", got)
	}
	if !strings.Contains(buf.String(), "first") {
		t.Errorf("fault not surfaced: %q", buf.String())
	}
	select {
	case <-g.abort:
	default:
		t.Fatal("abort channel not closed after record")
	}
}

// TestWatchFaultGating checks channel waits only become fault-abortable once a
// goroutine has spawned and the policy propagates.
func TestWatchFaultGating(t *testing.T) {
	m := &Machine{}
	if m.watchFault() {
		t.Fatal("no sink: want false")
	}
	m.fault = newGoroutineFault(io.Discard, false)
	if m.watchFault() {
		t.Fatal("armed but not spawned: want false")
	}
	m.fault.spawned.Store(true)
	if !m.watchFault() {
		t.Fatal("spawned + propagate: want true")
	}
	m.fault.cont = true
	if m.watchFault() {
		t.Fatal("continue policy: want false")
	}
}
