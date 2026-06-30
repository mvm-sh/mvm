package stdlib

import (
	"reflect"
	"runtime"
	"sync"
	"unsafe"

	"github.com/mvm-sh/mvm/vm"
)

// The runtime.Func bridge: runtime.FuncForPC over interpreted frames returns a
// *runtime.Func sentinel whose Name()/FileLine() must read recorded metadata,
// not the host runtime's lookup (which can't decode mvm's synthetic PCs).
// Lives here, not in vm: it is stdlib stub behavior, driven by runtime_virt.go.

func init() {
	vm.RegisterMethodValueShim(runtimeFuncPtrType, runtimeFuncMethodShim)
}

// RuntimeFuncInfo holds the synthesized Name/FileLine for a *runtime.Func sentinel.
type RuntimeFuncInfo struct {
	Name string
	File string
	Line int
}

type runtimeFuncEntry struct {
	rf   *runtime.Func
	info *RuntimeFuncInfo
}

var runtimeFuncMeta sync.Map // uintptr (sentinel addr) -> *runtimeFuncEntry

var runtimeFuncPtrType = reflect.TypeFor[*runtime.Func]()

// runtimeFuncSentinel embeds runtime.Func with padding so each allocation gets a
// unique address (at least 2 bytes apart, so the pkg/errors pc-1 lookup never aliases).
type runtimeFuncSentinel struct {
	rf runtime.Func
	_  [24]byte
}

// NewRuntimeFuncSentinel returns a fresh *runtime.Func whose address is unique.
func NewRuntimeFuncSentinel() *runtime.Func {
	s := &runtimeFuncSentinel{}
	return (*runtime.Func)(unsafe.Pointer(s))
}

// RegisterRuntimeFunc records Name/File/Line for rf so interpreted code calling
// rf.Name() / rf.FileLine() observes them instead of the host runtime's lookup.
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

// LookupRuntimeFunc returns rf's registered metadata, or nil if not from the mvm bridge.
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

// LookupRuntimeFuncByPC returns the runtime Func and info from a program counter.
// pkg/errors stores PCs as sentinel+1, so it probes pc-1 before pc.
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

func runtimeFuncMethodShim(rv reflect.Value, name string) reflect.Value {
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
