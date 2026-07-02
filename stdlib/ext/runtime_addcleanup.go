// Package ext holds the cmd/extract-generated native stdlib bindings.
// This file is a hand-written supplement (no generated marker, so clean_generate keeps it) for symbols extract cannot emit.
package ext

import (
	"reflect"
	"runtime"

	"github.com/mvm-sh/mvm/stdlib"
)

// runtime.AddCleanup is generic, so cmd/extract can't reflect.ValueOf it; this shim monomorphizes it.
// AddCleanup uses ptr only as an address, so a *byte view watches the same object and S=any boxes the arg.
// The init runs after the generated runtime.go (filename order), so Values["runtime"] exists.
func init() {
	stdlib.Values["runtime"]["AddCleanup"] = reflect.ValueOf(addCleanup)
}

func addCleanup(ptr any, cleanup func(any), arg any) runtime.Cleanup {
	p := reflect.ValueOf(ptr)
	if p.Kind() != reflect.Pointer || p.IsNil() {
		panic("runtime.AddCleanup: ptr is nil")
	}
	return runtime.AddCleanup((*byte)(p.UnsafePointer()), cleanup, arg)
}
