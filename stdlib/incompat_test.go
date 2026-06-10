package stdlib

import "testing"

func TestSkipReason(t *testing.T) {
	// Spot-check one entry. Don't pin the exact reason string -- the wording
	// changes; we just want a non-empty return so the driver's skip path fires.
	if got := SkipReason("flag", "TestDefineAfterSet"); got == "" {
		t.Error("flag/TestDefineAfterSet missing from Incompat")
	}
	if got := SkipReason("flag", "NoSuchTest"); got != "" {
		t.Errorf("unknown test should not match, got %q", got)
	}
	if got := SkipReason("nonexistent/pkg", "TestFoo"); got != "" {
		t.Errorf("unknown pkg should not match, got %q", got)
	}
}

func TestIsGenericOnly(t *testing.T) {
	if !IsGenericOnly("crypto/hkdf") {
		t.Error("crypto/hkdf should be a generic-only stub")
	}
	if IsGenericOnly("crypto/hmac") {
		t.Error("crypto/hmac is bridged, not a generic-only stub")
	}
}
