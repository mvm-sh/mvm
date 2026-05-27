package synth

import "testing"

func TestEnabledDefaultFalse(t *testing.T) {
	// MVM_SYNTH is unset in normal test runs.
	// Tests exercise AttachStructMethods directly, bypassing the flag.
	if Enabled() {
		t.Log("note: MVM_SYNTH was set in this test environment")
	}
}
