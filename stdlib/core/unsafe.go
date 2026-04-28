// Hand-written binding for the "unsafe" pseudo-package.
// unsafe has no parseable Go source, so cmd/extract cannot generate this file.
// Do not add the cmd/extract "Code generated" marker: `make clean_generate`
// greps for that marker and would delete this file.

package core

import (
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/stdlib"
)

// Sizeof/Alignof/Offsetof are intercepted at compile time - in
// goparser.evalConstExpr for const contexts, and in comp.compileBuiltin for
// regular expressions. These stubs only provide symbol-table entries and
// must never run.
func unsafeSizeof(any) uintptr   { panic("unsafe.Sizeof: not intercepted") }
func unsafeAlignof(any) uintptr  { panic("unsafe.Alignof: not intercepted") }
func unsafeOffsetof(any) uintptr { panic("unsafe.Offsetof: not intercepted") }

// Add/String/StringData are callable; Slice/SliceData are intercepted in
// comp.compileBuiltin (to set the pointer-element-dependent result type),
// so the stubs use reflect on `any` for their generic parameters.
func unsafeAdd(p unsafe.Pointer, n int) unsafe.Pointer { return unsafe.Add(p, n) } //nolint:gosec
func unsafeString(p *byte, n int) string               { return unsafe.String(p, n) }
func unsafeStringData(s string) *byte                  { return unsafe.StringData(s) }

func unsafeSlice(ptr any, n int) any {
	rv := reflect.ValueOf(ptr)
	p := rv.UnsafePointer()
	elemType := rv.Type().Elem()
	if p == nil {
		if n != 0 {
			panic("unsafe.Slice: ptr is nil and len is not zero")
		}
		return reflect.Zero(reflect.SliceOf(elemType)).Interface()
	}
	return reflect.NewAt(reflect.ArrayOf(n, elemType), p).Elem().Slice(0, n).Interface()
}

func unsafeSliceData(s any) any {
	rv := reflect.ValueOf(s)
	elemType := rv.Type().Elem()
	ptrType := reflect.PointerTo(elemType)
	if rv.Len() == 0 && rv.Cap() == 0 {
		if rv.IsNil() {
			return reflect.Zero(ptrType).Interface()
		}
		return reflect.NewAt(elemType, unsafe.Pointer(rv.Pointer())).Interface() //nolint:gosec
	}
	return reflect.NewAt(elemType, unsafe.Pointer(rv.Index(0).UnsafeAddr())).Interface() //nolint:gosec
}

func init() {
	stdlib.Values["unsafe"] = map[string]reflect.Value{
		"Pointer":    reflect.ValueOf((*unsafe.Pointer)(nil)),
		"Sizeof":     reflect.ValueOf(unsafeSizeof),
		"Alignof":    reflect.ValueOf(unsafeAlignof),
		"Offsetof":   reflect.ValueOf(unsafeOffsetof),
		"Add":        reflect.ValueOf(unsafeAdd),
		"Slice":      reflect.ValueOf(unsafeSlice),
		"String":     reflect.ValueOf(unsafeString),
		"SliceData":  reflect.ValueOf(unsafeSliceData),
		"StringData": reflect.ValueOf(unsafeStringData),
	}
}
