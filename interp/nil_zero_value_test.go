package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// The zero value of a map or slice variable is nil. A local `var m map[...]int`
// used to be a non-nil empty container (Fnew always made one), so `m == nil` was
// false in a function body while correct under -e. A composite literal or make
// must still be non-nil, and writing to a nil map is a recoverable panic.
func TestNilZeroValueMapSlice(t *testing.T) {
	src := `package main

import "fmt"

type MyMap map[string]int
type S struct {
	M map[string]int
	L []int
}

func main() {
	var m map[string]int
	var s []int
	fmt.Println(m == nil, s == nil) // true true

	// Composite and make are non-nil, even when empty.
	fmt.Println(map[string]int{} == nil, []int{} == nil) // false false
	fmt.Println(make(map[string]int) == nil, make([]int, 0) == nil) // false false

	// Named header types and struct fields default to nil.
	var mm MyMap
	var st S
	fmt.Println(mm == nil, st.M == nil, st.L == nil) // true true true

	// Append to a nil slice works; the result is non-nil.
	var a []int
	a = append(a, 1, 2)
	fmt.Println(a, a == nil) // [1 2] false

	// Writing to a nil map is a recoverable runtime panic.
	func() {
		defer func() { fmt.Println("recovered:", recover() != nil) }() // true
		var nm map[string]int
		nm["x"] = 1
	}()
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("nil_zero.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "true true\n" +
		"false false\n" +
		"false false\n" +
		"true true true\n" +
		"[1 2] false\n" +
		"recovered: true\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}

// Two composite literals of the same map/slice type in one function must each
// produce their own non-nil container; the non-nil patch once matched a single
// canonical slot, leaving the other literal nil. Reduced from go-difflib chainB.
func TestNilZeroValueDuplicateLiterals(t *testing.T) {
	src := `package main

import "fmt"

func main() {
	// Two empty map[K]struct{} literals: writing to the first must not panic.
	a := map[string]struct{}{}
	a["x"] = struct{}{}
	b := map[string]struct{}{}
	b["y"] = struct{}{}
	fmt.Println(len(a), len(b)) // 1 1

	// Same for slices: both must be non-nil and independently appendable.
	p := []int{}
	p = append(p, 1)
	q := []int{}
	q = append(q, 2, 3)
	fmt.Println(len(p), len(q), p == nil, q == nil) // 1 2 false false
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("dup_literals.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "1 1\n" +
		"1 2 false false\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}
