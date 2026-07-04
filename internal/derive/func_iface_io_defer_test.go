package derive

import (
	iofs "io/fs"
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
	// WS interface { Pop() Fwd }, where Fwd is a still-forward struct so Pop's sig
	// cannot materialize yet (materialize returns nil for a placeholder). WS thus
	// has no buildable synth rtype until the sig fills, exactly the http2 case.
	fwd := mtype.SymStruct(nil, nil, nil)
	fwd.Name = "Fwd"
	fwd.PkgName = "example.com/fnfield"
	fwd.Placeholder = true
	ws := mtype.SymBasic(reflect.Interface)
	ws.Name = "WS"
	ws.PkgName = "example.com/fnfield"
	ws.IfaceMethods = []mtype.IfaceMethod{
		{Name: "Pop", ID: -1, Rtype: nil, Sig: mtype.SymFunc(nil, []*mtype.Type{fwd}, false)},
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
	srv.PkgName = "example.com/fnfield"

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
	if !pendingFinalize.has(srv) {
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
	if pendingFinalize.has(srv) {
		t.Fatal("Srv should no longer be pending after the func field synths")
	}
}

// nativeIdentityFor must reject a same-named type from another package (import
// path) and an interface skew that keeps the method count (names), else a user
// `package fs` adopts the host io/fs rtype and native methods shadow its own.
func TestNativeIdentityForGuards(t *testing.T) {
	fmRt := reflect.TypeFor[iofs.FileMode]()
	RegisterNativeIdentity("fs.FileMode", fmRt)
	mode := func(importPath string) *mtype.Type {
		u := mtype.SymBasic(reflect.Uint32)
		u.Name = "FileMode"
		u.PkgName = "fs"
		u.ImportPath = importPath
		return u
	}
	if got := nativeIdentityFor(mode("io/fs")); got != fmRt {
		t.Errorf("mirror FileMode: got %v, want host identity", got)
	}
	if got := nativeIdentityFor(mode("example.com/user/fs")); got != nil {
		t.Errorf("user fs.FileMode adopted host identity %v", got)
	}

	fsRt := reflect.TypeFor[iofs.FS]()
	RegisterNativeIdentity("fs.FS", fsRt)
	iface := func(method string) *mtype.Type {
		it := mtype.SymBasic(reflect.Interface)
		it.Name = "FS"
		it.PkgName = "fs"
		it.ImportPath = "io/fs"
		it.IfaceMethods = []mtype.IfaceMethod{{Name: method}}
		return it
	}
	if got := nativeIdentityFor(iface("Open")); got != fsRt {
		t.Errorf("mirror FS: got %v, want host identity", got)
	}
	if got := nativeIdentityFor(iface("Root")); got != nil {
		t.Errorf("same-count wrong-name interface adopted host identity %v", got)
	}
}
