package vm

import (
	"reflect"
	"runtime"
	"testing"
	"time"
	"weak"
)

// A method value stored into a native struct func-field forms a cycle
// (field -> wrapper -> mvm func -> receiver -> field). buildCallFunc must
// register its funcFields entry weakly so the entry does not pin that graph;
// otherwise every bridged field leaks its receiver for the interpreter's life
// (the blackfriday New()-per-Run leak). This asserts the captured graph is
// collectable once the wrapper is dropped, and that get then misses.
func TestFuncFieldWeakNoRetain(t *testing.T) {
	m := &Machine{}

	sentinel := new([4096]byte)
	cl := Closure{Code: 0, Heap: []*Value{{ref: reflect.ValueOf(sentinel)}}}
	fval := Value{ref: reflect.ValueOf(cl)}

	w := m.buildCallFunc(fval, reflect.TypeOf(func() {}))
	key := funcDataPtr(w)
	if _, ok := m.funcFields.get(key); !ok {
		t.Fatal("buildCallFunc did not register a funcFields entry")
	}

	ws := weak.Make(sentinel)

	collected := false
	for range 10 {
		runtime.GC()
		if ws.Value() == nil {
			collected = true
			break
		}
	}
	if !collected {
		t.Fatal("receiver graph retained: funcFields entry pins it (not weak?)")
	}
	if _, ok := m.funcFields.get(key); ok {
		t.Error("get returned a hit after the wrapper was collected")
	}
}

// A strong funcFields entry outlives its wrapper, so a recycled funcval
// address must be re-registered, not skipped: the old skip-guard resolved a
// new wrapper to a dead wrapper's closure (quicktest checkParams.fail calling
// a finished test's t.Fatal in spf13/cast). Live weak entries are kept: their
// referent pins the address, so the mapping is still the live funcval's.
func TestFuncFieldSetStrongKeep(t *testing.T) {
	tbl := newFuncFieldsTable()
	p := uintptr(0x1000)
	v1 := Value{num: 1}
	v2 := Value{num: 2}

	// Strong entry at a recycled address: must be overwritten.
	tbl.setStrongKeep(p, v1)
	tbl.setStrongKeep(p, v2)
	if got, _ := tbl.get(p); got.num != 2 {
		t.Errorf("strong entry not overwritten: got num=%d, want 2", got.num)
	}

	// Live weak entry: must be kept (self-pruning form).
	ref := &funcRef{val: v1}
	tbl.setWeak(p, ref)
	tbl.setStrongKeep(p, v2)
	if got, _ := tbl.get(p); got.num != 1 {
		t.Errorf("live weak entry replaced: got num=%d, want 1", got.num)
	}
	runtime.KeepAlive(ref)

	// Dead weak entry: must be overwritten.
	for range 10 {
		runtime.GC()
		if _, ok := tbl.get(p); !ok {
			break
		}
	}
	if _, ok := tbl.get(p); ok {
		t.Skip("weak ref not collected; cannot exercise the dead-weak case")
	}
	tbl.setStrongKeep(p, v2)
	if got, ok := tbl.get(p); !ok || got.num != 2 {
		t.Errorf("dead-weak entry not overwritten: got num=%d ok=%v, want 2 true", got.num, ok)
	}
}

// A strong entry must die with its funcval: cleanupStrong attaches a runtime
// cleanup that prunes it, so a funcval later recycled to the same address can
// never observe a dead wrapper's entry (and the entry stops pinning the
// closure graph). The generation guard makes a predecessor's cleanup skip a
// successor registration at the same address.
func TestFuncFieldStrongCleanup(t *testing.T) {
	tbl := newFuncFieldsTable()

	fn := reflect.MakeFunc(reflect.TypeOf(func() {}), func([]reflect.Value) []reflect.Value { return nil })
	slot := reflect.New(fn.Type()).Elem()
	slot.Set(fn)
	fp := funcValueUnsafe(slot)
	p := uintptr(fp)

	gen := tbl.setStrongKeep(p, Value{num: 7})
	if _, ok := tbl.get(p); !ok {
		t.Fatal("entry missing after registration")
	}

	// A stale-generation prune must not delete a successor registration.
	gen2 := tbl.setStrongKeep(p, Value{num: 8})
	tbl.pruneStrong(p, gen)
	if got, ok := tbl.get(p); !ok || got.num != 8 {
		t.Fatalf("predecessor cleanup deleted successor: got num=%d ok=%v", got.num, ok)
	}

	// Dropping the funcval must prune the entry via the runtime cleanup.
	// fn is dead after the KeepAlive; slot still pins the funcval until zeroed.
	tbl.cleanupStrong(fp, gen2)
	runtime.KeepAlive(fn)
	slot.Set(reflect.Zero(slot.Type()))
	pruned := false
	for range 100 {
		runtime.GC()
		time.Sleep(time.Millisecond)
		if _, ok := tbl.get(p); !ok {
			pruned = true
			break
		}
	}
	if !pruned {
		t.Error("strong entry not pruned after funcval collection")
	}
}
