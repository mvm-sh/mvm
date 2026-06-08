package vm

import (
	"reflect"
	"runtime"
	"testing"
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
