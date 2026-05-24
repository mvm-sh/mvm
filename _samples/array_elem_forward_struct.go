package main

// Known bug (pre-existing, not from the issue #18 fix): an array/slice/map field
// whose element is a forward-referenced struct sibling finalizes against the
// placeholder's dummy layout. vm.ArrayOf/SliceOf/MapOf snapshot the element's
// placeholder rtype eagerly and do not propagate the Placeholder flag, so the
// forward-ref deferral guard in goparser/type.go (which only checks for a direct
// struct-VALUE field) never fires. As a result Sizeof([3]B) reports 24 instead
// of 48 and Sizeof(A) reports 32 instead of 56 -- a silent wrong layout, a
// latent memory-safety hazard for reflect/copy/memmove paths.
//
// Reproduces only when the element struct (B) is declared AFTER the user (A) in
// the same group; declaring B first gives the correct layout. The fix belongs in
// the type.go deferral guard (recurse through composite element types) or in
// ArrayOf/SliceOf/MapOf (mark the result Placeholder when its element is one).

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

// skip: array element of a forward-declared struct sibling uses placeholder layout (Sizeof 24/32, want 48/56)
