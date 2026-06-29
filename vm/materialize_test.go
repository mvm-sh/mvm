package vm

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/derive"
	"github.com/mvm-sh/mvm/mtype"
)

// MaterializeRtype builds an rtype from a purely symbolic *Type (nil Rtype) --
// the S1 goal: goparser describes a type symbolically, comp materializes it.
func TestMaterializeRtype(t *testing.T) {
	intT := mtype.TypeOf(0)
	strT := mtype.TypeOf("")

	cases := []struct {
		name string
		typ  *mtype.Type
		want reflect.Type
	}{
		{"ptr", mtype.SymPtr(intT), reflect.TypeOf((*int)(nil))},
		{"slice", mtype.SymSlice(intT), reflect.TypeOf([]int(nil))},
		{"array", mtype.SymArray(3, intT), reflect.TypeOf([3]int{})},
		{"chan", mtype.SymChan(reflect.BothDir, intT), reflect.TypeOf(make(chan int))},
		{"map", mtype.SymMap(strT, intT), reflect.TypeOf(map[string]int(nil))},
		{"nested []*int", mtype.SymSlice(mtype.SymPtr(intT)), reflect.TypeOf([]*int(nil))},
		{"map[string][]int", mtype.SymMap(strT, mtype.SymSlice(intT)), reflect.TypeOf(map[string][]int(nil))},
	}
	for _, c := range cases {
		if c.typ.Rtype != nil {
			t.Fatalf("%s: precondition failed, Rtype should be nil before materialize", c.name)
		}
		got := derive.MaterializeRtype(c.typ)
		if got != c.want {
			t.Errorf("%s: materialized %v, want %v", c.name, got, c.want)
		}
		if c.typ.Rtype != got {
			t.Errorf("%s: Rtype not cached on the *Type", c.name)
		}
	}
}
