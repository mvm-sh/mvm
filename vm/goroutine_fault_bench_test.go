package vm

import (
	"io"
	"reflect"
	"testing"
)

// BenchmarkChanRecvFaultOverhead measures what the abort-aware channel path costs
// real code: m.chanRecv with the fault sink disarmed (no goroutine spawned, the
// plain recv) vs armed (a goroutine spawned, the reflect.Select wakeup). A closed
// channel is always ready, isolating the per-op overhead.
func BenchmarkChanRecvFaultOverhead(b *testing.B) {
	ch := make(chan int)
	close(ch)
	cv := reflect.ValueOf(ch)

	b.Run("FaultOff", func(b *testing.B) {
		m := &Machine{} // no sink -> plain ch.Recv()
		b.ReportAllocs()
		for b.Loop() {
			m.chanRecv(cv)
		}
	})
	b.Run("FaultOn", func(b *testing.B) {
		m := &Machine{isRoot: true}
		m.fault = newGoroutineFault(io.Discard, false)
		m.fault.spawned.Store(true) // armed -> abort-aware reflect.Select
		b.ReportAllocs()
		for b.Loop() {
			m.chanRecv(cv)
		}
	})
}
