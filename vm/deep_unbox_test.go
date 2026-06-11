package vm

import (
	"reflect"
	"testing"
	"time"
)

// Unboxing through a pointer must keep the SAME pointer (in-place), or a
// native callee mutating through it writes to a detached copy (zerolog's
// hook.Run(e) regression: e.Str appended to a clone's buf).
func TestDeepUnboxIfaceInPlace(t *testing.T) {
	type holder struct {
		V any
	}
	h := &holder{V: Iface{Typ: &Type{Rtype: reflect.TypeFor[int]()}, Val: ValueOf(42)}}

	m := &Machine{}
	w, changed := m.deepUnboxIface(reflect.ValueOf(h), 0, nil)
	if changed {
		t.Fatal("pointer arg reported changed; callers would swap in a detached copy")
	}
	if w.Pointer() != reflect.ValueOf(h).Pointer() {
		t.Fatal("pointer identity lost")
	}
	if got, ok := h.V.(int); !ok || got != 42 {
		t.Fatalf("pointee not unboxed in place: %#v", h.V)
	}
}

// Slice and map elements unbox in place too: the backing storage may be
// aliased elsewhere and must stay shared.
func TestDeepUnboxIfaceSliceMapInPlace(t *testing.T) {
	box := Iface{Typ: &Type{Rtype: reflect.TypeFor[int]()}, Val: ValueOf(7)}
	s := []any{box}
	mp := map[string]any{"k": box}

	m := &Machine{}
	if _, changed := m.deepUnboxIface(reflect.ValueOf(s), 0, nil); changed {
		t.Fatal("slice reported changed")
	}
	if got, ok := s[0].(int); !ok || got != 7 {
		t.Fatalf("slice elem not unboxed in place: %#v", s[0])
	}
	if _, changed := m.deepUnboxIface(reflect.ValueOf(mp), 0, nil); changed {
		t.Fatal("map reported changed")
	}
	if got, ok := mp["k"].(int); !ok || got != 7 {
		t.Fatalf("map value not unboxed in place: %#v", mp["k"])
	}
}

// Cyclic pointer graphs (e.g. testing.T parent/sub links) must not blow up
// into an exponential re-walk bounded only by maxUnboxDepth.
func TestDeepUnboxIfaceCycle(t *testing.T) {
	type node struct {
		A, B *node
		V    any
	}
	n1, n2 := &node{}, &node{}
	n1.A, n1.B = n2, n2
	n2.A, n2.B = n1, n1

	m := &Machine{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.deepUnboxIface(reflect.ValueOf(n1), 0, nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deepUnboxIface did not terminate on a cyclic graph")
	}
}
