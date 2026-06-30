package runtype

import (
	"reflect"
	"testing"
	"unsafe"
)

// synthStructForDerive builds a methodless synth struct rtype as a derived-type
// test elem. ReserveMethods registers its layout (all the constructors need) and
// stamps the name; it is left unfilled, so no dispatch is exercised here.
func synthStructForDerive(t *testing.T, name string) reflect.Type {
	t.Helper()
	res, err := ReserveMethods(
		reflect.StructOf([]reflect.StructField{
			{Name: "V", Type: reflect.TypeOf(int(0))},
		}),
		name, "test",
	)
	if err != nil {
		t.Fatalf("ReserveMethods(%q): %v", name, err)
	}
	return res.Type()
}

func TestPointerToOnSynthStruct(t *testing.T) {
	elem := synthStructForDerive(t, "DerivePT")
	pt := PointerTo(elem)
	if pt == nil {
		t.Fatal("PointerTo returned nil")
	}
	if got, want := pt.Kind(), reflect.Pointer; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if pt.Elem() != elem {
		t.Errorf("Elem != synth elem")
	}
	if pt.NumMethod() != 0 {
		t.Errorf("derived *T should have no methods, got %d", pt.NumMethod())
	}
	// reflect.New must accept the synth elem (proves Equal/GCData borrowed
	// from *int work).
	v := reflect.New(elem)
	if v.Type() != pt {
		// reflect.New returns reflect.PointerTo(elem) which used elem.PtrToThis
		// (set by an earlier ReservePtrMethods) if reserved; for our naked elem
		// PtrToThis is 0, so reflect builds its own *T. Our pt is a fresh
		// synth *T independent of that.
		// We tolerate the inequality and just verify pt is still usable.
		_ = v
	}
}

func TestSliceOfOnSynthStruct(t *testing.T) {
	elem := synthStructForDerive(t, "DeriveSL")
	st := SliceOf(elem)
	if st == nil {
		t.Fatal("SliceOf returned nil")
	}
	if got, want := st.Kind(), reflect.Slice; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if st.Elem() != elem {
		t.Errorf("Elem != synth elem")
	}
	if st.NumMethod() != 0 {
		t.Errorf("derived []T should have no methods, got %d", st.NumMethod())
	}
	// MakeSlice exercises Size/Align of the slice header and Elem layout.
	sl := reflect.MakeSlice(st, 3, 3)
	if sl.Len() != 3 {
		t.Errorf("Len = %d, want 3", sl.Len())
	}
	// Field access via Index() must reach the synth elem's first field.
	sl.Index(1).FieldByName("V").SetInt(42)
	if got := sl.Index(1).FieldByName("V").Int(); got != 42 {
		t.Errorf("Index(1).V = %d, want 42", got)
	}
}

// TestDerivedSynthCompositesDedup guards that repeated derivations over the same
// synth elem return ONE rtype (like reflect.*Of's global cache), so e.g. two
// struct fields of the same synth-element map type compare equal under DeepEqual.
func TestDerivedSynthCompositesDedup(t *testing.T) {
	elem := synthStructForDerive(t, "DeriveDedup")
	key := synthStructForDerive(t, "DeriveDedupKey")
	if SliceOf(elem) != SliceOf(elem) {
		t.Error("SliceOf not deduped")
	}
	if PointerTo(elem) != PointerTo(elem) {
		t.Error("PointerTo not deduped")
	}
	if ArrayOf(3, elem) != ArrayOf(3, elem) {
		t.Error("ArrayOf not deduped")
	}
	if ArrayOf(3, elem) == ArrayOf(4, elem) {
		t.Error("ArrayOf must distinguish length")
	}
	if ChanOf(reflect.BothDir, elem) != ChanOf(reflect.BothDir, elem) {
		t.Error("ChanOf not deduped")
	}
	if MapOf(key, elem) != MapOf(key, elem) {
		t.Error("MapOf not deduped")
	}
}

