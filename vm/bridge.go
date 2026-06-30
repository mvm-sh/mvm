package vm

import (
	"reflect"
	"sync"
)

// NativeMethodHook intercepts a native method call. The returned values
// are the replacement results. Whether a hook is installed for the
// (rtype, name) pair is reported separately by hasNativeMethodHook.
type NativeMethodHook = func(m *Machine, recv reflect.Value, args []reflect.Value) []reflect.Value

type hookKey struct {
	rt   reflect.Type
	name string
}

var (
	nativeMethodHooksMu sync.RWMutex
	nativeMethodHooks   = map[hookKey]NativeMethodHook{}
)

// RegisterNativeMethodHook installs hook for recvInstance's named method.
// recvInstance is a typed nil (e.g. (*testing.T)(nil)); the function keys
// on reflect.TypeOf(recvInstance).
func RegisterNativeMethodHook(recvInstance any, methodName string, hook NativeMethodHook) {
	rt := reflect.TypeOf(recvInstance)
	if rt == nil {
		return
	}
	nativeMethodHooksMu.Lock()
	nativeMethodHooks[hookKey{rt, methodName}] = hook
	nativeMethodHooksMu.Unlock()
}

func hasNativeMethodHook(rt reflect.Type, name string) bool {
	nativeMethodHooksMu.RLock()
	defer nativeMethodHooksMu.RUnlock()
	_, ok := nativeMethodHooks[hookKey{rt, name}]
	return ok
}

func lookupNativeMethodHook(rt reflect.Type, name string) NativeMethodHook {
	nativeMethodHooksMu.RLock()
	defer nativeMethodHooksMu.RUnlock()
	return nativeMethodHooks[hookKey{rt, name}]
}

// MethodValueShim intercepts native method-value resolution on a sentinel type
// (e.g. *runtime.Func): it returns a replacement bound method, or an invalid
// Value to decline. Unlike NativeMethodHook it yields the method value (for
// reflect / method-expression use), not the call result.
type MethodValueShim = func(rv reflect.Value, name string) reflect.Value

var (
	methodValueShimsMu sync.RWMutex
	methodValueShims   = map[reflect.Type]MethodValueShim{}
)

// RegisterMethodValueShim installs shim for the exact receiver rtype rt.
func RegisterMethodValueShim(rt reflect.Type, shim MethodValueShim) {
	if rt == nil {
		return
	}
	methodValueShimsMu.Lock()
	methodValueShims[rt] = shim
	methodValueShimsMu.Unlock()
}

// isShimmedNativeType forces rt's methods onto the slow path so tryMethodValueShim runs.
func isShimmedNativeType(rt reflect.Type) bool {
	methodValueShimsMu.RLock()
	defer methodValueShimsMu.RUnlock()
	_, ok := methodValueShims[rt]
	return ok
}

func tryMethodValueShim(rv reflect.Value, name string) reflect.Value {
	if !rv.IsValid() {
		return reflect.Value{}
	}
	methodValueShimsMu.RLock()
	shim := methodValueShims[rv.Type()]
	methodValueShimsMu.RUnlock()
	if shim == nil {
		return reflect.Value{}
	}
	return shim(rv, name)
}
