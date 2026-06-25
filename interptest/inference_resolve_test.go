package interptest

import "testing"

// Three parse-time type-resolution gaps that broke generic inference when the
// argument type came from (a) a range-over-func loop variable, (b) a pointer-
// receiver method's return value, or (c) a type-switch case variable's field.
// All three surfaced interpreting go/token, go/ast, and go/printer on wasm.

// (a) range-over-func: the loop variable's type is the yield func's parameter,
// so slices.Clone(f.field) can infer its type parameter. (go/token serialize.go)
func TestInferRangeOverFuncVar(t *testing.T) {
	const src = `package main
import ("fmt"; "iter"; "slices")
type File struct{ lines []int }
type tree struct{ items []*File }
func (t *tree) all() iter.Seq[*File] {
	return func(yield func(*File) bool) {
		for _, f := range t.items { if !yield(f) { return } }
	}
}
func main() {
	t := &tree{items: []*File{{lines: []int{1, 2, 3}}}}
	for f := range t.all() { fmt.Println(slices.Clone(f.lines)) }
}`
	if got, want := evalOut(t, "rangefunc.go", src), "[1 2 3]\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// (b) pointer-receiver method return type resolves for inference even when the
// receiver is reached as a value (the method key is "*T.m").
func TestInferPtrRecvMethodReturn(t *testing.T) {
	const src = `package main
import ("fmt"; "slices")
type box struct{ xs []int }
func (b *box) data() []int { return b.xs }
func main() {
	b := box{xs: []int{4, 5}}     // value, ptr-receiver method auto-addresses
	fmt.Println(slices.Clone(b.data()))
}`
	if got, want := evalOut(t, "ptrrecv.go", src), "[4 5]\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// (c) type-switch case variable is typed per single-type case, so a field access
// on it can feed generic inference. (go/ast walk.go: walkList(v, n.List))
func TestInferTypeSwitchCaseVarField(t *testing.T) {
	const src = `package main
import ("fmt"; "slices")
type Node interface{ node() }
type Comment struct{ Text string }
func (*Comment) node() {}
type Group struct{ List []*Comment }
func (*Group) node() {}
func count[N Node](list []N) int { return len(slices.Clone(list)) }
func walk(n Node) int {
	switch x := n.(type) {
	case *Group:
		return count(x.List)
	case *Comment:
		return len(x.Text)
	}
	return -1
}
func main() {
	fmt.Println(walk(&Group{List: []*Comment{{"a"}, {"b"}}}))
}`
	if got, want := evalOut(t, "typeswitch.go", src), "2\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
