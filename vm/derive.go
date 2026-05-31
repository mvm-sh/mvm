package vm

import (
	"os"
	"reflect"
	"sync"
	"unsafe"

	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/runtype"
)

// reserveSynth gates the materialize-once reserve/fill path (cascade retirement).
// On: named method-bearing types get a reserved synth identity at materialize
// that attach fills in place, so composites capturing them need no patching.
// Covers non-struct kinds; structs stay on the swap path for now.
var reserveSynth = os.Getenv("MVM_RESERVE") == "1"

// synthReservation holds a named type's reserved value (method-set T) and
// pointer (method-set *T) rtypes, awaiting Fill at attach.
type synthReservation struct {
	value *runtype.Reservation
	ptr   *runtype.Reservation
}

var reservations = map[*mtype.Type]*synthReservation{} // guarded by derivedMu

func lookupReservation(t *mtype.Type) *synthReservation {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return reservations[t]
}

// isFieldClone reports whether t is a struct-field copy of a named method-bearing
// type: it carries the field name in t.Name but must resolve to Base's identity.
// A canonical defined type's Base is unnamed; defined-over-named is intercepted by
// definedOverBase before reaching here.
func isFieldClone(t *mtype.Type) bool {
	return t.Base != nil && t.Base.Name != "" &&
		t.Base.Kind() == t.Kind() && len(t.Base.Methods) > 0
}

// maybeReserve gives a named non-struct method-bearing type a reserved synth
// identity over layoutRT so attach fills methods in place. Returns layoutRT
// unchanged when the gate is off or t is not reservable.
func maybeReserve(t *mtype.Type, layoutRT reflect.Type) reflect.Type {
	if !reserveSynth || layoutRT == nil || t.Name == "" ||
		layoutRT.Kind() == reflect.Struct || !synthSupportedKind(layoutRT.Kind()) {
		return layoutRT
	}
	if lookupReservation(t) != nil {
		return t.Rtype
	}
	if isFieldClone(t) {
		return MaterializeRtype(t.Base)
	}
	if len(t.Methods) == 0 {
		return layoutRT
	}
	name := qualifiedTypeName(t)
	// Reserve the value rtype unconditionally: it gives T a writable, named
	// identity for ReservePtrMethods to wire *T into (PtrToThis on a shared/native
	// layout faults). Fill leaves it methodless when method-set(T) is empty.
	vr, err := runtype.ReserveMethods(layoutRT, name, t.PkgPath)
	if err != nil {
		return layoutRT
	}
	res := &synthReservation{value: vr}
	valueRT := vr.Type()
	if r, err := runtype.ReservePtrMethods(valueRT, "*"+name, t.PkgPath); err == nil {
		res.ptr = r
		AttachPtrDerived(t, r.Type())
	}
	derivedMu.Lock()
	reservations[t] = res
	derivedMu.Unlock()
	return valueRT
}

type derivedTypes struct {
	ptr     *mtype.Type
	slice   *mtype.Type
	array   map[int]*mtype.Type
	chans   map[reflect.ChanDir]*mtype.Type
	maps    map[*mtype.Type]*mtype.Type // keyed by this type, indexed by elem
	valMaps map[*mtype.Type]*mtype.Type // value is this type, indexed by key; lets the cascade reach map[K]T
}

var (
	// derivedMu serializes the derived/prior/synthIface side tables and the
	// RefreshRtype cascade. Contended only when parallel tests share a std
	// *Type; uncontended within one single-threaded Compiler.
	derivedMu       sync.Mutex
	derivedCache    = map[*mtype.Type]*derivedTypes{}
	priorRtypes     = map[*mtype.Type]reflect.Type{}
	synthIfaceCache = map[*mtype.Type]reflect.Type{}
)

func ensureDerived(t *mtype.Type) *derivedTypes {
	d := derivedCache[t]
	if d == nil {
		d = &derivedTypes{}
		derivedCache[t] = d
	}
	return d
}

