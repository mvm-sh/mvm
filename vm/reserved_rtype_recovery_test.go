package vm

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/derive"
	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/runtype"
)

// A value-only interpreted type (its *Type never globalized) that round-tripped
// through native reflect must still be recoverable from the reservation registry,
// so a type assertion / type switch can consult mvm's method tables. This guards
// the typeByRtype fallback that fixes x/net/http2 TestServerHandleCustomConn
// (connStateConn went native; its ConnectionState() tls.ConnectionState method has
// no word-shape, and the *Type was absent from globals).
func TestTypeByRtypeReservationFallback(t *testing.T) {
	layout := reflect.StructOf([]reflect.StructField{{Name: "X", Type: reflect.TypeFor[int]()}})
	vr, err := runtype.ReserveMethods(layout, "main.valueOnlyConn", "")
	if err != nil {
		t.Fatalf("ReserveMethods: %v", err)
	}
	rt := vr.Type()
	if !runtype.IsSynth(rt) {
		t.Fatal("reserved rtype should be synth")
	}

	typ := &mtype.Type{Name: "valueOnlyConn", Rtype: rt}
	derive.SetValueReservation(typ, vr)
	defer derive.DeleteReservation(typ)

	if got := derive.TypeForReservedRtype(rt); got != typ {
		t.Fatalf("TypeForReservedRtype = %v, want %v", got, typ)
	}

	// Via a Machine with empty globals: the globals index misses, the synth-gated
	// reservation fallback recovers it.
	m := &Machine{}
	if got := m.typeByRtype(rt); got != typ {
		t.Fatalf("typeByRtype = %v, want %v", got, typ)
	}
	// A genuine native rtype (not synth) is not in reservations: must stay nil and
	// must not be misrecovered.
	if got := m.typeByRtype(reflect.TypeFor[int]()); got != nil {
		t.Fatalf("typeByRtype(int) = %v, want nil", got)
	}
}
