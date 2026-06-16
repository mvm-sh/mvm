// An interface value passed through an indirect call (a func value, or a method
// which mvm dispatches via a bound func value) arrives boxed as an mvm Iface,
// whose in-memory layout is not a Go eface {type, data}. Taking its address for
// unsafe access -- the pattern protobuf's pointerOfIface uses to recover a
// message pointer -- must still see a real eface, so &iface promotes to a real
// interface{} cell. Without the fix the indirect path reads a zero data word
// while the direct call reads the pointer.
package main

import (
	"fmt"
	"unsafe"
)

type T struct{ A string }

func (*T) M() {}

func dataWord(v any) uintptr {
	type eface struct{ typ, data unsafe.Pointer }
	return uintptr((*eface)(unsafe.Pointer(&v)).data)
}

func viaFunc(m any) uintptr { return dataWord(m) }

var fnVar = viaFunc

type recv struct{}

func (recv) call(m any) uintptr { return dataWord(m) }

func bump(p *interface{}) { *p = 2 }

func main() {
	a := &T{A: "x"}
	direct := dataWord(a)
	fmt.Println("direct nonzero:", direct != 0)
	fmt.Println("via func value:", fnVar(a) == direct)
	fmt.Println("via method:", recv{}.call(a) == direct)

	// The unsafe-eface fix must NOT detach a real interface{} variable: &iface
	// stays an aliasing *interface{} so writes through it propagate.
	var v interface{} = 1
	pv := &v
	*pv = 2
	var w interface{} = 1
	bump(&w)
	fmt.Println("write via &iface:", v)
	fmt.Println("write via *interface{} param:", w)
}

// Output:
// direct nonzero: true
// via func value: true
// via method: true
// write via &iface: 2
// write via *interface{} param: 2
