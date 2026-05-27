package synth

import "testing"

func TestEnabledDefaultFalse(t *testing.T) {
	t.Setenv("MVM_SYNTH", "")
	if Enabled() {
		t.Error("Enabled() = true with MVM_SYNTH empty, want false")
	}
}

func TestEnabledTrue(t *testing.T) {
	t.Setenv("MVM_SYNTH", "1")
	if !Enabled() {
		t.Error("Enabled() = false with MVM_SYNTH=1, want true")
	}
}