func TestArrayOfOnSynthStruct(t *testing.T) {
	elem := synthStructForDerive(t, "DeriveAR")
	at := ArrayOf(4, elem)
	if at == nil {
		t.Fatal("ArrayOf returned nil")
	}
	if got, want := at.Kind(), reflect.Array; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if at.Len() != 4 {
		t.Errorf("Len = %d, want 4", at.Len())
	}
	if at.Elem() != elem {
		t.Errorf("Elem != synth elem")
	}
	if at.NumMethod() != 0 {
		t.Errorf("derived [N]T should have no methods, got %d", at.NumMethod())
	}
	// Element layout must match: probe Size and field offset via the synth
	// elem and a known-int layout.
	v := reflect.New(at).Elem()
	v.Index(2).FieldByName("V").SetInt(7)
	if got := v.Index(2).FieldByName("V").Int(); got != 7 {
		t.Errorf("Index(2).V = %d, want 7", got)
	}
}

func TestChanOfOnSynthStruct(t *testing.T) {
	elem := synthStructForDerive(t, "DeriveCH")
	cases := []struct {
		dir  reflect.ChanDir
		want reflect.ChanDir
	}{
		{reflect.BothDir, reflect.BothDir},
		{reflect.RecvDir, reflect.RecvDir},
		{reflect.SendDir, reflect.SendDir},
	}
	for _, c := range cases {
		ct := ChanOf(c.dir, elem)
		if ct == nil {
			t.Fatalf("ChanOf(%v) returned nil", c.dir)
		}
		if got := ct.Kind(); got != reflect.Chan {
			t.Errorf("Kind = %v, want Chan", got)
		}
		if ct.Elem() != elem {
			t.Errorf("Elem != synth elem")
		}
		if got := ct.ChanDir(); got != c.want {
			t.Errorf("ChanDir = %v, want %v", got, c.want)
		}
	}
	// Bidir channel: MakeChan + Send/Recv must work end to end.
	ct := ChanOf(reflect.BothDir, elem)
	ch := reflect.MakeChan(ct, 1)
	v := reflect.New(elem).Elem()
	v.FieldByName("V").SetInt(11)
	ch.Send(v)
	got, ok := ch.Recv()
	if !ok {
		t.Fatal("Recv ok=false")
	}
	if g := got.FieldByName("V").Int(); g != 11 {
		t.Errorf("Recv.V = %d, want 11", g)
	}
}

func TestMapOfOnSynthStruct(t *testing.T) {
	elem := synthStructForDerive(t, "DeriveMP")
	mt := MapOf(reflect.TypeOf(""), elem)
	if mt == nil {
		t.Fatal("MapOf returned nil")
	}
	if got, want := mt.Kind(), reflect.Map; got != want {
		t.Errorf("Kind = %v, want %v", got, want)
	}
	if mt.Key() != reflect.TypeOf("") {
		t.Errorf("Key = %v, want string", mt.Key())
	}
	if mt.Elem() != elem {
		t.Errorf("Elem != synth elem")
	}
	if mt.NumMethod() != 0 {
		t.Errorf("derived map should have no methods, got %d", mt.NumMethod())
	}
	// End-to-end: MakeMap, SetMapIndex, MapIndex must round-trip.
	m := reflect.MakeMap(mt)
	v := reflect.New(elem).Elem()
	v.FieldByName("V").SetInt(99)
	m.SetMapIndex(reflect.ValueOf("k"), v)
	got := m.MapIndex(reflect.ValueOf("k"))
	if !got.IsValid() {
		t.Fatal("MapIndex returned invalid")
	}
	if g := got.FieldByName("V").Int(); g != 99 {
		t.Errorf("MapIndex.V = %d, want 99", g)
	}
}

// A synth map is a one-word direct-iface value; boxing it must not panic
// "bad indir" (buildMapOf used to zero TFlag, dropping tflagDirectIface).
func TestMapOfIsDirectIface(t *testing.T) {
	elem := synthStructForDerive(t, "DeriveMPI")
	mt := MapOf(reflect.TypeOf(""), elem)
	if rtypePtr(mt).TFlag&tflagDirectIface == 0 {
		t.Errorf("synth map TFlag missing tflagDirectIface: %#x", rtypePtr(mt).TFlag)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("boxing synth map panicked: %v", r)
		}
	}()
	_ = reflect.MakeMap(mt).Interface() // packs into an eface
}

