package runtype

import (
	"bytes"
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
