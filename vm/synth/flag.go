package synth

import "os"

// Enabled reports whether the synth-rtype path is selected for this process.
// The env var is read on every call so tests using t.Setenv take effect.
// Cost is negligible because callers (interp/synth.go, vm.AttachSynthMethods)
// gate once per Eval, not per hot-path op.
func Enabled() bool {
	return os.Getenv("MVM_SYNTH") != ""
}
