package vm

import "reflect"

// Interpreted io (wasm, or MVM_INTERP=io) builds its own io.EOF via errors.New,
// a distinct pointer from the host io.EOF the native floor returns, so
// `err == io.EOF` fails across the boundary. sentinelTable records the global
// slot of that interpreted io.EOF; nil means io is bridged and there is nothing
// to reconcile. The reconciliation itself lives in stdlib (it owns the io.EOF
// identity); vm only stores the slot and invokes the hooks at the boundary.
type sentinelTable struct {
	eofSlot int
}

// SetInterpEOFSlot enables canonicalization; slot is the interpreted io.EOF's
// global, supplied by the interpreter once io is compiled.
func (m *Machine) SetInterpEOFSlot(slot int) {
	m.sentinels = &sentinelTable{eofSlot: slot}
}

// SentinelsConfigured guards configureSentinels against re-running per Eval.
func (m *Machine) SentinelsConfigured() bool { return m.sentinels != nil }

// InterpEOFValue returns the interpreted io.EOF, or an invalid Value when io is
// bridged. io's init writes the slot before any reader runs, so a lazy read is safe.
func (m *Machine) InterpEOFValue() Value {
	if m.sentinels == nil {
		return Value{}
	}
	return m.globals[m.sentinels.eofSlot]
}

// Native-boundary sentinel reconciliation, set by stdlib (which owns io.EOF).
// canonReturns rewrites a returned native io.EOF to the interpreted copy;
// mapInterpReturn maps an interpreted io.EOF back to the host io.EOF for a
// native sink. Both fire only when m.sentinels != nil.
var (
	sentinelCanonReturns    func(m *Machine, out []reflect.Value)
	sentinelMapInterpReturn func(m *Machine, v Value) (reflect.Value, bool)
)

// RegisterSentinelHooks installs the boundary reconciliation.
func RegisterSentinelHooks(
	canonReturns func(*Machine, []reflect.Value),
	mapInterpReturn func(*Machine, Value) (reflect.Value, bool),
) {
	sentinelCanonReturns = canonReturns
	sentinelMapInterpReturn = mapInterpReturn
}
