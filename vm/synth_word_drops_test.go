package vm

import (
	"reflect"
	"strings"
	"testing"
)

// TestWordShapeDropReport checks that, with logging enabled, detectWordShape
// records a classifiable-but-poolless signature under "missing pools" and (on the
// register ABI) an unclassifiable one under "unsupported". The wasm/ABI0 classifier
// carries every type as stack bytes, so it has no unclassifiable case -- only
// missing-pool drops.
func TestWordShapeDropReport(t *testing.T) {
	if !wordShapesSupported {
		t.Skip("word shapes need a 64-bit little-endian target")
	}
	wordDropLog.Store(true)
	defer wordDropLog.Store(false)

	// "iii_i" has no generated pool on either arch. It uses a signature no real
	// method has, so the assertion is immune to drops other tests record into the
	// shared collectors.
	noPool := reflect.TypeOf((func(uintptr, uintptr, uintptr) bool)(nil))
	if _, ok := detectWordShape(noPool); ok {
		t.Fatalf("expected %v to drop (no pool)", noPool)
	}
	want := []string{"missing pools", "iii_i", "uintptr"}

	// An array param (length > 1) is stack-passed and unclassifiable on the
	// register ABI; on wasm it packs to stack bytes, so the unsupported bucket
	// stays empty there. (float32 now classifies on both arches.)
	if !wordABI0 {
		unclassifiable := reflect.TypeOf((func([2]int) bool)(nil))
		if _, ok := detectWordShape(unclassifiable); ok {
			t.Fatalf("expected %v to drop (unclassifiable)", unclassifiable)
		}
		want = append(want, "unsupported")
	}

	r := WordShapeDropReport()
	for _, w := range want {
		if !strings.Contains(r, w) {
			t.Errorf("report missing %q:\n%s", w, r)
		}
	}
}
