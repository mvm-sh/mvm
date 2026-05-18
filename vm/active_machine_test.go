package vm

import (
	"sync"
	"testing"
)

// BenchmarkSetActiveMachine measures the per-call cost of the
// gid + sync.Map pair that Machine.Run pays on entry and exit. The
// number divided by 2 is what each Run round-trip pays beyond the
// pre-fix atomic.Pointer.Swap.
func BenchmarkSetActiveMachine(b *testing.B) {
	m := &Machine{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prev := SetActiveMachine(m)
		SetActiveMachine(prev)
	}
}

// BenchmarkActiveMachineLoad measures the read-only path used by the
// runtime.Callers bridge.
func BenchmarkActiveMachineLoad(b *testing.B) {
	m := &Machine{}
	prev := SetActiveMachine(m)
	defer SetActiveMachine(prev)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ActiveMachine()
	}
}

// TestGidUniquePerGoroutine guards the per-arch fast-path assumption
// that gid() returns a distinct uintptr per concurrently-running
// goroutine. If a future Go release changes the register convention
// (e.g. moves g off R14 on amd64), the assembly would silently return
// garbage and every goroutine would alias the same Machine slot --
// exactly the bug the per-goroutine keying was added to prevent.
//
// Uniqueness only holds for goroutines that are alive at the same
// time: Go recycles *g structs after a goroutine ends, so a naive
// "spawn N goroutines and collect gids" test legitimately trips on
// reuse. Workers block on a release channel until all gids are
// collected and checked, guaranteeing every worker's *g is still live.
func TestGidUniquePerGoroutine(t *testing.T) {
	const n = 32
	ids := make([]uintptr, n)
	var ready sync.WaitGroup
	release := make(chan struct{})
	defer close(release)
	ready.Add(n)
	for i := 0; i < n; i++ {
		go func(j int) {
			ids[j] = gid()
			ready.Done()
			<-release
		}(i)
	}
	ready.Wait()
	seen := make(map[uintptr]int, n)
	for i, id := range ids {
		if prev, dup := seen[id]; dup {
			t.Fatalf("gid collision: goroutine %d and %d both got id %#x (register convention may have changed)", prev, i, id)
		}
		seen[id] = i
	}
	if gid() == 0 {
		t.Fatalf("gid() returned 0 for the test goroutine; register read returned uninitialized memory")
	}
}
