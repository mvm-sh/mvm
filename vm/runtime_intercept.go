package vm

import (
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/mvm-sh/mvm/runtype"
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

// activeMachine tracks the currently running Machine per goroutine so
// that native bridges installed via PackagePatcher closure at import time
// (currently only stdlib's runtime.Callers replacement) can find the
// running Machine when invoked through reflect. Most other bridges
// receive `m` explicitly via the VM-side call site and should NOT touch
// this map.
//
// Per-goroutine keying is what makes the lookup race-free across parallel
// tests: each goroutine's Run() writes to its own slot, so concurrent
// Machines on different goroutines never observe each other's state. A
// goroutine that nests Run() calls (Machine A re-entering through a
// native bridge that synchronously runs another mvm function on a pooled
// runner) sees a LIFO stack of (set, defer restore) pairs and resolves
// to the innermost active Machine, which is what bridge code wants.
//
// Key choice: we use the *g pointer (read from the goroutine register
// via gid()) rather than the parsed goid because it is dramatically
// cheaper (~1ns vs ~1us via runtime.Stack) and just as unique. The
// pointer is stable for the goroutine's lifetime and every Run() defers
// the matching SetActiveMachine(prev) restore, so the runtime's g
// recycling never observes a leaked slot.
//
// The value is a *machineCell, allocated once per goroutine and reused:
// nested re-entries swap the atomic pointer instead of re-storing into
// the map, which would allocate a trie node per call.
type machineCell struct {
	m atomic.Pointer[Machine]
}

var activeMachine sync.Map // uintptr (g pointer) -> *machineCell

// SetActiveMachine records m as the running Machine for the current
// goroutine and returns the previous value (nil if none). Pair with
// `defer SetActiveMachine(prev)` to restore on return. Passing m == nil
// (the restore step at goroutine top of stack) deletes the slot so the
// map doesn't accumulate stale entries from short-lived goroutines.
func SetActiveMachine(m *Machine) (prev *Machine) {
	g := gid()
	if v, ok := activeMachine.Load(g); ok {
		cell := v.(*machineCell)
		if m == nil {
			prev = cell.m.Load()
			activeMachine.Delete(g)
			return prev
		}
		return cell.m.Swap(m)
	}
	if m == nil {
		return nil
	}
	// First Run on this goroutine; g is unique to it, so Store is race-free.
	cell := &machineCell{}
	cell.m.Store(m)
	activeMachine.Store(g, cell)
	return nil
}

// ActiveMachine returns the Machine currently set via SetActiveMachine on
// the calling goroutine, or nil if none. Prefer reaching the Machine
// through an explicit parameter or closure capture; ActiveMachine is
// reserved for native bridge closures installed at package patch time
// with no other route to the runtime.
func ActiveMachine() *Machine {
	if v, ok := activeMachine.Load(gid()); ok {
		return v.(*machineCell).m.Load()
	}
	return nil
}

// runtimeFuncPtrType is *runtime.Func, used to detect intercepted receivers.
var runtimeFuncPtrType = reflect.TypeFor[*runtime.Func]()

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
var reflectValueRtype = reflect.TypeFor[reflect.Value]()

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
//
// m is the Machine currently executing the bridge call; it is captured into
// the returned MakeFunc closures so that later invocations resolve the
// receiver's method through the right type tables even when another goroutine
// is simultaneously running an unrelated Machine. Threading m explicitly here
// (vs. reading the package-global activeMachine) is what makes the
// reflect.Value.MethodByName path race-free under -race with t.Parallel().
func reflectValueShim(m *Machine, rv reflect.Value, name string) reflect.Value {
	if m == nil || !rv.IsValid() || rv.Type() != reflectValueRtype {
		return reflect.Value{}
	}
	innerRV, ok := rv.Interface().(reflect.Value)
	if !ok || !innerRV.IsValid() {
		return reflect.Value{}
	}
	// A synth rtype resolves its supported-shape methods natively (uncommon table),
	// so bail to native dispatch -- except MethodByName, whose case below probes
	// native first then falls back to the mvm shim for unsupported-shape methods
	// (e.g. func() []int) that the native table never carries.
	synthRecv := innerRV.Type() != ifaceRtype && runtype.IsSynth(innerRV.Type())
	if synthRecv && name != "MethodByName" {
		return reflect.Value{}
	}
	switch name {
	case "MethodByName":
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
				// Supported-shape methods are in the native table; prefer them.
				// Unsupported-shape methods fall through to the mvm shim below.
				if synthRecv {
					if nm := innerRV.MethodByName(methodName); nm.IsValid() {
						return []reflect.Value{reflect.ValueOf(nm)}
					}
				}
				method, found := m.MethodByName(ifc.Typ, methodName)
				if !found {
					return zeroReflectValueResult
				}
				ft := m.ifaceMethodFuncType(methodName)
				if ft == nil {
					return zeroReflectValueResult
				}
				closure := m.MakeMethodCallable(ifc, method)
				// Wrap in reflect.ValueOf so the returned value has type reflect.Value
				// (struct), matching the declared return type of func(string) reflect.Value.
				return []reflect.Value{reflect.ValueOf(m.makeCallFunc(closure, ft))}
			})
	case "Call", "CallSlice":
		if innerRV.Kind() != reflect.Func {
			return reflect.Value{}
		}
		spread := name == "CallSlice"
		return reflect.MakeFunc(callShimType,
			func(args []reflect.Value) []reflect.Value {
				var in []reflect.Value
				if len(args) > 0 && args[0].IsValid() && !args[0].IsNil() {
					in, _ = args[0].Interface().([]reflect.Value)
				}
				if out, ok := callSynthMethodFunc(innerRV, in, spread); ok {
					return []reflect.Value{reflect.ValueOf(out)}
				}
				return []reflect.Value{reflect.ValueOf(callWithSpread(innerRV, in, spread))}
			})
	}
	return reflect.Value{}
}

// callSynthMethodFunc converts a Call/CallSlice on a synth Method.Func
// (recognized by code PC) into a bound-method call.
// Method.Func packs the receiver by value (Tfn ABI), but synth stubs implement
// only the one-word Ifn form, which bound dispatch uses; an indirect value
// receiver would otherwise be misread.
// Direct-iface receivers already match the stub ABI and stay native.
func callSynthMethodFunc(fn reflect.Value, in []reflect.Value, spread bool) ([]reflect.Value, bool) {
	ft := fn.Type()
	if ft.NumIn() == 0 || len(in) == 0 {
		return nil, false
	}
	recvT := ft.In(0)
	if isDirectIface(recvT) || !runtype.IsSynth(recvT) ||
		!in[0].IsValid() || !in[0].Type().AssignableTo(recvT) {
		return nil, false
	}
	pc := fn.Pointer()
	var name string
	for i := range recvT.NumMethod() {
		mm := recvT.Method(i)
		if mm.Type == ft && mm.Func.Pointer() == pc {
			name = mm.Name
			break
		}
	}
	if name == "" {
		return nil, false
	}
	bound := in[0].MethodByName(name)
	if !bound.IsValid() {
		return nil, false
	}
	return callWithSpread(bound, in[1:], spread), true
}

func callWithSpread(fn reflect.Value, args []reflect.Value, spread bool) []reflect.Value {
	if spread {
		return fn.CallSlice(args)
	}
	return fn.Call(args)
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