func definedOverBase(t *mtype.Type) bool {
	if t.Base == nil || t.Base == t || t.ElemType != nil || t.KeyType != nil ||
		len(t.Fields) != 0 || len(t.Params) != 0 {
		return false
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Chan, reflect.Map, reflect.Struct:
		return true
	case reflect.Invalid:
		// Defined over an imported basic whose kind never resolved (`type
		// durationValue time.Duration`): recover the layout from Base.
		return true
	}
	return false
}

// MaterializeRtype builds and caches t.Rtype from t's symbolic graph (Kind +
// ElemType/KeyType/Fields/Params/Returns + ArrayLen/ChanDir/Variadic/Tags) when
// it is not already set, recursing into children first.
// This is the comp-side materialization that lets goparser build a *Type without an rtype.
//
// A named leaf (a primitive or struct that carries methods) must already hold
// its rtype so an un-materialized leaf here yields nil.
func MaterializeRtype(t *mtype.Type) reflect.Type {
	if t == nil {
		return nil
	}
	if t.Rtype != nil {
		return t.Rtype
	}
	if t.Placeholder {
		return nil // forward-declared struct/interface not yet finalized
	}
	// No own structure: materialize from the underlying (synth attach restores
	// the named identity).
	if definedOverBase(t) {
		if rt := MaterializeRtype(t.Base); rt != nil {
			t.Rtype = rt
			return rt
		}
	}
	var rt reflect.Type
	switch t.Kind() {
	case reflect.Pointer:
		elem := MaterializeRtype(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = derivePointerTo(elem)
	case reflect.Slice:
		elem := MaterializeRtype(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = deriveSliceOf(elem)
	case reflect.Array:
		elem := MaterializeRtype(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = deriveArrayOf(t.ArrayLen, elem)
	case reflect.Chan:
		elem := MaterializeRtype(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = deriveChanOf(t.ChanDir, elem)
	case reflect.Map:
		key, elem := MaterializeRtype(t.KeyType), MaterializeRtype(t.ElemType)
		if key == nil || elem == nil {
			return nil
		}
		rt = deriveMapOf(key, elem)
	case reflect.Func:
		in := make([]reflect.Type, len(t.Params))
		for i, p := range t.Params {
			if in[i] = MaterializeRtype(p); in[i] == nil {
				return nil
			}
		}
		out := make([]reflect.Type, len(t.Returns))
		for i, r := range t.Returns {
			if out[i] = MaterializeRtype(r); out[i] == nil {
				return nil
			}
		}
		rt = reflect.FuncOf(in, out, t.Variadic)
	case reflect.Struct:
		if len(t.Fields) == 0 && t.Base != nil && t.Base != t && t.Base.Kind() == reflect.Struct {
			// Defined type (type T1 T) whose Fields were cloned empty before the
			// underlying was finalized: materialize from the underlying's layout.
			rt = MaterializeRtype(t.Base)
			if rt == nil {
				return nil
			}
			t.Rtype = rt
			return rt
		}
		if t.Name != "" {
			// Named struct may be in a pointer cycle (field *T, or mutual T<->U):
			// install a placeholder rtype, materialize fields (a *T built then
			// resolves to it), then patch the placeholder in place.
			ph := mtype.NewPlaceholderRtype(t.Name)
			t.Rtype = ph
			for _, f := range t.Fields {
				MaterializeRtype(f)
			}
			if !fieldsMaterialized(t.Fields) {
				// A field references a not-yet-finalized placeholder (e.g. *T sibling):
				// reset Rtype so a later call retries once it is finalized.
				t.Rtype = nil
				return nil
			}
			mtype.PatchRtype(ph, mtype.StructOf(t.Fields, t.Embedded, t.Tags).Rtype)
			// PatchRtype keeps ph's placeholder name; stamp the real one.
			// Method-bearing types get theirs from attach instead.
			if len(t.Methods) == 0 {
				runtype.StampName(ph, qualifiedTypeName(t))
			}
			return ph
		}
		for _, f := range t.Fields {
			MaterializeRtype(f)
		}
		if !fieldsMaterialized(t.Fields) {
			return nil
		}
		rt = mtype.StructOf(t.Fields, t.Embedded, t.Tags).Rtype
	default:
		// Basic kind with no rtype yet: materialize to the canonical native basic
		// rtype (layout-correct). A named basic gets its method-bearing rtype from
		// the synth attach + cascade; this is the underlying for composites built
		// before attach.
		if rt = basicRtype(t.Kind()); rt == nil {
			return nil // genuinely un-materialized leaf
		}
	}
	rt = maybeReserve(t, rt)
	t.Rtype = rt
	return rt
}

func fieldsMaterialized(fields []*mtype.Type) bool {
	for _, f := range fields {
		if f.Rtype == nil && f.Kind() != reflect.Interface {
			return false
		}
	}
	return true
}

func basicRtype(k reflect.Kind) reflect.Type {
	return basicRtypes[k]
}

var basicRtypes = map[reflect.Kind]reflect.Type{
	reflect.Bool:          reflect.TypeOf(false),
	reflect.Int:           reflect.TypeOf(int(0)),
	reflect.Int8:          reflect.TypeOf(int8(0)),
	reflect.Int16:         reflect.TypeOf(int16(0)),
	reflect.Int32:         reflect.TypeOf(int32(0)),
	reflect.Int64:         reflect.TypeOf(int64(0)),
	reflect.Uint:          reflect.TypeOf(uint(0)),
	reflect.Uint8:         reflect.TypeOf(uint8(0)),
	reflect.Uint16:        reflect.TypeOf(uint16(0)),
	reflect.Uint32:        reflect.TypeOf(uint32(0)),
	reflect.Uint64:        reflect.TypeOf(uint64(0)),
	reflect.Uintptr:       reflect.TypeOf(uintptr(0)),
	reflect.Float32:       reflect.TypeOf(float32(0)),
	reflect.Float64:       reflect.TypeOf(float64(0)),
	reflect.Complex64:     reflect.TypeOf(complex64(0)),
	reflect.Complex128:    reflect.TypeOf(complex128(0)),
	reflect.String:        reflect.TypeOf(""),
	reflect.UnsafePointer: reflect.TypeOf(unsafe.Pointer(nil)),
	reflect.Interface:     mtype.AnyRtype,
}

// The Sym* derived constructors are goparser's parse-time entry points: they
// memoize and register the derived *Type in t's derived cache (so the post-attach
// cascade can refresh it) but leave Rtype nil -- comp materializes it later via
// MaterializeRtype. They are the lazy counterparts of PointerTo/SliceOf/... .

// SymPtr returns the canonical *t, registered in t's derived cache, Rtype nil.
func SymPtr(t *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.ptr != nil {
		return d.ptr
	}
	d.ptr = mtype.SymPtr(t)
	return d.ptr
}

// SymSlice returns the canonical []t, registered in t's derived cache, Rtype nil.
func SymSlice(t *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.slice != nil {
		return d.slice
	}
	d.slice = mtype.SymSlice(t)
	return d.slice
}

// SymArray returns the canonical [n]t, registered in t's derived cache, Rtype nil.
func SymArray(n int, t *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.array == nil {
		d.array = map[int]*mtype.Type{}
	} else if a := d.array[n]; a != nil {
		return a
	}
	a := mtype.SymArray(n, t)
	d.array[n] = a
	return a
}

// SymChan returns the canonical chan-t, registered in t's derived cache, Rtype nil.
func SymChan(dir reflect.ChanDir, t *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.chans == nil {
		d.chans = map[reflect.ChanDir]*mtype.Type{}
	} else if c := d.chans[dir]; c != nil {
		return c
	}
	c := mtype.SymChan(dir, t)
	d.chans[dir] = c
	return c
}

// SymMap returns the canonical map[k]e, registered in k's derived cache, Rtype nil.
func SymMap(k, e *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(k)
	if d.maps == nil {
		d.maps = map[*mtype.Type]*mtype.Type{}
	} else if m := d.maps[e]; m != nil {
		return m
	}
	m := mtype.SymMap(k, e)
	d.maps[e] = m
	registerValMap(e, k, m)
	return m
}

// PointerTo returns the canonical pointer type with element t, memoized.
func PointerTo(t *mtype.Type) *mtype.Type {
	if t.Rtype == nil {
		// Un-materialized elem: defer to MaterializeRtype, don't crash in reflect.PointerTo(nil).
		return SymPtr(t)
	}
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.ptr != nil {
		return d.ptr
	}
	d.ptr = &mtype.Type{Name: t.Name, Rtype: derivePointerTo(t.Rtype), ElemType: t}
	return d.ptr
}

// ArrayOf returns the canonical [length]t type, memoized.
func ArrayOf(length int, t *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.array == nil {
		d.array = map[int]*mtype.Type{}
	} else if a := d.array[length]; a != nil {
		return a
	}
	a := &mtype.Type{Rtype: deriveArrayOf(length, t.Rtype), ElemType: t, ArrayLen: length}
	d.array[length] = a
	return a
}

// SliceOf returns the canonical []t type, memoized.
func SliceOf(t *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.slice != nil {
		return d.slice
	}
	d.slice = &mtype.Type{Rtype: deriveSliceOf(t.Rtype), ElemType: t}
	return d.slice
}

// MapOf returns the canonical map[k]e type, memoized on the key, indexed by elem.
func MapOf(k, e *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(k)
	if d.maps == nil {
		d.maps = map[*mtype.Type]*mtype.Type{}
	} else if m := d.maps[e]; m != nil {
		return m
	}
	m := &mtype.Type{Rtype: deriveMapOf(k.Rtype, e.Rtype), ElemType: e, KeyType: k}
	d.maps[e] = m
	registerValMap(e, k, m)
	return m
}

// registerValMap records map[k]e in e's valMaps so refreshLocked can rebuild it
// when e's rtype is swapped by attach. Caller holds derivedMu.
func registerValMap(e, k, m *mtype.Type) {
	d := ensureDerived(e)
	if d.valMaps == nil {
		d.valMaps = map[*mtype.Type]*mtype.Type{}
	}
	d.valMaps[k] = m
}

// ChanOf returns the canonical chan-elem type with the given direction, memoized.
func ChanOf(dir reflect.ChanDir, elem *mtype.Type) *mtype.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(elem)
	if d.chans == nil {
		d.chans = map[reflect.ChanDir]*mtype.Type{}
	} else if c := d.chans[dir]; c != nil {
		return c
	}
	c := &mtype.Type{Rtype: deriveChanOf(dir, elem.Rtype), ElemType: elem, ChanDir: dir}
	d.chans[dir] = c
	return c
}

// derivePointerTo and its SliceOf/ArrayOf/ChanOf/MapOf siblings build a derived
// rtype: runtype.* for synth elems (reflect.*Of crashes on them via
// resolveNameOff), reflect.* otherwise to keep native identity. PointerTo uses
// reflect.PointerTo when a synth elem's PtrToThis is wired (unifies *T identity).
func derivePointerTo(elem reflect.Type) reflect.Type {
	if runtype.IsSynth(elem) && !runtype.HasPtrToThis(elem) {
		return runtype.PointerTo(elem)
	}
	return reflect.PointerTo(elem)
}

func deriveSliceOf(elem reflect.Type) reflect.Type {
	if runtype.IsSynth(elem) {
		return runtype.SliceOf(elem)
	}
	return reflect.SliceOf(elem)
}

func deriveArrayOf(n int, elem reflect.Type) reflect.Type {
	if runtype.IsSynth(elem) {
		return runtype.ArrayOf(n, elem)
	}
	return reflect.ArrayOf(n, elem)
}

func deriveChanOf(dir reflect.ChanDir, elem reflect.Type) reflect.Type {
	if runtype.IsSynth(elem) {
		return runtype.ChanOf(dir, elem)
	}
	return reflect.ChanOf(dir, elem)
}

func deriveMapOf(key, elem reflect.Type) reflect.Type {
	if runtype.IsSynth(key) || runtype.IsSynth(elem) {
		return runtype.MapOf(key, elem)
	}
	return reflect.MapOf(key, elem)
}

// RefreshRtype swaps t.Rtype to newRT and cascades through every derived *Type
// (*T, []T, [N]T, chan T, map[t]E) so compiler-captured derived rtypes track the
// swap. Uses runtype.* (reflect.*Of crashes on synth rtypes). Maps cascade only
// the t-as-key direction; t-as-element maps live under the key's cache.
func RefreshRtype(t *mtype.Type, newRT reflect.Type) {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	refreshLocked(t, newRT)
}

func refreshLocked(t *mtype.Type, newRT reflect.Type) {
	if newRT == nil || newRT == t.Rtype {
		return
	}
	if priorRtypes[t] == nil {
		priorRtypes[t] = t.Rtype
	}
	t.Rtype = newRT
	d := derivedCache[t]
	if d == nil {
		return
	}
	if d.ptr != nil {
		refreshLocked(d.ptr, runtype.PointerTo(newRT))
	}
	if d.slice != nil {
		refreshLocked(d.slice, runtype.SliceOf(newRT))
	}
	for length, a := range d.array {
		refreshLocked(a, runtype.ArrayOf(length, newRT))
	}
	for dir, c := range d.chans {
		refreshLocked(c, runtype.ChanOf(dir, newRT))
	}
	for e, mt := range d.maps {
		refreshLocked(mt, runtype.MapOf(newRT, e.Rtype))
	}
	for k, mt := range d.valMaps {
		refreshLocked(mt, runtype.MapOf(k.Rtype, newRT))
	}
}

// PriorRtype returns t's pre-synth-swap rtype, or nil if never swapped.
func PriorRtype(t *mtype.Type) reflect.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return priorRtypes[t]
}

// cachedSynthIface returns t's cached method-bearing synth interface rtype,
// building it via build on first use; a nil result is not cached (the AnyRtype
// bridge stays and a later call retries).
func cachedSynthIface(t *mtype.Type, build func() reflect.Type) reflect.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	if st := synthIfaceCache[t]; st != nil {
		return st
	}
	if st := build(); st != nil {
		synthIfaceCache[t] = st
		return st
	}
	return nil
}

// AttachPtrDerived records newPtrRT (a *T-with-methods rtype) as t's derived
// pointer type, materializing the slot if absent so a later PointerTo(t)
// returns it instead of a fresh methodless *T.
func AttachPtrDerived(t *mtype.Type, newPtrRT reflect.Type) {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.ptr == nil {
		d.ptr = &mtype.Type{Name: t.Name, Rtype: newPtrRT, ElemType: t}
	} else {
		refreshLocked(d.ptr, newPtrRT)
	}
}

const canonicalTypeMaxDepth = 1024

// CanonicalType walks the Base chain to the source *Type a struct-field copy
// derived from (t itself if Base is nil); depth-capped against cyclic Base.
// Clones must route through this to observe synth-cascade updates, which only
// touch canonical *Types.
func CanonicalType(t *mtype.Type) *mtype.Type {
	start := t
	for i := 0; i < canonicalTypeMaxDepth && t != nil && t.Base != nil; i++ {
		// Stop at a synth/method-bearing named type: crossing its named->underlying
		// Base link (type Grams int) would drop the synth rtype. Holds only
		// post-attach, so the pre-attach walk is unchanged.
		if t.Rtype != nil && (runtype.IsSynth(t.Rtype) || t.Rtype.NumMethod() > 0) {
			return t
		}
		t = t.Base
	}
	if t != nil && t.Base != nil {
		return start
	}
	return t
}

// LiveFieldRtype returns the current rtype for field f when rebuilding a struct.
// Clones (Base != nil) and derived types over a clone aren't refreshed by the
// in-Type cascade, so follow Base and re-derive.
func LiveFieldRtype(f *mtype.Type) reflect.Type {
	if f == nil {
		return nil
	}
	canonical := CanonicalType(f)
	if canonical == nil || canonical.Rtype == nil {
		return nil
	}
	// Derived type over a clone elem/key: re-derive from the canonical inner to
	// pick up cascade updates landed only on the canonical's derived chain.
	if canonical.ElemType != nil && canonical.ElemType.Base != nil {
		// A NAMED value-composite (e.g. `type Trace []Frame`) carries its own
		// method-bearing rtype; re-deriving from the element would drop the name
		// and methods. Its element is kept fresh by PatchSynthSliceElem. Pointers
		// have no identity beyond *elem, so they always re-derive.
		if canonical.Name != "" && canonical.Rtype.Kind() != reflect.Pointer {
			return canonical.Rtype
		}
		elemC := CanonicalType(canonical.ElemType)
		switch canonical.Rtype.Kind() {
		case reflect.Pointer:
			return derivePointerTo(elemC.Rtype)
		case reflect.Slice:
			return deriveSliceOf(elemC.Rtype)
		case reflect.Array:
			return deriveArrayOf(canonical.Rtype.Len(), elemC.Rtype)
		case reflect.Chan:
			return deriveChanOf(canonical.Rtype.ChanDir(), elemC.Rtype)
		case reflect.Map:
			keyC := canonical.KeyType
			if keyC != nil {
				keyC = CanonicalType(keyC)
			}
			keyRT := canonical.Rtype.Key()
			if keyC != nil && keyC.Rtype != nil {
				keyRT = keyC.Rtype
			}
			return deriveMapOf(keyRT, elemC.Rtype)
		}
	}
	return canonical.Rtype
}

// PatchSynthStructFields patches t's struct field rtypes in place to their live
// (post-attach) rtypes, preserving t.Rtype identity; layout-safe per field via
// SamePtrLayout. Serialized under mtype's StructOf lock (which reads shared
// cached field rtypes); live rtypes are computed before locking since
// LiveFieldRtype takes derivedMu.
func PatchSynthStructFields(t *mtype.Type) {
	if t == nil || t.Rtype == nil || t.Rtype.Kind() != reflect.Struct {
		return
	}
	if t.Rtype.NumField() != len(t.Fields) {
		return
	}
	lives := make([]reflect.Type, len(t.Fields))
	for i, f := range t.Fields {
		lives[i] = LiveFieldRtype(f)
	}
	mtype.WithStructTypesLock(func() {
		for i, live := range lives {
			if live == nil || live.Kind() == reflect.Interface {
				continue
			}
			if t.Rtype.Field(i).Type == live {
				continue
			}
			if !runtype.SamePtrLayout(t.Rtype.Field(i).Type, live) {
				continue
			}
			runtype.PatchStructField(t.Rtype, i, live)
		}
	})
}

// PatchSynthSliceElem swaps a named synth slice type's frozen element for its
// canonical cascade-refreshed element (t.Base.Rtype.Elem()), preserving t's own
// methods. Only synth rtypes are touched -- a native slice rtype is shared with
// reflect.
func PatchSynthSliceElem(t *mtype.Type) {
	if t == nil || t.Rtype == nil || t.Rtype.Kind() != reflect.Slice {
		return
	}
	if !runtype.IsSynth(t.Rtype) {
		return
	}
	if t.Base == nil || t.Base.Rtype == nil || t.Base.Rtype.Kind() != reflect.Slice {
		return
	}
	live := t.Base.Rtype.Elem()
	if live == nil || live == t.Rtype.Elem() {
		return
	}
	if !runtype.SamePtrLayout(t.Rtype.Elem(), live) {
		return
	}
	runtype.PatchSliceElem(t.Rtype, live)
}
