package vm

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"
)

// RuntimeFuncInfo holds the synthesized Name/FileLine for a *runtime.Func
// sentinel allocated by the bridged runtime.FuncForPC. The sentinel is a
// fresh &runtime.Func{} pointer; the host runtime never sees it because
// nativeMethodLookup intercepts Name and FileLine before any host method
// runs.
type RuntimeFuncInfo struct {
	Name string
	File string
	Line int
}

// runtimeFuncEntry pairs a sentinel pointer with its metadata. The map
// is keyed by sentinel address (uintptr) rather than *runtime.Func so
// that PC-based lookups (mvmFuncForPC's pc-1 / pc probes) can be plain
// map loads instead of `(*runtime.Func)(unsafe.Pointer(pc - 1))`. The
// pointer-arithmetic form is rejected by checkptr under -race because
// the resulting unsafe.Pointer expression carries no original to anchor
// the conversion. Storing rf alongside info keeps the sentinel
// allocation alive for the lifetime of the entry.
type runtimeFuncEntry struct {
	rf   *runtime.Func
	info *RuntimeFuncInfo
}

var runtimeFuncMeta sync.Map // uintptr (sentinel addr) -> *runtimeFuncEntry

// activeMachine tracks the currently running Machine so that native
// bridges (e.g. the runtime.Callers replacement) can find it.
// Single-machine-at-a-time semantics: concurrent Machines on different
// goroutines will race on this slot. Run threads the previous value
// through its call chain via SetActiveMachine + defer-restore, which is
// correct for the synchronous nesting that makeCallFunc/CallFunc
// produce. Concurrent goroutine execution is no worse off than under
// the previous global-stack implementation, which had the same race.
var activeMachine atomic.Pointer[Machine]

// SetActiveMachine atomically replaces the current Machine and returns
// the previous value. Pair with `defer SetActiveMachine(prev)` to
// restore on return.
func SetActiveMachine(m *Machine) (prev *Machine) {
	return activeMachine.Swap(m)
}

// ActiveMachine returns the Machine currently set via SetActiveMachine,
// or nil if none.
func ActiveMachine() *Machine {
	return activeMachine.Load()
}

// runtimeFuncPtrType is *runtime.Func, used to detect intercepted receivers.
var runtimeFuncPtrType = reflect.TypeOf((*runtime.Func)(nil))

// runtimeFuncSentinel embeds runtime.Func together with padding so each
// allocation gets a unique address. runtime.Func itself is a zero-sized
// struct (opaque{} field), and Go reuses a single pointer for all
// zero-size allocations -- using it bare would collapse every
// registered frame onto the same key.
//
// The padding is sized at 24 bytes (> 16) so allocations bypass Go's
// tiny allocator. With a 1-byte struct the tiny allocator can pack
// consecutive sentinels exactly 1 byte apart, which makes
// mvmFuncForPC's `pc-1 / pc` two-step lookup alias adjacent sentinels:
// pcs[i+1]-1 == sentinel_i, so frame i+1 prints frame i's metadata.
// 24 bytes lands the struct in a regular 8-aligned size class, so
// distinct sentinels are at least 8 bytes apart.
type runtimeFuncSentinel struct {
	rf runtime.Func
	_  [24]byte
}

// NewRuntimeFuncSentinel returns a fresh *runtime.Func whose address is
// unique. Use it together with RegisterRuntimeFunc to mark a PC as
// virtualized.
func NewRuntimeFuncSentinel() *runtime.Func {
	s := &runtimeFuncSentinel{}
	return (*runtime.Func)(unsafe.Pointer(s))
}

// RegisterRuntimeFunc associates Name/File/Line metadata with rf so that
// interpreted code calling rf.Name() / rf.FileLine() observes the
// recorded values instead of the host runtime's lookup. rf must be a
// non-nil pointer obtained from NewRuntimeFuncSentinel so the address is
// distinct from any other registered sentinel.
func RegisterRuntimeFunc(rf *runtime.Func, name, file string, line int) {
	if rf == nil {
		return
	}
	addr := uintptr(unsafe.Pointer(rf))
	runtimeFuncMeta.Store(addr, &runtimeFuncEntry{
		rf:   rf,
		info: &RuntimeFuncInfo{Name: name, File: file, Line: line},
	})
}

// LookupRuntimeFunc returns the registered metadata for rf, or nil if rf
// was not produced by the mvm bridge.
func LookupRuntimeFunc(rf *runtime.Func) *RuntimeFuncInfo {
	if rf == nil {
		return nil
	}
	v, ok := runtimeFuncMeta.Load(uintptr(unsafe.Pointer(rf)))
	if !ok {
		return nil
	}
	return v.(*runtimeFuncEntry).info
}

// LookupRuntimeFuncByPC resolves a host-style PC value to a registered
// sentinel and its metadata. It tries pc-1 first (pkg/errors stores
// PC = sentinel+1 and looks up via pc-1) and falls back to pc for
// callers that skipped the +1 convention. Returns nil/nil when pc does
// not name a virtualized frame.
//
// Compared to the previous (*runtime.Func)(unsafe.Pointer(pc - 1))
// idiom, this form does no pointer arithmetic on a uintptr that came
// from a host pointer, so it is safe under -race / checkptr.
func LookupRuntimeFuncByPC(pc uintptr) (*runtime.Func, *RuntimeFuncInfo) {
	if pc == 0 {
		return nil, nil
	}
	if v, ok := runtimeFuncMeta.Load(pc - 1); ok {
		e := v.(*runtimeFuncEntry)
		return e.rf, e.info
	}
	if v, ok := runtimeFuncMeta.Load(pc); ok {
		e := v.(*runtimeFuncEntry)
		return e.rf, e.info
	}
	return nil, nil
}

