package vm

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/mtype"
)

// An Iface map key (e.g. protoreflect MapKey.Interface() across the native
// boundary) must unwrap to its native concrete, else the boxed Iface struct is a
// distinct eface that never collides -- dynamicpb maps grew dup keys (TestMerge).
func TestMapKeyUnboxesIface(t *testing.T) {
	m := &Machine{}
	mp := reflect.MakeMap(reflect.TypeFor[map[any]int]())
	keyType := mp.Type().Key()

	box := Iface{Typ: &mtype.Type{Rtype: reflect.TypeFor[string]()}, Val: ValueOf("ab")}
	ifaceKey := Value{ref: reflect.ValueOf(box)}
	if !ifaceKey.IsIface() {
		t.Fatal("ifaceKey should be an Iface")
	}

	// Root cause: the raw path stores the Iface struct, not the native string.
	if raw := mapKeyReflect(keyType, ifaceKey); raw.Type() != ifaceRtype {
		t.Fatalf("mapKeyReflect = %v, want the boxed Iface struct (the bug mapKey fixes)", raw.Type())
	}

	mp.SetMapIndex(m.mapKey(keyType, ifaceKey), reflect.ValueOf(1))
	mp.SetMapIndex(m.mapKey(keyType, ValueOf("ab")), reflect.ValueOf(2))
	if got := mp.Len(); got != 1 {
		t.Fatalf("map len = %d, want 1: Iface key did not collide with literal string key", got)
	}
	if got := mp.MapIndex(reflect.ValueOf("ab")).Interface(); got != 2 {
		t.Fatalf("map[ab] = %v, want 2", got)
	}
}