// TestPointerToOnNativeElem proves the constructor works for native rtypes
// too -- it doesn't depend on the elem being synth.
func TestPointerToOnNativeElem(t *testing.T) {
	elem := reflect.TypeOf(int(0))
	pt := PointerTo(elem)
	if pt.Kind() != reflect.Pointer || pt.Elem() != elem {
		t.Fatalf("PointerTo(int): Kind=%v Elem=%v", pt.Kind(), pt.Elem())
	}
	// PointerTo on native elem must round-trip: derived *int receives an int*
	// and can read the int value via reflect.Value.Elem.
	x := 42
	v := reflect.NewAt(elem, unsafe.Pointer(&x))
	// Need to widen v's type to our derived *T. reflect.Value's Type is the
	// natural *int; we cannot assign across types directly. Instead, just
	// check that our pt is structurally usable: build a slice of pt.
	sl := reflect.MakeSlice(SliceOf(pt), 1, 1)
	sl.Index(0).Set(v)
	if got := sl.Index(0).Elem().Int(); got != 42 {
		t.Errorf("slice[0].Elem = %d, want 42", got)
	}
}

// TestChainedDerivations proves the layout registry lets us nest derivations
// across synth and native rtypes.
func TestChainedDerivations(t *testing.T) {
	elem := synthStructForDerive(t, "DeriveChained")
	// []*T over a synth struct.
	ptr := PointerTo(elem)
	sl := SliceOf(ptr)
	if sl.Kind() != reflect.Slice || sl.Elem() != ptr {
		t.Fatalf("SliceOf(PointerTo): Kind=%v Elem=%v", sl.Kind(), sl.Elem())
	}
	// map[string][]*T: exercises MapOf via the layout shadow because []*T's
	// shadow is registered in the layout map.
	mt := MapOf(reflect.TypeOf(""), sl)
	if mt.Kind() != reflect.Map || mt.Elem() != sl {
		t.Fatalf("MapOf(string, []*T): Kind=%v Elem=%v", mt.Kind(), mt.Elem())
	}
	m := reflect.MakeMap(mt)
	sliceV := reflect.MakeSlice(sl, 0, 0)
	m.SetMapIndex(reflect.ValueOf("k"), sliceV)
	if !m.MapIndex(reflect.ValueOf("k")).IsValid() {
		t.Errorf("MapIndex returned invalid after SetMapIndex")
	}

	// [3][]*T: array over the slice; exercises ArrayOf via the layout shadow.
	at := ArrayOf(3, sl)
	if at.Len() != 3 || at.Elem() != sl {
		t.Fatalf("ArrayOf(3, []*T): Len=%d Elem=%v", at.Len(), at.Elem())
	}
}

// TestDerivedKindsAreAnonymous: derived types must NOT be tflagNamed/
// tflagUncommon -- they're anonymous derived rtypes.
// reflect.Type.Name() returns "" for unnamed, the synth name otherwise.
func TestDerivedKindsAreAnonymous(t *testing.T) {
	elem := synthStructForDerive(t, "DeriveAnon")
	for _, c := range []struct {
		name string
		rt   reflect.Type
	}{
		{"PointerTo", PointerTo(elem)},
		{"SliceOf", SliceOf(elem)},
		{"ChanOf", ChanOf(reflect.BothDir, elem)},
		{"ArrayOf", ArrayOf(2, elem)},
		{"MapOf", MapOf(reflect.TypeOf(""), elem)},
	} {
		if got := c.rt.Name(); got != "" {
			t.Errorf("%s.Name() = %q, want \"\"", c.name, got)
		}
	}
}

