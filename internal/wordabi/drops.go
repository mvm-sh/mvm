package wordabi

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// dropLog gates drop recording, from MVM_WORDDROPS (see ADR-022). Atomic only so a
// test can toggle it without racing attaches.
var dropLog atomic.Bool

func init() { dropLog.Store(os.Getenv("MVM_WORDDROPS") != "") }

// SetDropLog toggles drop recording; tests use it to exercise DropReport without
// the env var.
func SetDropLog(v bool) { dropLog.Store(v) }

type wordDrop struct {
	count   atomic.Int64
	example atomic.Pointer[string]
}

// dropPools keys actionable drops by word-shape (no pool generated yet);
// dropUnsup keys drops needing floats or a bigger budget, by reason;
// dropDegraded keys methods attached via their erased typed shape because the
// precise word-shape was unavailable.
var (
	dropPools    sync.Map // shape key -> *wordDrop
	dropUnsup    sync.Map // reason    -> *wordDrop
	dropDegraded sync.Map // shape key or reason -> *wordDrop
)

func record(m *sync.Map, bucket string, sig reflect.Type) {
	if !dropLog.Load() {
		return
	}
	v, _ := m.LoadOrStore(bucket, &wordDrop{})
	d := v.(*wordDrop)
	d.count.Add(1)
	if d.example.Load() == nil {
		s := sig.String()
		d.example.CompareAndSwap(nil, &s)
	}
}

// RecordPoolDrop records a classifiable signature with no generated pool, keyed by
// its word-shape.
func RecordPoolDrop(key string, sig reflect.Type) { record(&dropPools, key, sig) }

// RecordUnsupDrop records an unclassifiable signature (needs floats or a bigger
// budget), keyed by reason.
func RecordUnsupDrop(reason string, sig reflect.Type) { record(&dropUnsup, reason, sig) }

// RecordDegradedDrop records a method attached via its erased typed shape because
// the precise word-shape was unavailable.
func RecordDegradedDrop(reason string, sig reflect.Type) { record(&dropDegraded, reason, sig) }

// DropReport summarizes the word-shapes dropped this process, or "" when
// MVM_WORDDROPS is unset or nothing dropped.
func DropReport() string {
	if !dropLog.Load() {
		return ""
	}
	pools := sortedDrops(&dropPools)
	unsup := sortedDrops(&dropUnsup)
	degraded := sortedDrops(&dropDegraded)
	if len(pools) == 0 && len(unsup) == 0 && len(degraded) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("mvm word-shape drops (MVM_WORDDROPS):\n")
	if len(pools) > 0 {
		b.WriteString("  missing pools -- add to internal/stubs/gen_pools.go wordShapes:\n")
		for _, e := range pools {
			fmt.Fprintf(&b, "    %-10s %7d  %s\n", e.bucket, e.count, e.example)
		}
	}
	if len(unsup) > 0 {
		b.WriteString("  unsupported -- need float words or a larger word budget:\n")
		for _, e := range unsup {
			fmt.Fprintf(&b, "    %-26s %7d  %s\n", e.bucket, e.count, e.example)
		}
	}
	if len(degraded) > 0 {
		b.WriteString("  degraded -- attached via the ERASED typed shape (synth-iface params published as any):\n")
		for _, e := range degraded {
			fmt.Fprintf(&b, "    %-26s %7d  %s\n", e.bucket, e.count, e.example)
		}
	}
	return b.String()
}

type dropEntry struct {
	bucket  string
	count   int64
	example string
}

// sortedDrops snapshots a collector, most-dropped first (ties broken by bucket).
func sortedDrops(m *sync.Map) []dropEntry {
	var es []dropEntry
	m.Range(func(k, v any) bool {
		d := v.(*wordDrop)
		ex := ""
		if p := d.example.Load(); p != nil {
			ex = *p
		}
		es = append(es, dropEntry{k.(string), d.count.Load(), ex})
		return true
	})
	sort.Slice(es, func(i, j int) bool {
		if es[i].count != es[j].count {
			return es[i].count > es[j].count
		}
		return es[i].bucket < es[j].bucket
	})
	return es
}
