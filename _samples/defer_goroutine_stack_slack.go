package main

import (
	"fmt"
	"sync"
)

// A defer keeps its func value plus a 3-slot header live on the stack for the
// rest of the function; the Grow slack must reserve those slots so a later
// bounds-check-free push does not overrun a goroutine's small initial frame.
// Modeled on grpc internal/grpcsync (*PubSub).Publish.
type pubsub struct {
	mu   sync.Mutex
	subs map[int]bool
	out  chan int
}

func (p *pubsub) publish(v int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k := range p.subs {
		s := k
		f := func() int { return s + v + k }
		select {
		case p.out <- f():
		default:
		}
	}
}

func main() {
	p := &pubsub{subs: map[int]bool{1: true, 2: true}, out: make(chan int, 100)}
	done := make(chan bool)
	go func() {
		for i := 0; i < 10; i++ {
			p.publish(i)
		}
		done <- true
	}()
	<-done
	fmt.Println("ok")
}

// Output:
// ok
