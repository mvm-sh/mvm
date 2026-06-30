package vm

import (
	"sync"
	"sync/atomic"
)

// activeMachine tracks the currently running Machine per goroutine, so native
// bridges (reflect dispatch, the runtime stack virtualizer) can reach it.
type machineCell struct {
	m atomic.Pointer[Machine]
}

var activeMachine sync.Map // uintptr (g pointer) -> *machineCell

// SetActiveMachine records m as the running Machine for the current
// goroutine and returns the previous value (nil if none).
func SetActiveMachine(m *Machine) (prev *Machine) {
	g := gid()
	if v, ok := activeMachine.Load(g); ok {
		cell := v.(*machineCell)
		if m == nil {
			prev = cell.m.Load()
			activeMachine.Delete(g)
			return prev
		}
		return cell.m.Swap(m)
	}
	if m == nil {
		return nil
	}
	// First Run on this goroutine; g is unique to it, so Store is race-free.
	cell := &machineCell{}
	cell.m.Store(m)
	activeMachine.Store(g, cell)
	return nil
}

// ActiveMachine returns the Machine currently set via SetActiveMachine on
// the calling goroutine, or nil if none.
func ActiveMachine() *Machine {
	if v, ok := activeMachine.Load(gid()); ok {
		return v.(*machineCell).m.Load()
	}
	return nil
}
