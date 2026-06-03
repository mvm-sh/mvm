package vm

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
)

// ErrGoroutineFault aborts a Run that was waiting on a channel when a goroutine
// panicked elsewhere; recoverPanic returns it and the top-level Eval maps it to a
// non-zero exit.
var ErrGoroutineFault = errors.New("vm: unrecovered goroutine panic")

// goroutineFault carries the first unrecovered panic from a spawned goroutine,
// shared by the root machine and every goroutine child. Go crashes the process
// on such a panic; mvm used to discard it (the child Run's error was dropped),
// silently killing the goroutine. record surfaces it the moment it happens and
// closes abort so a channel-blocked main wakes and exits.
//
// The sink is created eagerly (so pooled runner machines, which back native
// callbacks like test functions, share it), but channel waits stay on the plain
// path until spawned flips on the first `go` -- a program that spawns nothing
// pays nothing.
type goroutineFault struct {
	mu      sync.Mutex
	err     error         // first recorded fault, nil until one occurs
	abort   chan struct{} // closed when err is first set, to wake channel waits
	cont    bool          // policy: record+log only, don't propagate (mvm test)
	spawned atomic.Bool   // a goroutine has been started (arms channel-wait aborts)
	out     io.Writer     // where record surfaces the panic
}

func newGoroutineFault(out io.Writer, cont bool) *goroutineFault {
	return &goroutineFault{abort: make(chan struct{}), cont: cont, out: out}
}

// EnableGoroutineFaults arms goroutine-panic capture on the root machine before
// it runs, so runner machines and goroutine children share the sink. Idempotent.
func (m *Machine) EnableGoroutineFaults() {
	m.isRoot = true
	if m.fault == nil {
		m.fault = newGoroutineFault(m.err, m.faultContinue)
	}
}

// record stores the first goroutine fault, surfaces it, and closes abort. First
// fault wins, matching Go where the first panic crashes the process.
func (g *goroutineFault) record(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return
	}
	g.err = err
	_, _ = fmt.Fprintf(g.out, "panic in goroutine: %v\n", err)
	close(g.abort)
}

func (g *goroutineFault) pending() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

// SetGoroutineFaultContinue picks the policy for a later goroutine panic: false
// (default) propagates it as a non-zero exit; true records+logs only and lets
// execution continue (mvm test keeps running the suite). Call before the run.
func (m *Machine) SetGoroutineFaultContinue(v bool) {
	m.faultContinue = v
	if m.fault != nil {
		m.fault.cont = v
	}
}

// PendingGoroutineFault reports a recorded goroutine panic that should propagate
// (nil when none, or under the continue policy).
func (m *Machine) PendingGoroutineFault() error {
	if m.fault == nil || m.fault.cont {
		return nil
	}
	return m.fault.pending()
}

// GoroutineFault reports the first recorded goroutine panic regardless of policy.
// Under the continue policy the suite still runs to the end; the driver calls
// this afterward to fail the run.
func (m *Machine) GoroutineFault() error {
	if m.fault == nil {
		return nil
	}
	return m.fault.pending()
}

// chanRecv is ch.Recv(), but once a goroutine has spawned (sink armed) and the
// propagate policy is set, it also wakes on a goroutine fault and aborts the run
// -- so a main blocked on a channel whose sender died surfaces the panic instead
// of deadlocking.
func (m *Machine) chanRecv(ch reflect.Value) (reflect.Value, bool) {
	if !m.watchFault() {
		return ch.Recv()
	}
	chosen, v, ok := reflect.Select([]reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: ch},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(m.fault.abort)},
	})
	if chosen == 1 {
		panic(ErrGoroutineFault)
	}
	return v, ok
}

// chanSend is ch.Send(v) with the same fault-abort behavior as chanRecv.
func (m *Machine) chanSend(ch, v reflect.Value) {
	if !m.watchFault() {
		ch.Send(v)
		return
	}
	if chosen, _, _ := reflect.Select([]reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: ch, Send: v},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(m.fault.abort)},
	}); chosen == 1 {
		panic(ErrGoroutineFault)
	}
}

// watchFault reports whether this machine's channel waits should be
// fault-abortable: only the root (main) machine, once a goroutine has spawned and
// the policy propagates. The root wakes on any recorded fault and exits, which
// tears down any worker still blocked on a plain channel op -- so workers need not
// pay the reflect.Select cost.
func (m *Machine) watchFault() bool {
	return m.isRoot && m.fault != nil && !m.fault.cont && m.fault.spawned.Load()
}
