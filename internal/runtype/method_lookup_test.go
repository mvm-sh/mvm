package runtype

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"
	"unsafe"
)
import "testing"

type mlMap map[string]int

func (m mlMap) Sum() int {
	s := 0
	for _, v := range m {
		s += v
	}
	return s
}

func (m mlMap) AddAll(vs ...int) mlMap {
	for i, v := range vs {
		m[strings.Repeat("k", i+1)] = v
	}
	return m
}

func funcPCOfMethodValue(v reflect.Value) uintptr {
	type rvHeader struct {
		typ  unsafe.Pointer
		ptr  unsafe.Pointer
		flag uintptr
	}
	return **(**uintptr)(unsafe.Pointer(&(*rvHeader)(unsafe.Pointer(&v)).ptr))
}

// The abi-walk lookup must agree with stock reflect on every exported method:
// same name, index, full method type (incl. variadic), and resolved code PC.
func TestTypeMethodByNameABIMatchesReflect(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeFor[*strings.Builder](),
		reflect.TypeFor[*bytes.Buffer](),
		reflect.TypeFor[time.Time](),
		reflect.TypeFor[*sync.Pool](),
		reflect.TypeFor[reflect.Value](),
		reflect.TypeFor[mlMap](),
	}
	for _, rt := range types {
		if rt.NumMethod() == 0 {
			t.Fatalf("%v: no exported methods; bad test corpus", rt)
		}
		for i := range rt.NumMethod() {
			want := rt.Method(i)
			got, ok := typeMethodByNameABI(rt, want.Name)
			if !ok {
				t.Fatalf("%v: ABI lookup missed %s", rt, want.Name)
			}
			if got.Index != want.Index || got.Name != want.Name {
				t.Errorf("%v.%s: got index %d name %q, want %d %q",
					rt, want.Name, got.Index, got.Name, want.Index, want.Name)
			}
			if got.Type != want.Type {
				t.Errorf("%v.%s: type %v, want %v", rt, want.Name, got.Type, want.Type)
			}
			if gotPC, wantPC := funcPCOfMethodValue(got.Func), funcPCOfMethodValue(want.Func); gotPC != wantPC {
				t.Errorf("%v.%s: PC %#x, want %#x", rt, want.Name, gotPC, wantPC)
			}
		}
		if _, ok := typeMethodByNameABI(rt, "NoSuchMethodEver"); ok {
			t.Errorf("%v: phantom method found", rt)
		}
	}
}

// The forged Func must be callable end-to-end like reflect's own Method.Func.
func TestTypeMethodByNameABIForgedFuncCall(t *testing.T) {
	m, ok := typeMethodByNameABI(reflect.TypeFor[*strings.Builder](), "WriteString")
	if !ok {
		t.Fatal("WriteString not found")
	}
	var b strings.Builder
	m.Func.Call([]reflect.Value{reflect.ValueOf(&b), reflect.ValueOf("forged")})
	if b.String() != "forged" {
		t.Fatalf("builder = %q, want %q", b.String(), "forged")
	}

	vm, ok := typeMethodByNameABI(reflect.TypeFor[mlMap](), "AddAll")
	if !ok {
		t.Fatal("AddAll not found")
	}
	got := vm.Func.Call([]reflect.Value{
		reflect.ValueOf(mlMap{}), reflect.ValueOf(3), reflect.ValueOf(4),
	})[0].Interface().(mlMap)
	if got["k"] != 3 || got["kk"] != 4 {
		t.Fatalf("AddAll result = %v", got)
	}
}

// typeMethodABI must agree with the by-name lookup at every index.
func TestTypeMethodABIByIndex(t *testing.T) {
	rt := reflect.TypeFor[*bytes.Buffer]()
	for i := range rt.NumMethod() {
		want := rt.Method(i)
		got, ok := typeMethodABI(rt, i)
		if !ok {
			t.Fatalf("index %d: not found", i)
		}
		if got.Name != want.Name || got.Index != want.Index || got.Type != want.Type {
			t.Errorf("index %d: got %q/%d/%v, want %q/%d/%v",
				i, got.Name, got.Index, got.Type, want.Name, want.Index, want.Type)
		}
	}
	if _, ok := typeMethodABI(rt, rt.NumMethod()); ok {
		t.Error("out-of-range index found a method")
	}
	if _, ok := typeMethodABI(rt, -1); ok {
		t.Error("negative index found a method")
	}
}

// A bindABIMethod value must behave like a stock bound method value, without
// reflect's methodReceiver PC heap-spill: same signature, plain and variadic
// calls, pointer receiver mutation visible.
func TestBindABIMethodCall(t *testing.T) {
	var b strings.Builder
	m, ok := typeMethodByNameABI(reflect.TypeFor[*strings.Builder](), "WriteString")
	if !ok {
		t.Fatal("WriteString not found")
	}
	bound := bindABIMethod(m, reflect.ValueOf(&b))
	if want := reflect.ValueOf(&b).MethodByName("WriteString").Type(); bound.Type() != want {
		t.Fatalf("bound type %v, want %v", bound.Type(), want)
	}
	bound.Call([]reflect.Value{reflect.ValueOf("bound")})
	if b.String() != "bound" {
		t.Fatalf("builder = %q, want %q", b.String(), "bound")
	}

	mm, ok := typeMethodByNameABI(reflect.TypeFor[mlMap](), "AddAll")
	if !ok {
		t.Fatal("AddAll not found")
	}
	vb := bindABIMethod(mm, reflect.ValueOf(mlMap{}))
	if !vb.Type().IsVariadic() {
		t.Fatalf("bound AddAll not variadic: %v", vb.Type())
	}
	got := vb.Call([]reflect.Value{reflect.ValueOf(5), reflect.ValueOf(6)})[0].Interface().(mlMap)
	if got["k"] != 5 || got["kk"] != 6 {
		t.Fatalf("AddAll result = %v", got)
	}
	spread := vb.CallSlice([]reflect.Value{reflect.ValueOf([]int{7, 8})})[0].Interface().(mlMap)
	if spread["k"] != 7 || spread["kk"] != 8 {
		t.Fatalf("AddAll spread result = %v", spread)
	}
}

// mlWithStack mirrors pkg/errors' withStack: Error is promoted from an
// embedded unexported interface field.
type mlWithStack struct{ error }

// A receiver read out of an unexported field is read-only; mf.Call runs
// mustBeExported on it, so bindABIMethod must clear the flag.
func TestBindABIMethodROReceiver(t *testing.T) {
	w := &mlWithStack{errors.New("EOF")}
	recv := reflect.ValueOf(w).Elem().Field(0).Elem() // *errorString, read-only
	if recv.CanInterface() {
		t.Fatal("receiver is not read-only; test no longer covers the flagRO path")
	}
	m, ok := typeMethodByNameABI(recv.Type(), "Error")
	if !ok {
		t.Fatal("Error not found")
	}
	if got := bindABIMethod(m, recv).Call(nil)[0].String(); got != "EOF" {
		t.Fatalf("Error() = %q, want %q", got, "EOF")
	}
}
