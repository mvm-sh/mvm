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
