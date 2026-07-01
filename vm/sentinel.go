package vm

import (
	"maps"
	"reflect"
)

// Interpreted io/io-fs (wasm, or MVM_INTERP) mint their own io.EOF and oserror
// sentinels via errors.New, distinct pointers from the host ones the native floor
// (os/syscall) returns and compares, so `err == io.EOF` and syscall.Errno.Is
// fail across the boundary. sentinelTable records each interpreted sentinel's
// global slot by name; nil means those packages are bridged, nothing to
// reconcile. The reconciliation lives in stdlib (which owns the native
// identities); vm stores the slots and invokes the hooks at the boundary.
type sentinelTable struct {
	slots map[string]int
}

// SetInterpSentinelSlot enables canonicalization; slot is the named interpreted
// sentinel's global, supplied by the interpreter once its package is compiled.
// Copy-on-write: pooled runners share the *sentinelTable pointer, so a later
// registration must publish a fresh table rather than mutate the shared map.
func (m *Machine) SetInterpSentinelSlot(name string, slot int) {
	next := &sentinelTable{slots: make(map[string]int, 8)}
	if m.sentinels != nil {
		maps.Copy(next.slots, m.sentinels.slots)
	}
	next.slots[name] = slot
	m.sentinels = next
}

// SentinelConfigured reports whether name's slot is already registered.
func (m *Machine) SentinelConfigured(name string) bool {
	if m.sentinels == nil {
		return false
	}
	_, ok := m.sentinels.slots[name]
	return ok
}

// InterpSentinelValue returns the interpreted sentinel, or an invalid Value when
// its package is bridged. init writes the slot before any use, so a lazy read is safe.
func (m *Machine) InterpSentinelValue(name string) Value {
	if m.sentinels == nil {
		return Value{}
	}
	slot, ok := m.sentinels.slots[name]
	if !ok {
		return Value{}
	}
	return m.globals[slot]
}

// Native-boundary sentinel reconciliation, set by stdlib (which owns the native
// sentinels). canonReturns rewrites a returned native io.EOF to the interpreted
// copy; mapInterp maps an interpreted sentinel (io.EOF, an oserror sentinel) to
// its host value for a native sink or a native-call arg. Both fire only when
// m.sentinels != nil.
var (
	sentinelCanonReturns func(m *Machine, out []reflect.Value)
	sentinelMapInterp    func(m *Machine, v Value) (reflect.Value, bool)
)

// RegisterSentinelHooks installs the boundary reconciliation.
func RegisterSentinelHooks(
	canonReturns func(*Machine, []reflect.Value),
	mapInterp func(*Machine, Value) (reflect.Value, bool),
) {
	sentinelCanonReturns = canonReturns
	sentinelMapInterp = mapInterp
}
