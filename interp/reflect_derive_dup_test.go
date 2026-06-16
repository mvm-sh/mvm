package interp

import "testing"

// reflect.SliceOf/MapOf/ArrayOf/ChanOf called from interpreted code over a synth
// (interpreted) element built a distinct rtype from the one mvm's materialize
// derives for the same composite literal: native reflect uses its own cache, mvm
// uses runtype's. So reflect.SliceOf(reflect.TypeOf((*X)(nil))) != reflect.TypeOf([]*X{}),
// which broke protobuf's extension converters (build.go reflect.SliceOf(goType) vs
// the user's []*X literal). interceptReflectCtor reroutes these to runtype.Derive*.
//
// PointerTo is NOT intercepted: rtype's PtrToThis back-pointer makes native
// reflect.PointerTo converge with the materialized *T on its own, so the pointer
// cases below must stay equal without interception.
func TestReflectDeriveDupConverges(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type X struct{ A int }

func (x *X) M() string { return "m" } // method-bearing -> synth rtype

type E int32

func (e E) String() string { return "e" }

func main() {
	// Slice of pointer-to-message: the protobuf extension case.
	litSlice := reflect.TypeOf([]*X{})
	ctorSlice := reflect.SliceOf(reflect.TypeOf((*X)(nil)))
	fmt.Println("slice:", litSlice == ctorSlice)

	// Slice whose element is itself a synth slice ([]E, E method-bearing): forces
	// the synth builder on BOTH the composite-literal (MkSlice) and reflect.SliceOf
	// sides, which diverge unless both route through runtype.Derive.
	litSynth := reflect.TypeOf([][]E{})
	ctorSynth := reflect.SliceOf(reflect.TypeOf([]E{}))
	fmt.Println("synthSlice:", litSynth == ctorSynth)

	// Map with synth key and elem.
	litMap := reflect.TypeOf(map[E]*X{})
	ctorMap := reflect.MapOf(reflect.TypeOf(E(0)), reflect.TypeOf((*X)(nil)))
	fmt.Println("map:", litMap == ctorMap)

	// Array of synth elem.
	litArr := reflect.TypeOf([3]*X{})
	ctorArr := reflect.ArrayOf(3, reflect.TypeOf((*X)(nil)))
	fmt.Println("array:", litArr == ctorArr)

	// Chan of synth elem.
	litChan := reflect.TypeOf(make(chan E))
	ctorChan := reflect.ChanOf(reflect.BothDir, reflect.TypeOf(E(0)))
	fmt.Println("chan:", litChan == ctorChan)

	// Pointer derivation already converges natively via PtrToThis; the value the
	// converter built (reflect.PtrTo) must equal the one reflect.New/NewAt yields.
	s := []E{}
	litPtr := reflect.TypeOf(&s)
	ctorPtr := reflect.PtrTo(reflect.TypeOf(s))
	newPtr := reflect.New(reflect.TypeOf(s)).Type()
	fmt.Println("ptr:", litPtr == ctorPtr && litPtr == newPtr)
}
`
	want := "slice: true\nsynthSlice: true\nmap: true\narray: true\nchan: true\nptr: true\n"
	if got := evalProgram(t, "reflect_derive_dup.go", src); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}
