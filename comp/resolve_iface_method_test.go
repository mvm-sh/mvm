package comp

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// resolveIfaceMethodSym must take a method's static return type from the
// interface's own symbolic signature, not from a same-named concrete method.
//
// Mirrors `mvm test prototext`: protoreflect.Message and protoreflect.ProtoMessage
// are mutually recursive (Message.Interface() returns ProtoMessage,
// ProtoMessage.ProtoReflect() returns Message). While that cycle is unresolved a
// method's cached im.Rtype can momentarily collapse its named-interface return to
// interface{}; a same-named concrete method (e.g. some other Interface() interface{})
// then spuriously matched that degraded sig, so `m.Interface()` was typed
// interface{} and a later `.ProtoReflect()` failed at compile time with
// "undefined: ProtoReflect" -- non-deterministically, on map-iteration order.
func TestResolveIfaceMethodPrefersSymbolicReturn(t *testing.T) {
	ifaceRtype := reflect.TypeFor[any]()
	degradedSig := reflect.FuncOf(nil, []reflect.Type{ifaceRtype}, false) // func() interface{}

	// B is the named interface the method actually returns.
	wantB := &vm.Type{Name: "B", Rtype: ifaceRtype}

	// A.GetB: symbolic Sig returns B, but its cached Rtype degraded to func() interface{}.
	a := &vm.Type{
		Name:  "A",
		Rtype: ifaceRtype,
		IfaceMethods: []vm.IfaceMethod{{
			Name:  "GetB",
			Rtype: degradedSig,
			Sig:   &vm.Type{Returns: []*vm.Type{wantB}},
		}},
	}

	c := NewCompiler(golang.GoSpec)
	// A same-named concrete method whose func() interface{} signature matches the
	// degraded ifaceSig -- the spurious match the old resolution order returned.
	c.Symbols["pp.GetB"] = &symbol.Symbol{Kind: symbol.Func, Name: "pp.GetB", Type: &vm.Type{Rtype: degradedSig}}

	sym := c.resolveIfaceMethodSym(a, "GetB")
	if sym == nil {
		t.Fatal("resolveIfaceMethodSym returned nil for a declared method")
	}
	if got := sym.Type.ReturnType(0); got != wantB {
		t.Fatalf("GetB return type = %v, want the symbolic B (%v); the spurious concrete won", got, wantB)
	}
}
