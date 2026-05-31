package runtype

import (
	"reflect"
	"testing"
)

// stubPC returns a valid code PC for a method's Ifn/Tfn. NumMethod / Method /
// Implements never dereference it, so any real func PC suffices here.
func stubPC() uintptr { return reflect.ValueOf(stubPC).Pointer() }

func sampleMethod(name string) MethodSpec {
	return MethodSpec{
		Name:     name,
		Exported: true,
		Sig:      reflect.TypeOf(func() string { return "" }),
		StubPC:   stubPC(),
	}
}

// fielder is satisfied by a String() string method, used to assert Implements
// flips from false (reserved) to true (filled) on the SAME rtype identity.
type stringerLike interface{ String() string }

func TestReserveFillStablePrimitive(t *testing.T) {
	r, err := ReserveMethods(reflect.TypeOf(int(0)), "Grams", "weight")
	if err != nil {
		t.Fatal(err)
	}
	rt := r.Type()
	if got := rt.NumMethod(); got != 0 {
		t.Fatalf("reserved NumMethod = %d, want 0", got)
	}
	if rt.Name() != "Grams" {
		t.Fatalf("reserved Name = %q, want Grams", rt.Name())
	}
	ifaceRT := reflect.TypeOf((*stringerLike)(nil)).Elem()
	if rt.Implements(ifaceRT) {
		t.Fatal("reserved type unexpectedly implements stringerLike")
	}

	if err := r.Fill([]MethodSpec{sampleMethod("String")}); err != nil {
		t.Fatal(err)
	}
	if r.Type() != rt {
		t.Fatal("Fill changed rtype identity")
	}
	if got := rt.NumMethod(); got != 1 {
		t.Fatalf("filled NumMethod = %d, want 1", got)
	}
	if rt.Method(0).Name != "String" {
		t.Fatalf("filled Method(0).Name = %q, want String", rt.Method(0).Name)
	}
	if !rt.Implements(ifaceRT) {
		t.Fatal("filled type does not implement stringerLike")
	}
}

// The cascade-retiring property: a composite that captured the reserved rtype
// BEFORE Fill observes the methods afterward, with no rtype swap.
func TestReserveFillSeenThroughComposite(t *testing.T) {
	r, err := ReserveMethods(reflect.TypeOf(""), "Name", "pkg")
	if err != nil {
		t.Fatal(err)
	}
	elem := r.Type()
	sliceOfElem := reflect.SliceOf(elem) // captures elem pre-Fill
	structOfElem := reflect.StructOf([]reflect.StructField{
		{Name: "N", Type: elem},
	})

	if err := r.Fill([]MethodSpec{sampleMethod("String")}); err != nil {
		t.Fatal(err)
	}
	if got := sliceOfElem.Elem().NumMethod(); got != 1 {
		t.Fatalf("slice elem NumMethod = %d, want 1 (cascade-free fill failed)", got)
	}
	if got := structOfElem.Field(0).Type.NumMethod(); got != 1 {
		t.Fatalf("struct field NumMethod = %d, want 1", got)
	}
}

func TestReserveFillStructAndPtr(t *testing.T) {
	layout := reflect.StructOf([]reflect.StructField{
		{Name: "X", Type: reflect.TypeOf(int(0))},
	})
	r, err := ReserveMethods(layout, "Point", "geom")
	if err != nil {
		t.Fatal(err)
	}
	elem := r.Type()
	pr, err := ReservePtrMethods(elem, "*Point", "geom")
	if err != nil {
		t.Fatal(err)
	}
	if reflect.PointerTo(elem) != pr.Type() {
		t.Fatal("PointerTo(elem) does not resolve to the reserved *Point")
	}
	if elem.NumMethod() != 0 || pr.Type().NumMethod() != 0 {
		t.Fatal("reserved struct/ptr have unexpected methods")
	}
	if err := pr.Fill([]MethodSpec{sampleMethod("Move")}); err != nil {
		t.Fatal(err)
	}
	if got := reflect.PointerTo(elem).NumMethod(); got != 1 {
		t.Fatalf("*Point NumMethod after Fill = %d, want 1", got)
	}
}

// TestReserveStructLayoutFill covers the struct cycle path: reserve over a
// provisional layout, fill methods, then fill the real layout (with a self-ref
// *T field) -- identity, methods, and layout must all hold.
func TestReserveStructLayoutFill(t *testing.T) {
	provisional := reflect.StructOf([]reflect.StructField{
		{Name: "Placeholder", Type: reflect.TypeOf(int(0))},
	})
	r, err := ReserveMethods(provisional, "Node", "tree")
	if err != nil {
		t.Fatal(err)
	}
	node := r.Type()
	if err := r.Fill([]MethodSpec{sampleMethod("Visit")}); err != nil {
		t.Fatal(err)
	}
	// Real layout references *Node (the reserved identity) -- the cycle.
	realLayout := reflect.StructOf([]reflect.StructField{
		{Name: "Val", Type: reflect.TypeOf(int(0))},
		{Name: "Next", Type: reflect.PointerTo(node)},
	})
	FillStructLayout(node, realLayout)

	if r.Type() != node {
		t.Fatal("FillStructLayout changed identity")
	}
	if node.NumMethod() != 1 || node.Method(0).Name != "Visit" {
		t.Fatalf("methods lost after layout fill: NumMethod=%d", node.NumMethod())
	}
	if node.NumField() != 2 || node.Field(0).Name != "Val" || node.Field(1).Name != "Next" {
		t.Fatalf("layout not filled: NumField=%d", node.NumField())
	}
	if node.Size() != realLayout.Size() {
		t.Fatalf("size = %d, want %d", node.Size(), realLayout.Size())
	}
	if node.Field(1).Type != reflect.PointerTo(node) {
		t.Fatal("self-ref *Node field type mismatch")
	}
	_ = reflect.New(node).Elem().Interface() // must not panic
}

func TestFillRejectsBadCounts(t *testing.T) {
	r, err := ReserveMethods(reflect.TypeOf(int(0)), "T", "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Fill(nil); err == nil {
		t.Fatal("Fill(nil) should error")
	}
	tooMany := make([]MethodSpec, maxMethods+1)
	for i := range tooMany {
		tooMany[i] = sampleMethod("M")
	}
	if err := r.Fill(tooMany); err == nil {
		t.Fatal("Fill over maxMethods should error")
	}
}