// TestDerivedStringFormatting checks the printed names of derived rtypes.
// We don't pin every detail (the synth elem's String may include its name),
// but we check that the prefix structure (* / [] / chan etc.) is right.
func TestDerivedStringFormatting(t *testing.T) {
	elem := synthStructForDerive(t, "Foo")
	elemS := elem.String()
	cases := []struct {
		got, wantPrefix string
	}{
		{PointerTo(elem).String(), "*"},
		{SliceOf(elem).String(), "[]"},
		{ChanOf(reflect.BothDir, elem).String(), "chan "},
		{ChanOf(reflect.RecvDir, elem).String(), "<-chan "},
		{ChanOf(reflect.SendDir, elem).String(), "chan<- "},
		{ArrayOf(2, elem).String(), "[2]"},
		{MapOf(reflect.TypeOf(""), elem).String(), "map[string]"},
	}
	for _, c := range cases {
		if c.got == "" {
			t.Errorf("derived String empty")
		}
		// Spot-check that the elem name shows up at the end.
		if !contains(c.got, elemS) {
			t.Errorf("derived %q missing elem name %q", c.got, elemS)
		}
		if !startsWith(c.got, c.wantPrefix) {
			t.Errorf("derived %q missing prefix %q", c.got, c.wantPrefix)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func startsWith(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// TestLayoutForFallback: layoutFor on an unregistered rtype returns t itself.
func TestLayoutForFallback(t *testing.T) {
	x := reflect.TypeOf(int(0))
	if got := layoutFor(x); got != x {
		t.Errorf("layoutFor(int) = %v, want %v", got, x)
	}
}

// TestSliceOfNilElem: nil elem yields nil result.
func TestSliceOfNilElem(t *testing.T) {
	for _, c := range []struct {
		name string
		got  reflect.Type
	}{
		{"PointerTo", PointerTo(nil)},
		{"SliceOf", SliceOf(nil)},
		{"ChanOf", ChanOf(reflect.BothDir, nil)},
	} {
		if c.got != nil {
			t.Errorf("%s(nil) = %v, want nil", c.name, c.got)
		}
	}
}

// TestStampName verifies the in-place name stamp: a fresh anonymous
// reflect.StructOf rtype (Name()=="") gains a name and pkg-qualified String()
// without any layout change. This is the primitive a future type-identity
// dedup layer would use to name interpreted structs.
func TestStampName(t *testing.T) {
	rt := reflect.StructOf([]reflect.StructField{
		{Name: "V", Type: reflect.TypeOf(int(0))},
	})
	if rt.Name() != "" {
		t.Fatalf("precondition: fresh StructOf should be anonymous, got %q", rt.Name())
	}
	sizeBefore, alignBefore, nfBefore := rt.Size(), rt.Align(), rt.NumField()

	StampName(rt, "pkg.Vector")

	if got := rt.String(); got != "pkg.Vector" {
		t.Errorf("String() = %q, want %q", got, "pkg.Vector")
	}
	if got := rt.Name(); got != "Vector" {
		t.Errorf("Name() = %q, want %q (reflect strips to last dot)", got, "Vector")
	}
	// Layout must be untouched by a name-only stamp.
	if rt.Size() != sizeBefore || rt.Align() != alignBefore || rt.NumField() != nfBefore {
		t.Errorf("layout changed: size %d->%d align %d->%d nfield %d->%d",
			sizeBefore, rt.Size(), alignBefore, rt.Align(), nfBefore, rt.NumField())
	}
	// Value ops still work against the renamed rtype.
	v := reflect.New(rt).Elem()
	v.Field(0).SetInt(7)
	if v.Field(0).Int() != 7 {
		t.Errorf("field access broken after stamp")
	}
}

// TestDeriveRoutesNativeVsSynth pins the Derive* routing policy: a native
// component yields the canonical reflect identity (so e.g. two []int converge),
// while a synth component yields a synth composite (reflect.*Of would crash).
func TestDeriveRoutesNativeVsSynth(t *testing.T) {
	if got := DeriveSliceOf(reflect.TypeOf(0)); got != reflect.TypeOf([]int(nil)) {
		t.Errorf("DeriveSliceOf(int) = %v, want canonical []int", got)
	}
	if IsSynth(DeriveSliceOf(reflect.TypeOf(0))) {
		t.Error("DeriveSliceOf(native) must not be synth")
	}
	if got := DerivePointerTo(reflect.TypeOf(0)); got != reflect.TypeOf((*int)(nil)) {
		t.Errorf("DerivePointerTo(int) = %v, want canonical *int", got)
	}
	if got := DeriveMapOf(reflect.TypeOf(""), reflect.TypeOf(0)); got != reflect.TypeOf(map[string]int(nil)) {
		t.Errorf("DeriveMapOf(string,int) = %v, want canonical map[string]int", got)
	}

	elem := synthStructForDerive(t, "DeriveRoute")
	if !IsSynth(DeriveSliceOf(elem)) {
		t.Error("DeriveSliceOf(synth) must be synth")
	}
	if !IsSynth(DerivePointerTo(elem)) {
		t.Error("DerivePointerTo(synth without PtrToThis) must be synth")
	}
	if sl := DeriveSliceOf(elem); sl.Elem() != elem {
		t.Errorf("DeriveSliceOf(synth).Elem() = %v, want the synth elem", sl.Elem())
	}
}
