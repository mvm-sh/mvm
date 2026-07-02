package interptest

import "testing"

// A value-receiver method called on a non-addressable struct (a map-index
// result) must get an addressable copy of the receiver so the body can write
// its fields; before the HeapAlloc detach fix this panicked with
// "reflect.Value.SetInt using unaddressable value".
func TestValueRecvFieldWriteFromMapIndex(t *testing.T) {
	src := `package main

import "fmt"

type Pos struct{ Col, Start int }

func (p Pos) withCol() Pos { p.Col = p.Start + 1; return p }

func main() {
	m := map[string]Pos{"a": {Start: 5}}
	fmt.Println(m["a"].withCol().Col)
}
`
	if got, want := evalOut(t, "value_recv_map_index.go", src), "6\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A slice argument is passed by value: its header is copied, so reassigning the
// caller's field the arg came from must not change the parameter. Before the
// detachByValueArgs ref-header fix the parameter aliased the field's cell, so
// `p.ctx = nil` shrank len(key) to 0 (it would panic in code that then indexed).
func TestSliceArgHeaderDetached(t *testing.T) {
	src := `package main

import "fmt"

type P struct{ ctx []string }

func (p *P) f(key []string) int {
	p.ctx = nil
	return len(key)
}

func main() {
	p := &P{}
	p.ctx = append(p.ctx, "t")
	fmt.Println(p.f(p.ctx))
}
`
	if got, want := evalOut(t, "slice_arg_detach.go", src), "1\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A callee returning its param yields a value sharing the caller's variable
// storage; a multi-assign writing that variable must not clobber the other
// results before they are stored (vm.Detach). Broke net/url resolvePath via
// `elem, remaining, found = strings.Cut(remaining, "/")` with interpreted strings.
func TestMultiAssignAliasedReturn(t *testing.T) {
	src := `package main

import "fmt"

func cut(s string) (string, string, bool) { return s, "", false }

func main() {
	elem, remaining, found := "", "c", true
	elem, remaining, found = cut(remaining)
	fmt.Println(elem, remaining, found)

	var x string
	x, global = retG()
	fmt.Println(x, global)
}

var global = "glob"

func retG() (string, string) { return global, "new" }
`
	if got, want := evalOut(t, "multiassign_alias.go", src), "c  false\nglob new\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A compound assign whose LHS contains a call must run the call once: the
// op= desugar to `lhs = lhs op rhs` duplicated it (hoistLHSCall). Broke
// regexp/syntax `p.op(OpEndText).Flags |= WasDollar`, pushing two ops.
func TestCompoundAssignCallLHS(t *testing.T) {
	src := `package main

import "fmt"

type R struct{ Flags int }

var (
	calls int
	r     = &R{}
	sl    = []int{10, 20}
)

func getR() *R     { calls++; return r }
func getPtr() *int { calls++; return &sl[0] }

func main() {
	getR().Flags |= 4
	fmt.Println(calls, r.Flags)
	getR().Flags++
	fmt.Println(calls, r.Flags)
	*getPtr() += 7
	fmt.Println(calls, sl[0])
}
`
	if got, want := evalOut(t, "compound_assign_call_lhs.go", src), "1 4\n2 5\n3 17\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// An anonymous struct used as a struct field keeps reflect's empty type name:
// its String() is the struct literal, not the field name. Before the materialize
// fix the field-type clone inherited the field name and reflected as "Foo".
func TestAnonStructFieldTypeName(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

func main() {
	var b struct{ Foo struct{ V int } }
	fmt.Println(reflect.TypeOf(b).Field(0).Type.String())
}
`
	if got, want := evalOut(t, "anon_struct_field_name.go", src), "struct { V int }\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// The implicit & on a pointer-receiver call must alias a captured variable's
// heap cell (AddrCell/AddrHeap), like explicit &v does; plain Addr boxed a
// detached copy and the mutation was lost. Broke logrus TestLevelUnmarshalText
// (u.UnmarshalText inside t.Run left u at 0).
func TestPtrRecvCapturedAutoAddr(t *testing.T) {
	src := `package main

import "fmt"

type Level uint32

func (l *Level) Set(v Level) { *l = v }

func main() {
	var a Level
	func() { a.Set(1) }()
	fmt.Println(a)

	var b Level
	g := func() { b.Set(2) }
	g()
	fmt.Println(b)

	var c Level
	defer func() { _ = c }()
	c.Set(3)
	fmt.Println(c)
}
`
	if got, want := evalOut(t, "ptr_recv_captured.go", src), "1\n2\n3\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
