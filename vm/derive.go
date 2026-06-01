package vm

import (
	"reflect"
	"sync"
	"unsafe"

	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/runtype"
)

// synthReservation holds a named type's reserved value (method-set T) and
// pointer (method-set *T) rtypes, awaiting Fill at attach.
type synthReservation struct {
	value *runtype.Reservation
	ptr   *runtype.Reservation
}

var reservations = map[*mtype.Type]*synthReservation{} // guarded by derivedMu

// sharedStructs shares one reserved rtype per (name, layout) across Evals so a
// process-global registry keyed by reflect.Type (gob's nameToConcreteType) sees a
// single rtype per name. Sound only for methodless-table types: a method stub
// captures the attaching *Machine, but a methodless identity is a pure carrier.
type sharedStructKey struct {
	name      string
	layoutSig string
}

var sharedStructs = map[sharedStructKey]*synthReservation{} // guarded by derivedMu

// hasReservableMethods reports whether t carries any method worth reserving an
// identity for. The method pre-pass (comp.preregisterMethods) populates a slot's
// Sig before the body compiles, so a slot counts when it is either resolved (a
// compiled code address / embedded dispatch) or carries a symbolic Sig.
func hasReservableMethods(t *mtype.Type) bool {
	for i := range t.Methods {
		if t.Methods[i].IsResolved() || t.Methods[i].Sig != nil {
			return true
		}
	}
	return false
}

// hasSynthTableMethods reports whether any method has a supported detectShape, so
// attach installs a Machine-bound native stub (then the rtype can't be shared).
// method.Rtype is often nil pre-attach; materialize Sig to read the shape.
func hasSynthTableMethods(t *mtype.Type) bool {
	for _, method := range t.Methods {
		sig := method.Rtype
		if sig == nil && method.Sig != nil {
			sig = MaterializeRtype(method.Sig)
		}
		if sig == nil {
			return true // unknown sig: assume table method, don't share
		}
		if _, ok := detectShape(sig); ok {
			return true
		}
	}
	return false
}

func lookupReservation(t *mtype.Type) *synthReservation {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return reservations[t]
}

// isFieldClone reports whether t is a struct-field copy of a named type that must
// resolve to Base's identity: it carries the field name in t.Name with a cleared
// PkgPath. A canonical defined type's Base is unnamed; defined-over-named is
// intercepted by definedOverBase before reaching here.
func isFieldClone(t *mtype.Type) bool {
	if t.Defined {
		return false // a top-level `type X T` definition owns its identity
	}
	if t.Base == nil || t.Base.Name == "" || t.Base.Kind() != t.Kind() {
		return false
	}
	// Method-bearing base: the clone shares Base's identity + methods. Methodless
	// named-struct base: still a clone -- route to Base for the canonical qualified
	// identity, else the container's field stamps a bare name and gets patched.
	return len(t.Base.Methods) > 0 || (t.Kind() == reflect.Struct && t.Base.PkgPath != "")
}

// maybeReserve gives a named non-struct method-bearing type a reserved synth
// identity over layoutRT so attach fills methods in place. Returns layoutRT
// unchanged when t is not reservable.
func maybeReserve(t *mtype.Type, layoutRT reflect.Type) reflect.Type {
	if layoutRT == nil || t.Name == "" ||
		layoutRT.Kind() == reflect.Struct || !synthSupportedKind(layoutRT.Kind()) {
		return layoutRT
	}
	if lookupReservation(t) != nil {
		return t.Rtype
	}
	if isFieldClone(t) {
		return MaterializeRtype(t.Base)
	}
	if !hasReservableMethods(t) {
		return layoutRT
	}
	return reserveValueAndPtr(t, layoutRT)
}

