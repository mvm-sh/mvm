package vm

import (
	"reflect"
	"strings"
	"testing"
)

// TestWordShapeDropReport checks that, with logging enabled, detectWordShape
// records a classifiable-but-poolless signature under "missing pools" and an
// unclassifiable one under "unsupported".
func TestWordShapeDropReport(t *testing.T) {
	if !wordShapesSupported {
		t.Skip("word shapes need a 64-bit little-endian target")
	}
	wordDropLog.Store(true)
	defer wordDropLog.Store(false)

	// "iii_i" has no generated pool; a float param is unclassifiable. Both use
	// signatures no real method has, so the assertions are immune to drops other
	// tests record into the shared collectors.
	noPool := reflect.TypeOf((func(uintptr, uintptr, uintptr) bool)(nil))
	if _, ok := detectWordShape(noPool); ok {
		t.Fatalf("expected %v to drop (no pool)", noPool)
	}
	hasFloat := reflect.TypeOf((func(float64) bool)(nil))
	if _, ok := detectWordShape(hasFloat); ok {
		t.Fatalf("expected %v to drop (float)", hasFloat)
	}

	r := WordShapeDropReport()
	for _, want := range []string{"missing pools", "iii_i", "uintptr", "unsupported"} {
		if !strings.Contains(r, want) {
			t.Errorf("report missing %q:\n%s", want, r)
		}
	}
}
