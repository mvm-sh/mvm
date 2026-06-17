package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/mvm-sh/mvm/vm"
)

const doublePressWindow = 1 * time.Second

var progressSignalCh chan os.Signal

func watchProgressSignal() {
	ch := make(chan os.Signal, 1)
	progressSignalCh = ch
	signal.Notify(ch, os.Interrupt)
	go func() {
		var last time.Time
		for range ch {
			now := time.Now()
			if !last.IsZero() && now.Sub(last) < doublePressWindow {
				os.Exit(130) // 128 + SIGINT
			}
			last = now
			fmt.Fprintln(os.Stderr, "mvm: interrupt (Ctrl-C again to quit)")
			vm.RequestStateDump()
		}
	}()
}

// stopProgressSignal stops the Ctrl-C watcher and lets its goroutine exit, so
// it does not register as a leak in goroutine-checking tests. Ctrl-C reverts to
// its default disposition (terminate).
func stopProgressSignal() {
	if progressSignalCh != nil {
		signal.Stop(progressSignalCh)
		close(progressSignalCh)
		progressSignalCh = nil
	}
}
