package interp

import "testing"

// An interface method whose param mentions the interface itself (goldmark
// ast.Node.SortChildren(func(n1, n2 Node) int)) materializes that param to any
// while the interface's own synth rtype is mid-build, but to the precise synth
// iface on the concrete side. mtype.Identical compared materialized Rtypes by
// pointer and returned false, so no concrete type satisfied the interface.
// Identical now falls through to structural identity on rtype inequality.
func TestIfaceSelfRefMethodSig(t *testing.T) {
	src := `package main

import "fmt"

type Node interface {
	Kind() int
	SortChildren(comparator func(n1, n2 Node) int)
}

type Base struct{ n int }

func (b *Base) Kind() int                                     { return 1 }
func (b *Base) SortChildren(comparator func(n1, n2 Node) int) {}

func main() {
	var x any = &Base{}
	_, ok := x.(Node)
	fmt.Println("is Node:", ok)
}
`
	want := "is Node: true\n"
	if got := evalProgram(t, "selfsig.go", src); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
