package interp

import "testing"

// reflect.Type.MethodByName must find an interpreted method that is invisible to
// Go's native synth rtype: one beyond the 16-method synth cap AND of an
// unsupported synth shape (multiple func returns plus a slice), and its
// Method.Func must be callable with a zero receiver. This is exactly how
// protobuf's makeStructInfo derives a legacy message's oneof wrappers via
// reflect.PtrTo(t).MethodByName("XXX_OneofFuncs").Func.Call([zero-recv]); without
// the reflectTypeShim the method is reported missing and fieldInfoForOneof
// nil-derefs. The 20 A* getters push XXX_Wrappers past the cap; its shape (a
// func return plus []any) keeps it out of the native table regardless of order.
func TestReflectTypeMethodByNameBeyondCap(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type W struct {
	b bool ` + "`protobuf:\"varint,1,opt\"`" + `
}

type T struct{ x int }

func (t *T) A0() int  { return 0 }
func (t *T) A1() int  { return 1 }
func (t *T) A2() int  { return 2 }
func (t *T) A3() int  { return 3 }
func (t *T) A4() int  { return 4 }
func (t *T) A5() int  { return 5 }
func (t *T) A6() int  { return 6 }
func (t *T) A7() int  { return 7 }
func (t *T) A8() int  { return 8 }
func (t *T) A9() int  { return 9 }
func (t *T) A10() int { return 10 }
func (t *T) A11() int { return 11 }
func (t *T) A12() int { return 12 }
func (t *T) A13() int { return 13 }
func (t *T) A14() int { return 14 }
func (t *T) A15() int { return 15 }
func (t *T) A16() int { return 16 }
func (t *T) A17() int { return 17 }
func (t *T) A18() int { return 18 }
func (t *T) A19() int { return 19 }

// Multiple-func-plus-slice return: unsupported synth shape, like protobuf's
// legacy XXX_OneofFuncs. Never gets a native synth stub.
func (t *T) XXX_Wrappers() (func() int, []any) {
	return func() int { return 42 }, []any{(*W)(nil)}
}

func main() {
	rt := reflect.TypeOf(&T{})
	m, ok := rt.MethodByName("XXX_Wrappers")
	fmt.Println("found:", ok, "numIn:", m.Type.NumIn(), "numOut:", m.Type.NumOut())
	rets := m.Func.Call([]reflect.Value{reflect.Zero(m.Type.In(0))})
	vs, isAny := rets[1].Interface().([]any)
	fmt.Println("isAny:", isAny, "len:", len(vs))
	wt := reflect.TypeOf(vs[0]).Elem()
	fmt.Println("wrapper:", wt.Name(), "tag:", wt.Field(0).Tag.Get("protobuf"))

	// Method-set rules: a pointer-receiver method is hidden on the value type.
	_, valOK := reflect.TypeOf(T{}).MethodByName("XXX_Wrappers")
	fmt.Println("valueExposes:", valOK)
}
`
	want := "found: true numIn: 1 numOut: 2\nisAny: true len: 1\nwrapper: W tag: varint,1,opt\nvalueExposes: false\n"
	if got := evalProgram(t, "reflect_type_methodbyname.go", src); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}
