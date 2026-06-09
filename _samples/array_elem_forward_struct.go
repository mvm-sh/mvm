package main

// An array of a forward-referenced struct sibling once finalized against the
// placeholder's dummy layout (Sizeof([3]B) was 24 not 48, Sizeof(A) 32 not 56):
// the deferral guard checked only a direct struct-value field. placeholderStructElem
// now walks array elements, so the decl defers until B is finalized.
// Only triggers when B is declared after A.

import (
	"fmt"
	"unsafe"
)

type (
	A struct {
		items [3]B
		tag   int8
	}
	B struct{ p, q int64 }
)

func main() {
	fmt.Println(unsafe.Sizeof([3]B{}), unsafe.Sizeof(A{}))
}

// Output:
// 48 56
