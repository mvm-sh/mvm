//go:build cgo

// Build the mvm interpreter as a C-callable static archive.
//
// Build from the parent directory (examples/c) so the .a / .h land next
// to main.c:
//
//	go build -buildmode=c-archive -o libmvm.a ./shim
//
// See ../Makefile for the full recipe.
package main

/*
#include <stdint.h>
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"reflect"
	"runtime/cgo"
	"strings"
	"unsafe"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// MvmNew creates a new interpreter and returns an opaque handle.
// The handle must eventually be released with MvmFree.
//
//export MvmNew
func MvmNew() C.uintptr_t {
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)

	// Pre-register a custom "host" package. Interpreted code can call
	// host.Greet, host.Repeat, and read host.Answer.
	i.ImportPackageValues(map[string]map[string]reflect.Value{
		"host": {
			"Greet":  reflect.ValueOf(func(s string) string { return "hello, " + s + "!" }),
			"Repeat": reflect.ValueOf(strings.Repeat),
			"Answer": reflect.ValueOf(42),
		},
	})

	i.AutoImportPackages()
	return C.uintptr_t(cgo.NewHandle(i))
}

// MvmFree releases the interpreter referenced by h.
//
//export MvmFree
func MvmFree(h C.uintptr_t) {
	cgo.Handle(h).Delete()
}

// MvmEval compiles and runs src on the interpreter referenced by h.
// On success it stores a printable form of the result in *outResult and
// returns 0. On error it stores the message in *outError and returns -1.
// Both strings, when non-NULL, must be released by the caller via MvmFreeString.
//
//export MvmEval
func MvmEval(h C.uintptr_t, name, src *C.char, outResult, outError **C.char) C.int {
	i, ok := cgo.Handle(h).Value().(*interp.Interp)
	if !ok {
		*outError = C.CString("invalid interpreter handle")
		return -1
	}
	res, err := i.Eval(C.GoString(name), C.GoString(src))
	if err != nil {
		*outError = C.CString(err.Error())
		return -1
	}
	if res.IsValid() && res.CanInterface() {
		*outResult = C.CString(fmt.Sprintf("%v", res.Interface()))
	} else {
		*outResult = C.CString("")
	}
	return 0
}

// MvmFreeString releases a string previously returned through an out-parameter.
//
//export MvmFreeString
func MvmFreeString(s *C.char) {
	if s != nil {
		C.free(unsafe.Pointer(s))
	}
}

func main() {}
