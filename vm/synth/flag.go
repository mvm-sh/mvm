package synth

import "os"

// Enabled reports whether the synth-rtype path is selected for this process.
// Default is OFF: the Phase-3a "default-on" sweep surfaced regressions
// rooted in compile-time rtype captures (allocGlobalSlots,
// reflect.PointerTo/SliceOf/MapOf-derived rtypes in c.Data and bytecode
// immediates) that synth's t.Rtype replacement doesn't refresh.
// Closing those gaps is on the path to Phase 3b.
// Set MVM_SYNTH=1 to opt in for synth-only test runs.
// The env var is read on every call so tests using t.Setenv take effect.
// Cost is negligible because callers (interp/synth.go, vm.AttachSynthMethods)
// gate once per Eval, not per hot-path op.
func Enabled() bool {
	return os.Getenv("MVM_SYNTH") == "1"
}
