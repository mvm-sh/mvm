package vm

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/mtype"
)

// A generic-instance iface param (name has '#') erases to interface{} even when
// its method sigs are ready; a non-generic iface stays precise (the go-cmp
// contract). Without this, materialization order leaves one side precise and the
// other erased, so native reflect.Implements fails (grpc *health.Server.Watch).
func TestGenericInstanceIfaceParamErases(t *testing.T) {
	mkIface := func(name string) *mtype.Type {
		it := mtype.SymBasic(reflect.Interface)
		it.Name = name
		it.PkgName = "example.com/gi"
		it.IfaceMethods = []mtype.IfaceMethod{{
			Name:  "M",
			ID:    -1,
			Rtype: reflect.TypeFor[func() bool](), // sig ready -> synthIfaceRtype would build precisely
			Sig:   mtype.SymFunc(nil, []*mtype.Type{mtype.SymBasic(reflect.Bool)}, false),
		}}
		return it
	}

	// Non-generic interface stays precise: func(Plain) carries Plain's method set.
	plainFn := MaterializeRtype(mtype.SymFunc([]*mtype.Type{mkIface("Plain")}, nil, false))
	if plainFn == nil {
		t.Fatal("plain func did not materialize")
	}
	if got := plainFn.In(0).NumMethod(); got != 1 {
		t.Fatalf("non-generic iface param: NumMethod() = %d, want 1 (precise)", got)
	}

	// Generic-instance interface erases: func(Box#int) param is interface{}.
	giFn := MaterializeRtype(mtype.SymFunc([]*mtype.Type{mkIface("Box#int")}, nil, false))
	if giFn == nil {
		t.Fatal("generic-instance func did not materialize")
	}
	if got := giFn.In(0).NumMethod(); got != 0 {
		t.Fatalf("generic-instance iface param: NumMethod() = %d, want 0 (erased to any)", got)
	}
	if giFn.In(0) != mtype.AnyRtype {
		t.Fatalf("generic-instance iface param = %v, want the canonical any rtype", giFn.In(0))
	}
}
