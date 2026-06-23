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
