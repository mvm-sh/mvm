package main

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/mvm-sh/mvm/vm"
)

const doublePressWindow = 1 * time.Second

var (
	progressSignalCh chan os.Signal
	watcherDone      chan struct{} // closed to stop the watcher goroutine
	watcherExited    chan struct{} // closed by the watcher goroutine on return
	interruptMu      sync.Mutex
	lastInterrupt    time.Time
)

// watchProgressSignal installs the Ctrl-C handler.
func watchProgressSignal() {
	ch := make(chan os.Signal, 2)
	done := make(chan struct{})
	exited := make(chan struct{})
	progressSignalCh, watcherDone, watcherExited = ch, done, exited
	signal.Notify(ch, os.Interrupt)
	go func() {
		defer close(exited)
		for {
			select {
			case <-ch:
				handleInterrupt()
			case <-done:
				return
			}
		}
	}()
}

func handleInterrupt() {
	interruptMu.Lock()
	now := time.Now()
	double := !lastInterrupt.IsZero() && now.Sub(lastInterrupt) < doublePressWindow
	lastInterrupt = now
	interruptMu.Unlock()
	if double {
		os.Exit(130) // 128 + SIGINT
	}
	fmt.Fprintln(os.Stderr, "mvm: interrupt (Ctrl-C again to quit)")
	vm.RequestStateDump()
}

func safepointInterrupt() {
	if watcherDone != nil {
		close(watcherDone)
		<-watcherExited // wait so no goroutine lingers for a leak check to count
		watcherDone, watcherExited = nil, nil
	}
	ch := progressSignalCh
	if ch == nil {
		ch = make(chan os.Signal, 2)
		progressSignalCh = ch
		signal.Notify(ch, os.Interrupt)
	}
	vm.SetSafepointHook(func() {
		select {
		case <-ch:
			handleInterrupt()
		default:
		}
	})
}
