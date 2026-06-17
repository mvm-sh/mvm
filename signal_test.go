package main

import "testing"

// Stop is nil-safe before start, clears the channel (so the watcher goroutine
// exits), and is idempotent.
func TestStopProgressSignal(t *testing.T) {
	stopProgressSignal() // nil-safe when never started
	if progressSignalCh != nil {
		t.Fatal("progressSignalCh must be nil before watchProgressSignal")
	}
	watchProgressSignal()
	if progressSignalCh == nil {
		t.Fatal("watchProgressSignal must set progressSignalCh")
	}
	stopProgressSignal()
	if progressSignalCh != nil {
		t.Error("stopProgressSignal must clear progressSignalCh so the watcher exits")
	}
	stopProgressSignal() // idempotent
}
