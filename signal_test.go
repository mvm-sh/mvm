package main

import "testing"

// watchProgressSignal starts the watcher goroutine; safepointInterrupt stops it
// and switches to VM-safepoint draining, leaving no parked goroutine for
// interpreted leak checks to count.
func TestSafepointInterrupt(t *testing.T) {
	watchProgressSignal()
	if progressSignalCh == nil {
		t.Fatal("watchProgressSignal must set progressSignalCh")
	}
	safepointInterrupt() // stops the watcher, re-registers for safepoint draining
	if progressSignalCh == nil {
		t.Fatal("safepointInterrupt must keep a channel registered for draining")
	}
	safepointInterrupt() // idempotent: stops the prior channel, registers a fresh one
}
