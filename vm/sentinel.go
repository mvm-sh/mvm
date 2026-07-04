package vm

import (
	"maps"
	"reflect"
)

// Interpreted io/io-fs (wasm, or MVM_INTERP) mint their own io.EOF and oserror
// sentinels via errors.New, distinct pointers from the host ones the native floor
// (os/syscall) returns and compares, so `err == io.EOF` and syscall.Errno.Is
// fail across the boundary.
// sentinelTable records each interpreted sentinel's global slot by name; nil
// means those packages are bridged, nothing to reconcile.
// hosts pairs a slot with its host twin until healSentinels converges the
// global on it (via the HealSentinels op the interpreter emits after var-inits).
type sentinelTable struct {
	slots map[string]int
	hosts map[int]reflect.Value
}

func (s *sentinelTable) clone() *sentinelTable {
	return &sentinelTable{slots: maps.Clone(s.slots), hosts: maps.Clone(s.hosts)}
}

// SetInterpSentinelSlot enables canonicalization; slot is the named interpreted
// sentinel's global, supplied by the interpreter once its package is compiled.
// Copy-on-write: pooled runners share the *sentinelTable pointer, so a later
// registration must publish a fresh table rather than mutate the shared map.
func (m *Machine) SetInterpSentinelSlot(name string, slot int) {
	var next *sentinelTable
	if m.sentinels != nil {
		next = m.sentinels.clone()
	} else {
		next = &sentinelTable{slots: map[string]int{}, hosts: map[int]reflect.Value{}}
	}
	next.slots[name] = slot
	if sentinelHostValue != nil {
		if hv, ok := sentinelHostValue(name); ok {
			next.hosts[slot] = hv
		}
	}
	m.sentinels = next
}

// healSentinels overwrites each initialized sentinel global with its host twin,
// so both copies are one pointer on every path, including ones no boundary hook
// sees (a bridged filepath.SkipDir global read compared to the interpreted
// fs.SkipDir).
// Runs via the HealSentinels op after each var-init group and before main:
// off any hot path, and before user goroutines can read the slots.
// Writing through promoted storage keeps an earlier &sentinel alias valid.
// Healed mappings are dropped so a later deliberate reassignment sticks;
// a slot whose var-init has not run yet stays armed for the next sweep.
func (m *Machine) healSentinels() {
	s := m.sentinels
	if s == nil || len(s.hosts) == 0 {
		return
	}
	next := s.clone()
	for slot, hv := range s.hosts {
		g := &m.globals[slot]
		if nilEqual(*g) {
			continue
		}
		if g.ref.IsValid() && g.ref.CanSet() && g.ref.Kind() == reflect.Interface {
			g.ref.Set(hv)
		} else {
			g.ref = hv
		}
		g.num = 0
		delete(next.hosts, slot)
	}
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
// sentinels); both fire only when m.sentinels != nil.
// mapInterp maps a pre-heal interpreted sentinel to its host value for a native
// sink or a native-call arg; post-heal it is an identity no-op.
// hostValue returns name's host sentinel, recorded at slot registration and
// consumed by healSentinels.
var (
	sentinelMapInterp func(m *Machine, v Value) (reflect.Value, bool)
	sentinelHostValue func(name string) (reflect.Value, bool)
)

// RegisterSentinelHooks installs the boundary reconciliation.
func RegisterSentinelHooks(
	mapInterp func(*Machine, Value) (reflect.Value, bool),
	hostValue func(string) (reflect.Value, bool),
) {
	sentinelMapInterp = mapInterp
	sentinelHostValue = hostValue
}
