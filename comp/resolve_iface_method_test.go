package comp

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/symbol"
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
	wantB := &mtype.Type{Name: "B", Rtype: ifaceRtype}

	// A.GetB: symbolic Sig returns B, but its cached Rtype degraded to func() interface{}.
	a := &mtype.Type{
		Name:  "A",
		Rtype: ifaceRtype,
		IfaceMethods: []mtype.IfaceMethod{{
			Name:  "GetB",
			Rtype: degradedSig,
			Sig:   &mtype.Type{Returns: []*mtype.Type{wantB}},
		}},
	}

	c := NewCompiler(golang.GoSpec)
	// A same-named concrete method whose func() interface{} signature matches the
	// degraded ifaceSig -- the spurious match the old resolution order returned.
	c.Symbols["pp.GetB"] = &symbol.Symbol{Kind: symbol.Func, Name: "pp.GetB", Type: &mtype.Type{Rtype: degradedSig}}

	sym := c.resolveIfaceMethodSym(a, "GetB")
	if sym == nil {
		t.Fatal("resolveIfaceMethodSym returned nil for a declared method")
	}
	if got := sym.Type.ReturnType(0); got != wantB {
		t.Fatalf("GetB return type = %v, want the symbolic B (%v); the spurious concrete won", got, wantB)
	}
}

// A method call on an interface-typed receiver whose own clone lost its method
// set must recover the canonical interface by PACKAGE, not name alone: two
// packages can each declare a same-named interface.
//
// Mirrors `mvm test gonum.org/v1/gonum/graph/path`: gonum has both graph.Graph
// (5 methods) and traverse.Graph (2 methods). A Complement field `g.Graph` is
// typed graph.Graph but its field clone carries no IfaceMethods, so the
// canonical had to be looked up. Keyed by name only, the two same-named
// interfaces were "ambiguous" -> no signature -> an arbitrary same-named
// concrete From() (wrong arity) was used, corrupting the compile stack
// non-deterministically (map order). Disambiguating by package fixes it.
func TestResolveIfaceMethodDisambiguatesByPackage(t *testing.T) {
	ifaceRtype := reflect.TypeFor[any]()
	nodesT := &mtype.Type{Name: "Nodes", Rtype: ifaceRtype}

	// The canonical graph.Graph: From() returns Nodes.
	graphIface := &mtype.Type{
		Name: "Graph", ImportPath: "gonum.org/v1/gonum/graph", PkgName: "graph", Rtype: ifaceRtype,
		IfaceMethods: []mtype.IfaceMethod{{Name: "From", Sig: &mtype.Type{Returns: []*mtype.Type{nodesT}}}},
	}
	// A foreign same-named interface in another package; must not hijack.
	traverseIface := &mtype.Type{
		Name: "Graph", ImportPath: "gonum.org/v1/gonum/graph/traverse", PkgName: "traverse", Rtype: ifaceRtype,
		IfaceMethods: []mtype.IfaceMethod{{Name: "From", Sig: &mtype.Type{Returns: nil}}},
	}

	c := NewCompiler(golang.GoSpec)
	c.Symbols["gonum.org/v1/gonum/graph.Graph"] = &symbol.Symbol{Kind: symbol.Type, Name: "Graph", Type: graphIface}
	c.Symbols["gonum.org/v1/gonum/graph/traverse.Graph"] = &symbol.Symbol{Kind: symbol.Type, Name: "Graph", Type: traverseIface}
	// A same-named concrete From with a NO-return signature: picked arbitrarily by
	// map order when no interface signature is recovered, it drops the call result.
	noRet := reflect.FuncOf(nil, nil, false) // func()
	c.Symbols["band.From"] = &symbol.Symbol{Kind: symbol.Func, Name: "band.From", Type: &mtype.Type{Rtype: noRet}}

	// The receiver: a graph.Graph clone with package info but no method set.
	clone := &mtype.Type{Name: "Graph", ImportPath: "gonum.org/v1/gonum/graph", PkgName: "graph", Rtype: ifaceRtype}

	sym := c.resolveIfaceMethodSym(clone, "From")
	if sym == nil {
		t.Fatal("resolveIfaceMethodSym returned nil for a declared method")
	}
	if sym.Type.NumOut() != 1 {
		t.Fatalf("From signature NumOut = %d, want 1; a foreign/concrete same-named method hijacked it (type %v)", sym.Type.NumOut(), sym.Type)
	}
	if got := sym.Type.ReturnType(0); got != nodesT {
		t.Fatalf("From return type = %v, want graph.Nodes (%v)", got, nodesT)
	}
}