// reflectValueRtype is the reflect.Type for reflect.Value itself.
var reflectValueRtype = reflect.TypeOf(reflect.Value{})

// Shim MakeFunc signatures, hoisted to avoid re-creating the reflect.Type on
// every shim invocation.
var (
	methodByNameShimType = reflect.TypeOf(func(string) reflect.Value { return reflect.Value{} })
	callShimType         = reflect.TypeOf(func([]reflect.Value) []reflect.Value { return nil })
)

// zeroReflectValueResult is the "method not found / invalid" return for the
// MethodByName shim: a one-element slice holding the zero reflect.Value, which
// matches the shim's declared return type.
var zeroReflectValueResult = []reflect.Value{reflect.Zero(reflectValueRtype)}

// reflectValueShim intercepts reflect.Value.MethodByName (and reflect.Value.Call
// on the result) when the inner value is an mvm-interpreted type, returning a
// synthetic bound method backed by mvm bytecode.
// This is needed because mvm methods live in vm.Type.Methods[] and are invisible
// to Go's native reflect type system.
//
// Two cases for the inner value:
//   - vm.Iface: reflect.ValueOf received an mvm interface without unwrapping;
//     we extract Typ and Val directly from the Iface struct.
//   - concrete mvm type: typeByRtype maps the Rtype back to the mvm *Type.
func reflectValueShim(rv reflect.Value, name string) reflect.Value {
	if !rv.IsValid() || rv.Type() != reflectValueRtype {
		return reflect.Value{}
	}
	innerRV, ok := rv.Interface().(reflect.Value)
	if !ok || !innerRV.IsValid() {
		return reflect.Value{}
	}
	switch name {
	case "MethodByName":
		m := ActiveMachine()
		if m == nil {
			return reflect.Value{}
		}
		// Build the Iface that MakeMethodCallable expects. When innerRV is
		// already a vm.Iface (mvm interface that escaped reflect.ValueOf
		// untouched), use it directly; otherwise wrap the concrete value
		// under its resolved mvm *Type.
		var ifc Iface
		if innerRV.Type() == ifaceRtype {
			ifc = innerRV.Interface().(Iface)
			if ifc.Typ == nil {
				return reflect.Value{}
			}
		} else {
			t := m.typeByRtype(innerRV.Type())
			if t == nil {
				return reflect.Value{}
			}
			ifc = Iface{Typ: t, Val: FromReflect(innerRV)}
		}
		return reflect.MakeFunc(methodByNameShimType,
			func(args []reflect.Value) []reflect.Value {
				methodName := args[0].String()
				m2 := ActiveMachine()
				if m2 == nil {
					return zeroReflectValueResult
				}
				method, found := m2.MethodByName(ifc.Typ, methodName)
				if !found {
					return zeroReflectValueResult
				}
				ft := m2.ifaceMethodFuncType(methodName)
				if ft == nil {
					return zeroReflectValueResult
				}
				closure := m2.MakeMethodCallable(ifc, method)
				// Wrap in reflect.ValueOf so the returned value has type reflect.Value
				// (struct), matching the declared return type of func(string) reflect.Value.
				return []reflect.Value{reflect.ValueOf(m2.makeCallFunc(closure, ft))}
			})
	case "Call":
		if innerRV.Kind() != reflect.Func {
			return reflect.Value{}
		}
		return reflect.MakeFunc(callShimType,
			func(args []reflect.Value) []reflect.Value {
				var in []reflect.Value
				if len(args) > 0 && args[0].IsValid() && !args[0].IsNil() {
					in, _ = args[0].Interface().([]reflect.Value)
				}
				out := innerRV.Call(in)
				return []reflect.Value{reflect.ValueOf(out)}
			})
	}
	return reflect.Value{}
}

// runtimeFuncShim returns a bound-method reflect.Value that satisfies
// (*runtime.Func).Name or (*runtime.Func).FileLine using the side-table
// entry for rv. Returns the zero reflect.Value if rv is not a tracked
// sentinel or name is not one of the intercepted methods.
func runtimeFuncShim(rv reflect.Value, name string) reflect.Value {
	if !rv.IsValid() || rv.Type() != runtimeFuncPtrType || rv.IsNil() {
		return reflect.Value{}
	}
	rf, ok := rv.Interface().(*runtime.Func)
	if !ok {
		return reflect.Value{}
	}
	info := LookupRuntimeFunc(rf)
	if info == nil {
		return reflect.Value{}
	}
	switch name {
	case "Name":
		return reflect.MakeFunc(reflect.TypeOf(func() string { return "" }),
			func(_ []reflect.Value) []reflect.Value {
				return []reflect.Value{reflect.ValueOf(info.Name)}
			})
	case "FileLine":
		return reflect.MakeFunc(reflect.TypeOf(func(uintptr) (string, int) { return "", 0 }),
			func(_ []reflect.Value) []reflect.Value {
				return []reflect.Value{reflect.ValueOf(info.File), reflect.ValueOf(info.Line)}
			})
	}
	return reflect.Value{}
}
