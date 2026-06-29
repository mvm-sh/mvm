package vm

import (
	"reflect"
	"testing"
	"time"

	"github.com/mvm-sh/mvm/mtype"
)

// Unboxing through a pointer must keep the SAME pointer (in-place), or a
// native callee mutating through it writes to a detached copy (zerolog's
// hook.Run(e) regression: e.Str appended to a clone's buf).
func TestDeepUnboxIfaceInPlace(t *testing.T) {
	type holder struct {
		V any
	}
	h := &holder{V: Iface{Typ: &mtype.Type{Rtype: reflect.TypeFor[int]()}, Val: ValueOf(42)}}

	m := &Machine{}
	w, changed := m.deepUnboxIface(reflect.ValueOf(h), 0, 0, nil)
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
	box := Iface{Typ: &mtype.Type{Rtype: reflect.TypeFor[int]()}, Val: ValueOf(7)}
	s := []any{box}
	mp := map[string]any{"k": box}

	m := &Machine{}
	if _, changed := m.deepUnboxIface(reflect.ValueOf(s), 0, 0, nil); changed {
		t.Fatal("slice reported changed")
	}
	if got, ok := s[0].(int); !ok || got != 7 {
		t.Fatalf("slice elem not unboxed in place: %#v", s[0])
	}
	if _, changed := m.deepUnboxIface(reflect.ValueOf(mp), 0, 0, nil); changed {
		t.Fatal("map reported changed")
	}
	if got, ok := mp["k"].(int); !ok || got != 7 {
		t.Fatalf("map value not unboxed in place: %#v", mp["k"])
	}
}

// A map past maxMapHops is left boxed: it belongs to a shared graph whose
// concurrent writer an iteration would race.
func TestDeepUnboxIfaceDeepMapSkipped(t *testing.T) {
	box := Iface{Typ: &mtype.Type{Rtype: reflect.TypeFor[int]()}, Val: ValueOf(7)}
	type inner struct{ M map[string]any }
	type outer struct{ I *inner }
	// arg (*outer): hop 1 -> outer.I (*inner): hop 2 -> inner.M past maxMapHops.
	o := &outer{I: &inner{M: map[string]any{"k": box}}}

	m := &Machine{}
	if _, changed := m.deepUnboxIface(reflect.ValueOf(o), 0, 0, nil); changed {
		t.Fatal("deep pointer arg reported changed")
	}
	if _, stillBoxed := o.I.M["k"].(Iface); !stillBoxed {
		t.Fatalf("deep map was iterated; want skipped: %#v", o.I.M["k"])
	}
}

// hops counts slice edges too: a map behind a pointer AND a slice is past
// maxMapHops, else the concurrent-map fatal recurs through non-pointer nodes.
func TestDeepUnboxIfaceSliceOfMapsSkipped(t *testing.T) {
	box := Iface{Typ: &mtype.Type{Rtype: reflect.TypeFor[int]()}, Val: ValueOf(7)}
	type outer struct{ S []map[string]any }
	// arg (*outer): hop 1 -> outer.S slice: hop 2 -> elem map past maxMapHops.
	o := &outer{S: []map[string]any{{"k": box}}}

	m := &Machine{}
	if _, changed := m.deepUnboxIface(reflect.ValueOf(o), 0, 0, nil); changed {
		t.Fatal("deep slice-of-maps arg reported changed")
	}
	if _, stillBoxed := o.S[0]["k"].(Iface); !stillBoxed {
		t.Fatalf("map behind a slice was iterated; want skipped: %#v", o.S[0]["k"])
	}
}

// A slice of maps passed directly is within maxMapHops, so its boxes unbox.
func TestDeepUnboxIfaceDirectSliceOfMapsUnboxed(t *testing.T) {
	box := Iface{Typ: &mtype.Type{Rtype: reflect.TypeFor[int]()}, Val: ValueOf(7)}
	s := []map[string]any{{"k": box}}

	m := &Machine{}
	m.deepUnboxIface(reflect.ValueOf(s), 0, 0, nil)
	if got, ok := s[0]["k"].(int); !ok || got != 7 {
		t.Fatalf("direct slice-of-maps not unboxed: %#v", s[0]["k"])
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
		m.deepUnboxIface(reflect.ValueOf(n1), 0, 0, nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deepUnboxIface did not terminate on a cyclic graph")
	}
}
