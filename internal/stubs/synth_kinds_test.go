//go:build !wasm

package stubs

import (
	"fmt"
	"reflect"
	"testing"
	"unsafe"

	"github.com/mvm-sh/mvm/internal/runtype"
)

// stringerSig is the reflect.Type for func() string (shape S1 without recv).
var stringerSig = reflect.TypeOf((func() string)(nil))

func stringerT() reflect.Type {
	return reflect.TypeOf((*fmt.Stringer)(nil)).Elem()
}

// mkSynth reserves a method-bearing synth rtype over layout and fills it -- the
// reserve/fill equivalent of the retired Attach* builders, used by these tests
// to exercise the stub-pool dispatch end to end. Same (rt, err) shape as the old
// builders so call sites are unchanged.
func mkSynth(layout reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	res, err := runtype.ReserveMethods(layout, name, pkgPath)
	if err != nil {
		return nil, err
	}
	if _, err := FillMethods(res, methods); err != nil {
		return nil, err
	}
	return res.Type(), nil
}

// mkSynthPtr is mkSynth for a *T identity (wires elem.PtrToThis).
func mkSynthPtr(elem reflect.Type, name, pkgPath string, methods []Method) (reflect.Type, error) {
	res, err := runtype.ReservePtrMethods(elem, name, pkgPath)
	if err != nil {
		return nil, err
	}
	if _, err := FillMethods(res, methods); err != nil {
		return nil, err
	}
	return res.Type(), nil
}

// stubHandler returns a HandlerS1 that records the call and returns out.
func stubHandler(called *bool, out string) HandlerS1 {
	return func(recv unsafe.Pointer) string {
		_ = recv
		*called = true
		return out
	}
}

