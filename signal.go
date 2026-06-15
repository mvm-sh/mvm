package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/mvm-sh/mvm/vm"
)

const doublePressWindow = 1 * time.Second

func watchProgressSignal() {
	ch := make(chan os.Signal, 1)
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
