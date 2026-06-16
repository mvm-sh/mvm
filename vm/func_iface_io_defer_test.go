package vm

import (
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/mtype"
)

// A struct field `func() Iface` whose interface result's method sigs are not yet
// materialized (im.Rtype nil) must not cache the erased `func() interface{}`
// permanently: the func type is kept pending and the struct re-patched by
// FinalizeDeferred once the sigs fill, so the field ends up `func() <Iface>`.
// This is the x/net/http2 Server.NewWriteScheduler erasure (WriteScheduler.Push/Pop
// referenced a forward struct, so they were nil when Server materialized).
func TestFuncIfaceResultDeferredSynth(t *testing.T) {
	// WS interface { Pop() bool }, with Pop's sig initially unmaterialized.
	ws := mtype.SymBasic(reflect.Interface)
	ws.Name = "WS"
	ws.PkgPath = "example.com/fnfield"
	ws.IfaceMethods = []mtype.IfaceMethod{
		{Name: "Pop", ID: -1, Rtype: nil, Sig: mtype.SymFunc(nil, []*mtype.Type{mtype.SymBasic(reflect.Bool)}, false)},
	}

	// Struct fnfield.Srv { New func() WS }: the field is a clone of the func type
	// with the field name in .Name and Base set, exactly as goparser builds it.
	funcType := mtype.SymFunc(nil, []*mtype.Type{ws}, false)
	field := *funcType
	field.Name = "New"
	field.Base = funcType
	field.Defined = false
	srv := mtype.SymStruct([]*mtype.Type{&field}, nil, nil)
	srv.Name = "Srv"
	srv.PkgPath = "example.com/fnfield"

	rt := MaterializeRtype(srv)
	if rt == nil {
		t.Fatal("Srv did not materialize")
	}
	// Best-effort layout while WS's sigs are unready: field erased to func() interface{}.
	f, ok := rt.FieldByName("New")
	if !ok {
		t.Fatal("no field New")
	}
	if f.Type.Kind() != reflect.Func || f.Type.NumOut() != 1 {
		t.Fatalf("field New = %v, want func()T", f.Type)
	}
	if got := f.Type.Out(0).NumMethod(); got != 0 {
		t.Fatalf("erased phase: Out(0).NumMethod() = %d, want 0 (interface{})", got)
	}
	if !isPending(srv) {
		t.Fatal("Srv should be pending while its func field's iface IO is unsynthable")
	}

	// materializeIfaceMethods completing: WS.Pop's sig is now materialized.
	ws.IfaceMethods[0].Rtype = reflect.TypeFor[func() bool]()
	FinalizeDeferred()

	// rt is patched in place; the field now exposes the precise WS method set.
	f, _ = rt.FieldByName("New")
	if got := f.Type.Out(0).NumMethod(); got != 1 {
		t.Fatalf("after FinalizeDeferred: Out(0).NumMethod() = %d, want 1 (WS.Pop)", got)
	}
	if isPending(srv) {
		t.Fatal("Srv should no longer be pending after the func field synths")
	}
}