func TestSynthPrimitiveInt(t *testing.T) {
	called := false
	rt, err := mkSynth(reflect.TypeOf(int(0)),
		"MyInt", "test", []Method{{
			Name: "String", Exported: true, Sig: stringerSig,
			Handler: stubHandler(&called, "myint"),
		}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	if got, want := rt.Kind(), reflect.Int; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if got, want := rt.NumMethod(), 1; got != want {
		t.Errorf("NumMethod = %d, want %d", got, want)
	}
	if !rt.Implements(stringerT()) {
		t.Fatal("rt.Implements(fmt.Stringer) = false")
	}

	v := reflect.New(rt).Elem()
	v.SetInt(42)
	s, ok := v.Interface().(fmt.Stringer)
	if !ok {
		t.Fatal("Interface().(fmt.Stringer) failed")
	}
	if got, want := s.String(), "myint"; got != want {
		t.Errorf("s.String() = %q, want %q", got, want)
	}
	if !called {
		t.Error("handler not invoked")
	}
}

func TestSynthPrimitiveString(t *testing.T) {
	called := false
	rt, err := mkSynth(reflect.TypeOf(""),
		"MyStr", "test", []Method{{
			Name: "String", Exported: true, Sig: stringerSig,
			Handler: stubHandler(&called, "mystr"),
		}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	if !rt.Implements(stringerT()) {
		t.Fatal("not Stringer")
	}
	v := reflect.New(rt).Elem()
	v.SetString("hi")
	s := v.Interface().(fmt.Stringer)
	if got, want := s.String(), "mystr"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !called {
		t.Error("handler not invoked")
	}
}

// TestInstallMethodsSortedByName pins the alphabetical-sort fix for #1:
// methods passed in REVERSE alphabetical order must end up sorted in the
// synth rtype's method array so reflect.implements (linear merge) and
// reflect.Type.MethodByName (binary search) work correctly.
// Without sorting, MethodByName misses entries past the binary-search
// midpoint and Implements returns false for multi-method target ifaces.
func TestSynthFunc(t *testing.T) {
	called := false
	layout := reflect.TypeOf(func(int) string { return "" })
	rt, err := mkSynth(layout, "MyFunc", "test", []Method{{
		Name: "String", Exported: true, Sig: stringerSig,
		Handler: stubHandler(&called, "myfunc"),
	}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	if got, want := rt.Kind(), reflect.Func; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if got, want := rt.NumIn(), 1; got != want {
		t.Errorf("NumIn = %d, want %d", got, want)
	}
	if rt.In(0) != reflect.TypeOf(int(0)) || rt.Out(0) != reflect.TypeOf("") {
		t.Errorf("signature = %v, want func(int) string", rt)
	}
	if !rt.Implements(stringerT()) {
		t.Fatal("not Stringer")
	}
	v := reflect.MakeFunc(rt, func([]reflect.Value) []reflect.Value {
		return []reflect.Value{reflect.ValueOf("")}
	})
	if got, want := v.Interface().(fmt.Stringer).String(), "myfunc"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !called {
		t.Error("handler not invoked")
	}
}

func TestInstallMethodsSortedByName(t *testing.T) {
	// Five methods in REVERSE alphabetical order so binary search would
	// miss the early ones if the array stays unsorted.
	mk := func(name string) Method {
		called := new(bool)
		return Method{
			Name: name, Exported: true, Sig: stringerSig,
			Handler: stubHandler(called, name),
		}
	}
	methods := []Method{mk("Zeta"), mk("Quux"), mk("Mid"), mk("Foo"), mk("Alpha")}

	rt, err := mkSynth(
		reflect.TypeOf(int(0)), "Multi", "test", methods)
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	if got, want := rt.NumMethod(), 5; got != want {
		t.Fatalf("NumMethod = %d, want %d", got, want)
	}
	want := []string{"Alpha", "Foo", "Mid", "Quux", "Zeta"}
	for i, name := range want {
		if got := rt.Method(i).Name; got != name {
			t.Errorf("Method(%d).Name = %q, want %q", i, got, name)
		}
		// MethodByName uses binary search; without sort it would miss.
		if _, ok := rt.MethodByName(name); !ok {
			t.Errorf("MethodByName(%q) not found", name)
		}
	}
}

// TestAcquireSlotsPartialRollback verifies that when one method's slot
// acquisition fails mid-batch, the handlers from earlier-acquired slots
// are cleared so captured closure state doesn't leak.
// Slot indices stay consumed (counters are monotonic); only handler refs
// are released.
func TestAcquireSlotsPartialRollback(t *testing.T) {
	// Methods: 2 S1 (ok) + 1 with bogus handler type (errInvalidHandlerType).
	called := new(bool)
	methods := []Method{
		{Name: "A", Exported: true, Sig: stringerSig, Handler: stubHandler(called, "a")},
		{Name: "B", Exported: true, Sig: stringerSig, Handler: stubHandler(called, "b")},
		{Name: "C", Exported: true, Sig: stringerSig, Shape: ShapeS1, Handler: "not a func"},
	}
	beforeUsed := SlotsUsedS1()
	_, _, err := acquireSlots(methods)
	if err == nil {
		t.Fatal("expected error from invalid handler type")
	}
	afterUsed := SlotsUsedS1()
	if afterUsed-beforeUsed != 2 {
		t.Errorf("SlotsUsedS1 delta = %d, want 2 (counter is monotonic; both attempts before failure must show)",
			afterUsed-beforeUsed)
	}
	// Verify the rolled-back slots have nil handlers.
	for i := beforeUsed; i < afterUsed; i++ {
		if slotPoolS1[i].handler != nil {
			t.Errorf("slotPoolS1[%d].handler not released after rollback", i)
		}
	}
}

func TestSynthSlice(t *testing.T) {
	called := false
	rt, err := mkSynth(reflect.TypeOf([]int(nil)),
		"MySlice", "test", []Method{{
			Name: "String", Exported: true, Sig: stringerSig,
			Handler: stubHandler(&called, "myslice"),
		}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	if got, want := rt.Kind(), reflect.Slice; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if rt.Elem() != reflect.TypeOf(int(0)) {
		t.Errorf("Elem = %v, want int", rt.Elem())
	}
	if !rt.Implements(stringerT()) {
		t.Fatal("not Stringer")
	}
	v := reflect.MakeSlice(rt, 3, 3)
	v.Index(0).SetInt(1)
	s := v.Interface().(fmt.Stringer)
	if got, want := s.String(), "myslice"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !called {
		t.Error("handler not invoked")
	}
}

func TestSynthArray(t *testing.T) {
	called := false
	layout := reflect.ArrayOf(4, reflect.TypeOf(int(0)))
	rt, err := mkSynth(layout, "MyArr", "test", []Method{{
		Name: "String", Exported: true, Sig: stringerSig,
		Handler: stubHandler(&called, "myarr"),
	}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	if got, want := rt.Kind(), reflect.Array; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if rt.Len() != 4 {
		t.Errorf("Len = %d, want 4", rt.Len())
	}
	if !rt.Implements(stringerT()) {
		t.Fatal("not Stringer")
	}
	v := reflect.New(rt).Elem()
	v.Index(0).SetInt(7)
	s := v.Interface().(fmt.Stringer)
	if got, want := s.String(), "myarr"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !called {
		t.Error("handler not invoked")
	}
}

func TestSynthMap(t *testing.T) {
	called := false
	layout := reflect.MapOf(reflect.TypeOf(""), reflect.TypeOf(int(0)))
	rt, err := mkSynth(layout, "MyMap", "test", []Method{{
		Name: "String", Exported: true, Sig: stringerSig,
		Handler: stubHandler(&called, "mymap"),
	}})
	if err != nil {
		t.Fatalf("mkSynth: %v", err)
	}
	if got, want := rt.Kind(), reflect.Map; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if rt.Key() != reflect.TypeOf("") {
		t.Errorf("Key = %v, want string", rt.Key())
	}
	if !rt.Implements(stringerT()) {
		t.Fatal("not Stringer")
	}
	v := reflect.MakeMap(rt)
	v.SetMapIndex(reflect.ValueOf("a"), reflect.ValueOf(1))
	s := v.Interface().(fmt.Stringer)
	if got, want := s.String(), "mymap"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !called {
		t.Error("handler not invoked")
	}
}