// reserveValueAndPtr reserves t's value identity over layoutRT and, when
// possible, its *T identity, recording both and wiring *T via AttachPtrDerived.
// The value rtype is reserved unconditionally: it gives T a writable, named
// identity for ReservePtrMethods to wire *T into (PtrToThis on a shared/native
// layout faults). Fill leaves it methodless when method-set(T) is empty. Returns
// layoutRT unchanged if the value reservation itself fails.
func reserveValueAndPtr(t *mtype.Type, layoutRT reflect.Type) reflect.Type {
	name := qualifiedTypeName(t)
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

// reserveDefinedOverBase reserves the identity of a defined type (`type T Base`)
// over its already-materialized base layout. A defined type with its OWN methods
// (e.g. `type ipNetValue net.IPNet`) reserves directly: maybeReserve skips
// struct-kind layouts and treats a named base as a field clone, neither of which
// fits a genuine defined type. A methodless or field-clone type defers to
// maybeReserve unchanged (field clones carry no own methods, so the gate is safe).
func reserveDefinedOverBase(t *mtype.Type, base reflect.Type) reflect.Type {
	if lookupReservation(t) != nil {
		return t.Rtype
	}
	if t.Name != "" && hasReservableMethods(t) {
		return reserveValueAndPtr(t, base)
	}
	return maybeReserve(t, base)
}

// maybeReserveStruct reserves a named struct's identity over a provisional layout
// (so a *T field cycle resolves to it), materializes fields, then fills the real
// layout in place -- attach fills methods into the same identity, so composites
// that captured it need no patching.
// handled=false: methodless (native path); rt=nil: a field is not yet finalized,
// retry later (reservation kept).
func maybeReserveStruct(t *mtype.Type) (rt reflect.Type, handled bool) {
	// A struct field clone (an embedded field, or any field carrying its type's
	// name) must resolve to its canonical Base identity, not reserve its own --
	// else an embedded main.Point field gets a distinct "Point" rtype and the
	// composite literal's main.Point value is not assignable to it. Mirrors the
	// isFieldClone branch in maybeReserve.
	if isFieldClone(t) {
		rt := MaterializeRtype(t.Base)
		t.Rtype = rt
		return rt, true
	}
	res := lookupReservation(t)
	if res == nil {
		if !hasReservableMethods(t) {
			return nil, false // methodless struct: native identity is stable already
		}
		name := qualifiedTypeName(t)
		vr, err := runtype.ReserveMethods(mtype.NewPlaceholderRtype(t.Name), name, t.PkgPath)
		if err != nil {
			return nil, false
		}
		res = &synthReservation{value: vr}
		if pr, err := runtype.ReservePtrMethods(vr.Type(), "*"+name, t.PkgPath); err == nil {
			res.ptr = pr
			AttachPtrDerived(t, pr.Type())
		}
		derivedMu.Lock()
		reservations[t] = res
		derivedMu.Unlock()
	}
	reserved := res.value.Type()
	t.Rtype = reserved // stable identity for field cycles during this pass
	for _, f := range t.Fields {
		MaterializeRtype(f)
	}
	if !fieldsMaterialized(t.Fields) {
		t.Rtype = nil // a field references a not-yet-finalized sibling; retry later
		return nil, true
	}
	realLayout := mtype.StructOf(t.Fields, t.Embedded, t.Tags).Rtype
	// Methodless-table identity is safe to share across Evals (see sharedStructs).
	if !hasSynthTableMethods(t) {
		key := sharedStructKey{name: qualifiedTypeName(t), layoutSig: realLayout.String()}
		derivedMu.Lock()
		if shared := sharedStructs[key]; shared != nil {
			sharedRT := shared.value.Type()
			reservations[t] = shared
			derivedMu.Unlock()
			t.Rtype = sharedRT
			if shared.ptr != nil {
				AttachPtrDerived(t, shared.ptr.Type())
			}
			return sharedRT, true
		}
		// Fill before publishing so a concurrent hit never adopts the placeholder.
		runtype.FillStructLayout(reserved, realLayout)
		sharedStructs[key] = res
		derivedMu.Unlock()
		t.Rtype = reserved
		return reserved, true
	}
	runtype.FillStructLayout(reserved, realLayout)
	t.Rtype = reserved
	return reserved, true
}

type derivedTypes struct {
	ptr   *mtype.Type
	slice *mtype.Type
	array map[int]*mtype.Type
	chans map[reflect.ChanDir]*mtype.Type
	maps  map[*mtype.Type]*mtype.Type // keyed by this type, indexed by elem
}

var (
	// derivedMu serializes the derived/synthIface side tables. Contended only
	// when parallel tests share a std *Type; uncontended within one
	// single-threaded Compiler.
	derivedMu       sync.Mutex
	derivedCache    = map[*mtype.Type]*derivedTypes{}
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
	// No own structure: materialize from the underlying. A method-bearing
	// defined-over-basic type (e.g. `type Confidence int` with methods) reserves
	// its identity over the base layout so attach fills methods in place.
	if definedOverBase(t) {
		if base := MaterializeRtype(t.Base); base != nil {
			rt := reserveDefinedOverBase(t, base)
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
		rt = runtype.DerivePointerTo(elem)
	case reflect.Slice:
		elem := MaterializeRtype(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveSliceOf(elem)
	case reflect.Array:
		elem := MaterializeRtype(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveArrayOf(t.ArrayLen, elem)
	case reflect.Chan:
		elem := MaterializeRtype(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveChanOf(t.ChanDir, elem)
	case reflect.Map:
		key, elem := MaterializeRtype(t.KeyType), MaterializeRtype(t.ElemType)
		if key == nil || elem == nil {
			return nil
		}
		rt = runtype.DeriveMapOf(key, elem)
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
			if rt, handled := maybeReserveStruct(t); handled {
				return rt
			}
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
		// rtype (layout-correct). A named basic gets its method-bearing identity
		// from maybeReserve below, which attach later fills in place.
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
// memoize and register the derived *Type in t's derived cache but leave Rtype
// nil -- comp materializes it later via MaterializeRtype. They are the lazy
// counterparts of PointerTo/SliceOf/... .

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
	d.ptr = &mtype.Type{Name: t.Name, Rtype: runtype.DerivePointerTo(t.Rtype), ElemType: t}
	return d.ptr
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
// returns it instead of a fresh methodless *T. The reserve path wires the *T
// identity once at materialize, so an existing slot just adopts newPtrRT.
func AttachPtrDerived(t *mtype.Type, newPtrRT reflect.Type) {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := ensureDerived(t)
	if d.ptr == nil {
		d.ptr = &mtype.Type{Name: t.Name, Rtype: newPtrRT, ElemType: t}
	} else {
		d.ptr.Rtype = newPtrRT
	}
}

const canonicalTypeMaxDepth = 1024

// CanonicalType walks the Base chain to the source *Type a struct-field copy
// derived from (t itself if Base is nil); depth-capped against cyclic Base.
// Used by symbol resolution to match a clone against its defining identity.
func CanonicalType(t *mtype.Type) *mtype.Type {
	start := t
	for i := 0; i < canonicalTypeMaxDepth && t != nil && t.Base != nil; i++ {
		// Stop at a synth/method-bearing named type: crossing its named->underlying
		// Base link (type Grams int) would drop the named method-bearing identity.
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
