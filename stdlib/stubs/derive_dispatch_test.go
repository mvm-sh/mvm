//go:build !wasm

package stubs

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/runtype"
)

// TestDerivedFmtStringer: a derived []T over a synth struct whose elem carries
// a dispatched Stringer formats via that method.
// Lives here, not runtype, because it needs the shape-stub dispatch pools.
func TestDerivedFmtStringer(t *testing.T) {
	called := new(bool)
	elem, err := mkSynth(
		reflect.StructOf([]reflect.StructField{
			{Name: "V", Type: reflect.TypeOf(int(0))},
		}),
		"DeriveFmt", "test",
		[]Method{{
			Name: "String", Exported: true, Sig: stringerSig,
			Handler: stubHandler(called, "DeriveFmt"),
		}},
	)
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	sl := runtype.SliceOf(elem)
	v := reflect.MakeSlice(sl, 2, 2)
	v.Index(0).FieldByName("V").SetInt(1)
	v.Index(1).FieldByName("V").SetInt(2)
	s := fmt.Sprintf("%v", v.Interface())
	if !strings.Contains(s, "DeriveFmt") {
		t.Errorf("%%v of derived slice = %q, want elem String() result present", s)
	}
}
