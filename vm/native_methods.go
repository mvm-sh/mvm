package vm

import "reflect"

// Native method tables (ADR-023 phase 1): each native receiver type caches, per
// global methodID, the unbound method func (reflect.Method.Func), called with
// the receiver prepended -- no per-call MethodByName/makeMethodValue.
// Gated to the simple case (see resolveSimpleNative); else slow path.

// nativeMethodTables enables the tables; the benchmark toggles it. On by default.
var nativeMethodTables = true

// SetNativeMethodTables toggles index-based native method dispatch (ADR-023).
func SetNativeMethodTables(b bool) { nativeMethodTables = b }

// nativeMethodDesc is a resolved native method. needAddr marks a pointer
// receiver; ok=false (the zero value) caches "not dispatchable, use slow path".
type nativeMethodDesc struct {
	fn       reflect.Value
	needAddr bool
	ok       bool
}

// nativeMethodSet is one type's table, indexed by global methodID (sparse).
type nativeMethodSet struct {
	byID map[int]nativeMethodDesc
}

// cachedNativeCall is the marker IfaceCall pushes on a hit; Call invokes Unbound
// with Recv prepended.
type cachedNativeCall struct {
	Unbound reflect.Value
	Recv    reflect.Value
}

var (
	cachedNativeCallRtype = reflect.TypeFor[cachedNativeCall]()
	reflectTypeIface      = reflect.TypeFor[reflect.Type]()
)

// nativeMethodDesc resolves and caches methodID for rt. Per-Machine, so no lock;
// child machines rebuild their own.
func (m *Machine) nativeMethodDesc(rt reflect.Type, methodID int, name string) nativeMethodDesc {
	set := m.nativeMethods[rt]
	if set == nil {
		if m.nativeMethods == nil {
			m.nativeMethods = make(map[reflect.Type]*nativeMethodSet)
		}
		set = &nativeMethodSet{byID: make(map[int]nativeMethodDesc)}
		m.nativeMethods[rt] = set
	}
	d, hit := set.byID[methodID]
	if !hit {
		if fn, needAddr, ok := resolveSimpleNative(rt, name); ok {
			d = nativeMethodDesc{fn: fn, needAddr: needAddr, ok: true}
		}
		set.byID[methodID] = d
	}
	return d
}

// resolveSimpleNative returns the unbound func for a directly-dispatchable
// native method, or ok=false to take the slow (shim/hook/promotion) path.
func resolveSimpleNative(rt reflect.Type, name string) (fn reflect.Value, needAddr, ok bool) {
	if rt == reflectValueRtype || isShimmedNativeType(rt) || rt.Implements(reflectTypeIface) ||
		isSynthOrSynthPtr(rt) || hasNativeMethodHook(rt, name) {
		return reflect.Value{}, false, false
	}
	if mm, found := rt.MethodByName(name); found {
		if simpleParams(mm.Func.Type()) {
			return mm.Func, false, true
		}
		return reflect.Value{}, false, false
	}
	if rt.Kind() != reflect.Pointer {
		pt := reflect.PointerTo(rt)
		if hasNativeMethodHook(pt, name) {
			return reflect.Value{}, false, false
		}
		if mm, found := pt.MethodByName(name); found && simpleParams(mm.Func.Type()) {
			return mm.Func, true, true
		}
	}
	return reflect.Value{}, false, false
}

// simpleParams reports no interface or func parameters, so arg-marshaling is a
// no-op and a prepended receiver cannot misalign it.
func simpleParams(ft reflect.Type) bool {
	for in := range ft.Ins() {
		switch in.Kind() {
		case reflect.Interface, reflect.Func:
			return false
		}
	}
	return true
}
