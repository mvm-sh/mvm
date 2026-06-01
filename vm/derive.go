package vm

import (
	"fmt"
	"os"
	"path"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/runtype"
)

// reserveSynth gates the materialize-once reserve/fill path (cascade retirement).
// Default on: a named method-bearing type gets a reserved synth identity at
// materialize that attach fills in place, so composites capturing it need no
// patching. MVM_RESERVE=0 falls back to the swap+cascade path during the
// transition; the gate and the cascade are removed once the flip is settled.
var reserveSynth = os.Getenv("MVM_RESERVE") != "0"

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

// structMatProbe logs each named-struct materialization (MVM_STRUCTMAT=1) for the
// reflect-free-struct-generate work: type, *Type identity, caller.
var structMatProbe = func(*mtype.Type) {}

func init() {
	if os.Getenv("MVM_STRUCTMAT") == "" {
		return
	}
	structMatProbe = func(t *mtype.Type) {
		if t.Name == "" || t.Kind() != reflect.Struct {
			return
		}
		var pcs [16]uintptr
		n := runtime.Callers(2, pcs[:])
		frames := runtime.CallersFrames(pcs[:n])
		var caller string
		for {
			fr, more := frames.Next()
			if !strings.Contains(fr.Function, "vm.MaterializeRtype") &&
				!strings.Contains(fr.Function, "vm.structMatProbe") &&
				!strings.Contains(fr.Function, "vm.deriv") {
				caller = fr.Function + " " + path.Base(fr.File) + ":" + itoa(fr.Line)
				break
			}
			if !more {
				break
			}
		}
		pre := "fresh"
		if t.Rtype != nil {
			pre = "preset"
		}
		fmt.Fprintf(os.Stderr, "structmat %s.%s id=%p %s <- %s\n", t.PkgPath, t.Name, t, pre, caller)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func lookupReservation(t *mtype.Type) *synthReservation {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return reservations[t]
}

// cascadeProbe logs each real cascade action (MVM_CASCADEPROBE=1) -- the swap +
// in-place patch work reserve aims to eliminate. Drives the residual to zero before
// the cascade funcs are deleted.
var cascadeProbe = os.Getenv("MVM_CASCADEPROBE") != ""

func cascadeHit(what string, t *mtype.Type) {
	if !cascadeProbe || t == nil {
		return
	}
	rts := ""
	if t.Rtype != nil {
		rts = t.Rtype.String()
	}
	fmt.Fprintf(os.Stderr, "CASCADE-HIT %s name=%q rtype=%q methods=%d\n", what, t.PkgPath+"."+t.Name, rts, len(t.Methods))
}

// isFieldClone reports whether t is a struct-field copy of a named type that must
// resolve to Base's identity: it carries the field name in t.Name with a cleared
// PkgPath. A canonical defined type's Base is unnamed; defined-over-named is
// intercepted by definedOverBase before reaching here.
func isFieldClone(t *mtype.Type) bool {
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
	if reserveSynth && t.Name != "" && hasReservableMethods(t) {
		return reserveValueAndPtr(t, base)
	}
	return maybeReserve(t, base)
}

// maybeReserveStruct reserves a named struct's identity over a provisional layout
// (so a *T field cycle resolves to it), materializes fields, then fills the real
// layout in place -- attach fills methods in place, so no cascade patching.
// handled=false: gate off or methodless (native path); rt=nil: a field is not yet
// finalized, retry later (reservation kept).
func maybeReserveStruct(t *mtype.Type) (rt reflect.Type, handled bool) {
	if !reserveSynth {
		return nil, false
	}
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
	structMatProbe(t)
	if t.Rtype != nil {
		return t.Rtype
	}
	if t.Placeholder {
		return nil // forward-declared struct/interface not yet finalized
	}
	// No own structure: materialize from the underlying. A method-bearing
	// defined-over-basic type (e.g. `type Confidence int` with methods) reserves
	// over the base layout so attach fills in place; otherwise the swap path runs.
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
			cascadeHit("PatchStructField", t)
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
	cascadeHit("PatchSliceElem", t)
	runtype.PatchSliceElem(t.Rtype, live)
}
