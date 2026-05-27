package synth

import "testing"

func TestEnabledDefaultFalse(t *testing.T) {
	t.Setenv("MVM_SYNTH", "")
	if Enabled() {
		t.Error("Enabled() = true with MVM_SYNTH empty, want false (Phase 3a sweep " +
			"surfaced compile-time rtype-capture regressions; default-on deferred)")
	}
}

func TestEnabledExplicitOptIn(t *testing.T) {
	t.Setenv("MVM_SYNTH", "1")
	if !Enabled() {
		t.Error("Enabled() = false with MVM_SYNTH=1, want true")
	}
}
