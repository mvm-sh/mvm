package wordabi

import (
	"reflect"
	"strings"
	"testing"
)

// TestDropReport records one drop in each bucket and checks the report renders the
// bucket headers, keys, and example types.
func TestDropReport(t *testing.T) {
	SetDropLog(true)
	defer SetDropLog(false)

	RecordPoolDrop("zzz_pool", reflect.TypeFor[int8]())
	RecordUnsupDrop("zzz_unsup reason", reflect.TypeFor[int16]())
	RecordDegradedDrop("zzz_degraded reason", reflect.TypeFor[int32]())

	r := DropReport()
	for _, w := range []string{
		"missing pools", "zzz_pool", "int8",
		"unsupported", "zzz_unsup reason", "int16",
		"degraded", "zzz_degraded reason", "int32",
	} {
		if !strings.Contains(r, w) {
			t.Errorf("report missing %q:\n%s", w, r)
		}
	}
}

// TestDropReportSilentWhenOff returns "" with logging disabled.
func TestDropReportSilentWhenOff(t *testing.T) {
	SetDropLog(false)
	RecordPoolDrop("ignored", reflect.TypeFor[int]())
	if r := DropReport(); r != "" {
		t.Errorf("want empty report when logging off, got:\n%s", r)
	}
}
