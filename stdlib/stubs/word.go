package stubs

import (
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"
)

// CoreFunc marshals one synth method call across the word boundary.
// The generated per-shape dispatcher (dispatchW*) scatters the native register
// words into pw (pointer-class words) and sw (integer-class words), invokes
// core, then gathers the result words back from rpw/rsw.
//
// pw/rpw are typed []unsafe.Pointer so the GC scans the pointer words; sw/rsw
// carry integer words as raw bits. recv is the receiver pointer per Go's
// iface-dispatch convention. The vm builds core; it does the reflect-driven
// value<->word marshaling and owns the error policy (a failed dispatch panics).
type CoreFunc = func(recv unsafe.Pointer, pw []unsafe.Pointer, sw []uint64, rpw []unsafe.Pointer, rsw []uint64)

// wordPool is one word-shape's stub-PC pool and parallel slot table.
// The generated pool_w*.go file owns the backing arrays and registers a *wordPool
// at init; acquireWordSlot claims slots monotonically like the typed shapes.
type wordPool struct {
	next  *atomic.Uint32
	cap   uint32
	stubs []uintptr
	slots []CoreFunc
	name  string
}

// wordPools maps a word-shape key (e.g. "pi_pppp") to its *wordPool.
// Populated only by registerWordPool from generated init funcs (before any
// runtime acquire), so reads are lock-free via sync.Map.
var wordPools sync.Map

// registerWordPool records a generated word-shape pool. Called from pool_w*.go
// init().
func registerWordPool(key string, p *wordPool) { wordPools.Store(key, p) }

// HasWordShape reports whether a word-shape pool exists for key, so the vm can
// drop (not error on) a method whose word-shape has no generated pool.
func HasWordShape(key string) bool {
	_, ok := wordPools.Load(key)
	return ok
}

// acquireWordSlot claims a free slot in the pool for key, storing core and
// returning the stub PC for Ifn/Tfn plus a release closure (mirrors acquireSlot
// for the typed shapes).
func acquireWordSlot(key string, core CoreFunc) (pc uintptr, release func(), err error) {
	v, ok := wordPools.Load(key)
	if !ok {
		return 0, nil, fmt.Errorf("stubs: unknown word shape %q", key)
	}
	p := v.(*wordPool)
	n := p.next.Add(1) - 1
	if n >= p.cap {
		return 0, nil, errPoolFmt(p.name, p.cap)
	}
	p.slots[n] = core
	return p.stubs[n], func() { p.slots[n] = nil }, nil
}
