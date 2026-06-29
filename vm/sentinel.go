package vm

import (
	"io"
	"reflect"
)

var nativeIoEOF any = io.EOF

// Interpreted io (wasm, or MVM_INTERP=io) builds its own io.EOF via errors.New,
// a distinct pointer from the host io.EOF the native floor returns, so
// `err == io.EOF` fails across the boundary.
// eofSlot is that interpreted io.EOF's global slot; nil means io is bridged and
// there is nothing to reconcile.
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

// io's init writes the slot before any reader runs, so a lazy read is safe.
func (m *Machine) interpEOF() reflect.Value {
	return m.globals[m.sentinels.eofSlot].Reflect()
}

// collectReturns delivers a returned error unwrapped, so match against concrete.
func (m *Machine) interpEOFConcrete() error {
<<<<<<< HEAD
	e, _ := m.globals[m.sentinels.eofSlot].Interface().(error)
=======
	g := m.globals[m.sentinels.eofSlot]
	if g.IsIface() {
		g = g.IfaceVal().Val
	}
	e, _ := g.Reflect().Interface().(error)
>>>>>>> 694836c16db5967330372b301fdf32e788dc2f91
	return e
}

// Counterpart of isInterpEOFReturn, for the native->interp direction.
<<<<<<< HEAD
// Caller gates on m.sentinels != nil.
func (m *Machine) canonNativeReturns(out []reflect.Value) {
=======
func (m *Machine) canonNativeReturns(out []reflect.Value) {
	if m.sentinels == nil {
		return
	}
>>>>>>> 694836c16db5967330372b301fdf32e788dc2f91
	for i, v := range out {
		if v.Kind() == reflect.Interface && !v.IsNil() && v.Interface() == nativeIoEOF {
			out[i] = m.interpEOF()
		}
	}
}

// Must run before bridgeIface: on wasm errors is interpreted, so the synth EOF
// gets wrapped in a synthErrShim that hides its identity.
<<<<<<< HEAD
// Caller gates on m.sentinels != nil.
func (m *Machine) isInterpEOFReturn(v Value) bool {
	if !v.IsValid() {
=======
func (m *Machine) isInterpEOFReturn(v Value) bool {
	if m.sentinels == nil || !v.IsValid() {
>>>>>>> 694836c16db5967330372b301fdf32e788dc2f91
		return false
	}
	if v.IsIface() {
		v = v.IfaceVal().Val
	}
	rv := v.Reflect()
	if !rv.IsValid() || !rv.Type().Implements(errorIface) || !rv.CanInterface() {
		return false
	}
	e, _ := rv.Interface().(error)
	return e != nil && e == m.interpEOFConcrete() //nolint:errorlint // sentinel identity, not a wrap
}
