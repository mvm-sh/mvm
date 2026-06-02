package vm

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// wordDropLog gates detectWordShape drop recording, from MVM_WORDDROPS (see
// ADR-022). Atomic only so a test can toggle it without racing attaches.
var wordDropLog atomic.Bool

func init() { wordDropLog.Store(os.Getenv("MVM_WORDDROPS") != "") }

type wordDrop struct {
	count   atomic.Int64
	example atomic.Pointer[string]
}

// wordDropPools keys actionable drops by word-shape (no pool generated yet);
// wordDropUnsup keys drops needing floats or a bigger budget, by reason.
var (
	wordDropPools sync.Map // shape key -> *wordDrop
	wordDropUnsup sync.Map // reason    -> *wordDrop
)

func recordWordDrop(m *sync.Map, bucket string, sig reflect.Type) {
	if !wordDropLog.Load() {
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

// WordShapeDropReport summarizes the word-shapes detectWordShape dropped this
// process, or "" when MVM_WORDDROPS is unset or nothing dropped.
func WordShapeDropReport() string {
	if !wordDropLog.Load() {
		return ""
	}
	pools := sortedDrops(&wordDropPools)
	unsup := sortedDrops(&wordDropUnsup)
	if len(pools) == 0 && len(unsup) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("mvm word-shape drops (MVM_WORDDROPS):\n")
	if len(pools) > 0 {
		b.WriteString("  missing pools -- add to stdlib/stubs/gen_pools.go wordShapes:\n")
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
