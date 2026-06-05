package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

func runMain(t *testing.T, name, src string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval(name, src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String()
}

// A type parameter is inferred from an argument whose type is a named generic
// instantiation (here *node[T], a pointer to a generic struct). Inference used
// to only descend Pointer/Slice/Map/Func and leaf-match on Name, so a struct
// instantiation node[T] left T unbound -> "cannot infer type parameter T".
// This is the shape google/btree relies on: func max[T](n *node[T]) called as
// max(t.root) where t.root is *node[T].
func TestGenericInferFromNamedInstanceArg(t *testing.T) {
	src := `package main

import "fmt"

type node[T any] struct{ items []T }

func first[T any](n *node[T]) (_ T, found bool) {
	if n == nil {
		return
	}
	return n.items[0], true
}

type Tree[T any] struct{ root *node[T] }

func (t *Tree[T]) Min() (_ T, _ bool) {
	return first(t.root)
}

func main() {
	t := &Tree[int]{root: &node[int]{items: []int{7, 8}}}
	v, ok := t.Min()
	fmt.Println(v, ok)
}
`
	if got, want := runMain(t, "infer_named_instance.go", src), "7 true\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A generic struct with a self-referential field whose type is itself a generic
// instantiation (children items[*node[T]]) gets instantiated from two distinct
// function contexts. The nested instance items[*node[int]] was registered under
// one mangled name but its method receiver was re-mangled later under a drifted
// name (a type arg's PkgPath is populated lazily), leaving the method's receiver
// type unresolved -> "cannot range over <nil>". The instance now snapshots its
// mangled name at registration, so method emission matches.
func TestGenericNestedInstanceMethodNoDrift(t *testing.T) {
	src := `package main

import "fmt"

type items[T any] []T

func (s items[T]) first() (T, bool) {
	for i := range s {
		return s[i], true
	}
	var z T
	return z, false
}

type node[T any] struct {
	items    items[T]
	children items[*node[T]]
}

func (n *node[T]) get() bool {
	_, ok := n.items.first()
	return ok
}

// references node[int] from a second function context (not just main)
func mk() *node[int] { return &node[int]{} }

func main() {
	_ = mk()
	n := &node[int]{items: items[int]{1, 2}}
	fmt.Println(n.get())
}
`
	if got, want := runMain(t, "nested_instance_drift.go", src), "true\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A generic method body (Tree.Min) calls a generic free function (leftmost) whose
// own signature forward-references a type (ctx) declared later. leftmost's
// registration defers, but a defined type (ItemTree) eagerly parses Min's body
// first, so the call used to compile to a bare ref to the codeless template -> nil
// func panic at run time. This is the google/btree shape (BTreeG.Min -> min(t.root),
// copyOnWriteContext declared after node).
func TestGenericForwardRefFreeFuncInMethod(t *testing.T) {
	src := `package main

import "fmt"

type Item interface{ Less(o Item) bool }

type node[T any] struct {
	items    []T
	children []*node[T]
	cow      *ctx[T] // forward ref to ctx, declared below
}

func leftmost[T any](n *node[T]) (_ T, ok bool) {
	if n == nil {
		return
	}
	return n.items[0], true
}

type Tree[T any] struct {
	root *node[T]
	cow  *ctx[T]
}

func (t *Tree[T]) Min() (_ T, _ bool) { return leftmost(t.root) }

func NewG[T any]() *Tree[T] { return &Tree[T]{cow: &ctx[T]{}} }

type ctx[T any] struct{ less func(a, b T) bool }

type ItemTree Tree[Item]

func New() *ItemTree { return (*ItemTree)(NewG[Item]()) }

func main() {
	t := New()
	mn, ok := (*Tree[Item])(t).Min()
	fmt.Println(mn, ok)
}
`
	if got, want := runMain(t, "fwdref_freefunc.go", src), "<nil> false\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
