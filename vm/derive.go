package vm

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/runtype"
)

// synthReservation holds a named type's reserved value (method-set T) and
// pointer (method-set *T) rtypes, awaiting Fill at attach.
type synthReservation struct {
	value *runtype.Reservation // method-set T
	ptr   *runtype.Reservation // method-set *T
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

// sharedCarriers shares one methodless named non-struct identity per (name,
// layout) across Evals and field clones; sound for the same reason as sharedStructs.
var sharedCarriers = map[sharedStructKey]*synthReservation{} // guarded by derivedMu

// sharedMethodStructs shares a method-bearing struct's reserved identity across
// re-materializations within ONE Machine, so a drop-retry re-Eval reusing the same
// Go type does not mint a distinct rtype each pass (which breaks reflect.Type ==).
// Machine-keyed because a method stub closes over the attaching Machine; reuse
// under a different Machine would dispatch to a dead one. Machine from ActiveMachine,
// set by the interp around materialize+attach.
type methodStructKey struct {
	machine   *Machine
	name      string
	layoutSig string
	methodSig string
}

var sharedMethodStructs = map[methodStructKey]*synthReservation{} // guarded by derivedMu

// methodFingerprint is the set of resolved/symbolic method indices (= the method
// name set, IDs being global per name), so two same-name+layout types with
// different method sets do not share one identity.
func methodFingerprint(t *mtype.Type) string {
	var b strings.Builder
	for i := range t.Methods {
		if t.Methods[i].IsResolved() || t.Methods[i].Sig != nil {
			fmt.Fprintf(&b, "%d,", i)
		}
	}
	return b.String()
}

// materializingRtypes holds struct layout rtypes whose placeholder/reservation is
// installed but not yet patched to the real layout (in-flight). A struct that
// embeds one of these by value cannot size itself correctly yet -- the placeholder
// is word-sized -- so it defers its own finalization (pendingFinalize) until the
// dependency is filled. Guarded by derivedMu.
var materializingRtypes = map[reflect.Type]bool{}

// pendingFinalize holds structs that installed their placeholder but deferred the
// real-layout patch because a by-value field was still in-flight. FinalizeDeferred
// drains them once the cycle settles. Guarded by derivedMu. pendingCount mirrors
// its size for a lock-free fast check on the hot MaterializeRtype early return,
// which is empty whenever the program has no such cycle (the common case).
var (
	pendingFinalize = map[*mtype.Type]bool{}
	pendingCount    atomic.Int64
)

// pendingGeom holds map/array rtypes derived over an in-flight placeholder key or
// elem; RebuildMap/ArrayGeometry fixes their stride once it is final. Guarded by derivedMu.
var (
	pendingGeom      = map[reflect.Type]bool{}
	pendingGeomCount atomic.Int64
)

func markPendingGeom(rt reflect.Type) {
	if rt == nil {
		return
	}
	derivedMu.Lock()
	if !pendingGeom[rt] {
		pendingGeom[rt] = true
		pendingGeomCount.Add(1)
	}
	derivedMu.Unlock()
}

func isPendingGeom(rt reflect.Type) bool {
	if rt == nil || pendingGeomCount.Load() == 0 {
		return false
	}
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return pendingGeom[rt]
}

// propagateGeom marks a named map/array carrier pending when the unnamed layout it
// copied is pending: reserveMap/reserveArray copy the stale geometry by value.
func propagateGeom(layoutRT, carrier reflect.Type) reflect.Type {
	if k := layoutRT.Kind(); k != reflect.Map && k != reflect.Array {
		return carrier
	}
	if carrier != layoutRT && isPendingGeom(layoutRT) {
		markPendingGeom(carrier)
	}
	return carrier
}

// takePendingGeom drains the tracked rtypes, partitioned by kind: arrays recompute
// inside the FinalizeDeferred fixpoint (a struct re-patch reads their stride), maps after.
func takePendingGeom() (arrays, maps []reflect.Type) {
	if pendingGeomCount.Load() == 0 {
		return nil, nil
	}
	derivedMu.Lock()
	for rt := range pendingGeom {
		switch rt.Kind() {
		case reflect.Array:
			arrays = append(arrays, rt)
		case reflect.Map:
			maps = append(maps, rt)
		}
	}
	pendingGeom = map[reflect.Type]bool{}
	pendingGeomCount.Store(0)
	derivedMu.Unlock()
	return arrays, maps
}

var materializingCount atomic.Int64 // lock-free mirror of len(materializingRtypes)

func markMaterializing(rt reflect.Type) {
	if rt == nil {
		return
	}
	derivedMu.Lock()
	if !materializingRtypes[rt] {
		materializingRtypes[rt] = true
		materializingCount.Add(1)
	}
	derivedMu.Unlock()
}

func clearMaterializing(rt reflect.Type) {
	if rt == nil {
		return
	}
	derivedMu.Lock()
	if materializingRtypes[rt] {
		delete(materializingRtypes, rt)
		materializingCount.Add(-1)
	}
	derivedMu.Unlock()
}

func isMaterializing(rt reflect.Type) bool {
	if rt == nil || materializingCount.Load() == 0 {
		return false
	}
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return materializingRtypes[rt]
}

// geomDepInFlight reports whether t's inline (by-value) layout reaches a still-
// materializing struct placeholder -- through arrays and nested structs, not
// through word-sized pointers/maps/slices/chans.
func geomDepInFlight(t *mtype.Type) bool {
	if materializingCount.Load() == 0 {
		return false
	}
	return geomDepSeen(t, map[*mtype.Type]bool{})
}

func geomDepSeen(t *mtype.Type, seen map[*mtype.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if isMaterializing(t.Rtype) {
		return true
	}
	switch t.Kind() {
	case reflect.Array:
		return geomDepSeen(t.ElemType, seen)
	case reflect.Struct:
		for _, f := range t.Fields {
			if geomDepSeen(f, seen) {
				return true
			}
		}
	}
	return false
}

func markPending(t *mtype.Type) {
	derivedMu.Lock()
	if !pendingFinalize[t] {
		pendingFinalize[t] = true
		pendingCount.Add(1)
	}
	derivedMu.Unlock()
}

func clearPending(t *mtype.Type) {
	derivedMu.Lock()
	if pendingFinalize[t] {
		delete(pendingFinalize, t)
		pendingCount.Add(-1)
	}
	derivedMu.Unlock()
}

func isPending(t *mtype.Type) bool {
	if pendingCount.Load() == 0 {
		return false
	}
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return pendingFinalize[t]
}

// anyByValueStructField reports whether t holds, by value, a struct (a direct
// struct field or array-of-struct elem) for which pred holds -- pred receives the
// struct's own *Type. Only a by-value struct field can place t in a by-value
// pointer cycle, so pointers/slices/maps/chans (word-sized) are skipped.
func anyByValueStructField(t *mtype.Type, pred func(*mtype.Type) bool) bool {
	for _, f := range t.Fields {
		switch f.Kind() {
		case reflect.Struct:
			if pred(f) {
				return true
			}
		case reflect.Array:
			if f.ElemType != nil && f.ElemType.Kind() == reflect.Struct && pred(f.ElemType) {
				return true
			}
		}
	}
	return false
}

// hasByValueStructField reports whether t holds a struct by value. Anon structs
// without one cannot be in a by-value cycle, so they skip the placeholder path.
func hasByValueStructField(t *mtype.Type) bool {
	return anyByValueStructField(t, func(*mtype.Type) bool { return true })
}

// byValueDepInFlight reports whether a by-value struct field of t is still an
// in-flight placeholder. Finalizing t's layout now would bake the placeholder's
// word size in place of the real one, so t must defer.
func byValueDepInFlight(t *mtype.Type) bool {
	return anyByValueStructField(t, func(f *mtype.Type) bool { return isMaterializing(f.Rtype) })
}

// finishStructOrDefer completes a struct whose placeholder ph holds the current
// best-effort layout: if a by-value field is still in-flight, mark t pending so
// FinalizeDeferred re-patches once the cycle settles; otherwise clear the in-flight
// bookkeeping. Returns ph either way (its identity is stable across the re-patch).
func finishStructOrDefer(t *mtype.Type, ph reflect.Type) reflect.Type {
	if byValueDepInFlight(t) || anyFieldPending(t) {
		markPending(t)
		return ph
	}
	clearMaterializing(ph)
	clearPending(t)
	return ph
}

// FinalizeDeferred patches deferred struct layouts and recomputes maps/arrays
// derived over an in-flight placeholder. Structs and arrays settle in one fixpoint
// (a re-patched struct reads its array fields' strides); maps follow.
// Holds materializeMu for the whole pass so it never races a concurrent materialization on a shared rtype.
func FinalizeDeferred() {
	materializeMu.Lock()
	defer materializeMu.Unlock()
	arrays, maps := takePendingGeom()
	for {
		progress := false
		// Arrays first, so a struct re-patch this round reads the corrected stride.
		for _, a := range arrays {
			if runtype.RebuildArrayGeometry(a) {
				progress = true
			}
		}
		derivedMu.Lock()
		pending := make([]*mtype.Type, 0, len(pendingFinalize))
		for t := range pendingFinalize {
			pending = append(pending, t)
		}
		derivedMu.Unlock()
		for _, t := range pending {
			materialize(t)
			if !isPending(t) {
				progress = true
			}
		}
		if !progress {
			// Genuine deadlock (e.g. an illegal infinite by-value cycle gc also
			// rejects): keep each type's best-effort layout but drop the global-map
			// entries so they cannot contaminate a later Eval.
			for _, t := range pending {
				clearMaterializing(t.Rtype)
				clearPending(t)
			}
			break
		}
	}
	for _, m := range maps {
		runtype.RebuildMapGeometry(m)
	}
}

func hasReservableMethods(t *mtype.Type) bool {
	for i := range t.Methods {
		if t.Methods[i].IsResolved() || t.Methods[i].Sig != nil {
			return true
		}
	}
	return false
}

func hasPromotedShapedMethods(t *mtype.Type) bool {
	for _, emb := range t.Embedded {
		ft := embeddedFieldRtype(t, emb)
		if ft == nil {
			continue
		}
		if ft.Kind() == reflect.Interface || (emb.Type != nil && emb.Type.IsInterface()) {
			// A multi-field struct embedding a method-bearing interface gets no
			// reflect.StructOf promotion (BuildStructRtype leaves the field
			// non-Anonymous to avoid a StructOf panic), so its promoted EmbedIface
			// methods are synth-attached and the struct must be reserved. Single-field
			// embeds are StructOf-promoted, but over-reserving is safe.
			if embIfaceHasShapedMethod(emb.Type) {
				return true
			}
			continue
		}
		sets := []reflect.Type{ft}
		if ft.Kind() != reflect.Pointer {
			sets = append(sets, reflect.PointerTo(ft))
		}
		for _, st := range sets {
			for i := 0; i < st.NumMethod(); i++ {
				meth := st.Method(i)
				if !meth.IsExported() {
					continue
				}
				sig := stripRecvType(meth.Type)
				if _, ok := detectShape(sig); ok {
					return true
				}
				if wordShapeAvailable(sig) {
					return true
				}
			}
		}
		// The embed's rtype publishes its methods only after ITS attach; walk
		// the mvm graph too. Over-reserving is safe (the reserve gate is a
		// superset of the attach trigger).
		if embTypeHasMethods(emb.Type) {
			return true
		}
	}
	return false
}

func sigHasSynthShape(sig reflect.Type) bool {
	erased := eraseSynthIfaceParams(sig)
	if _, ok := detectShape(erased); ok {
		return true
	}
	return wordShapeAvailable(erased)
}

func embIfaceHasShapedMethod(e *mtype.Type) bool {
	if e == nil {
		return false
	}
	if e.IsPtr() && e.ElemType != nil {
		e = e.ElemType
	}
	if !e.IsInterface() {
		return false
	}
	if e.Rtype == nil && len(e.IfaceMethods) == 0 {
		// Method set not yet knowable. Reserve conservatively.
		return true
	}
	e.EnsureIfaceMethods()
	for _, im := range e.IfaceMethods {
		sig := im.Rtype
		if sig == nil && im.Sig != nil {
			sig = materialize(im.Sig)
		}
		if sig == nil || sig.Kind() != reflect.Func {
			return true // unknown sig: assume attachable, over-reserve
		}
		if sigHasSynthShape(sig) {
			return true
		}
	}
	return false
}

// embTypeHasMethods reports whether an embedded field's mvm type (deref'd
// through a pointer embed, walking Base, depth-capped against cycles)
// declares any methods.
func embTypeHasMethods(e *mtype.Type) bool {
	if e != nil && e.IsPtr() && e.ElemType != nil {
		e = e.ElemType
	}
	for i := 0; e != nil && i < canonicalTypeMaxDepth; i, e = i+1, e.Base {
		if len(e.Methods) > 0 {
			return true
		}
	}
	return false
}

func embeddedFieldRtype(t *mtype.Type, emb mtype.EmbeddedField) reflect.Type {
	if emb.FieldIdx >= 0 && emb.FieldIdx < len(t.Fields) {
		if f := t.Fields[emb.FieldIdx]; f != nil && f.Rtype != nil {
			return f.Rtype
		}
	}
	if emb.Type != nil {
		return emb.Type.Rtype
	}
	return nil
}

func hasSynthTableMethods(t *mtype.Type) bool {
	for _, method := range t.Methods {
		sig := method.Rtype
		if sig == nil && method.Sig != nil {
			sig = materialize(method.Sig)
		}
		if sig == nil {
			return true // unknown sig: assume table method, don't share
		}
		if sigHasSynthShape(sig) {
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

func isFieldClone(t *mtype.Type) bool {
	if t.Defined {
		return false // a top-level `type X T` definition owns its identity
	}
	if t.Base == nil || t.Base.Name == "" || t.Base.Kind() != t.Kind() {
		return false
	}
	// Method-bearing or pkg-scoped named base: the clone resolves to Base's
	// identity rather than minting one under its own (possibly field-derived) name.
	return len(t.Base.Methods) > 0 || t.Base.PkgPath != ""
}

func maybeReserve(t *mtype.Type, layoutRT reflect.Type) reflect.Type {
	if layoutRT == nil || t.Name == "" ||
		layoutRT.Kind() == reflect.Struct || !runtype.SupportedKind(layoutRT.Kind()) {
		return layoutRT
	}
	if lookupReservation(t) != nil {
		return t.Rtype
	}
	if isFieldClone(t) {
		return materialize(t.Base)
	}
	if !hasReservableMethods(t) {
		// Only a definition (`type X T`) owns a named identity; placeholders
		// (generic type params) and parser-derived clones keep the native layout.
		if !t.Defined || t.PkgPath == "" {
			return layoutRT
		}
		return propagateGeom(layoutRT, reserveNamedCarrier(t, layoutRT))
	}
	if !t.Defined && t.Base != nil {
		// A parser clone of an unnamed method-carrying base (a `f *T` struct
		// field clones *T with the methods copied, Base.Name == "" so the
		// isFieldClone gate misses): the field name is not an identity.
		return layoutRT
	}
	return propagateGeom(layoutRT, reserveValueAndPtr(t, layoutRT))
}

// reserveNamedCarrier gives a methodless defined non-struct type its own named
// rtype, so reflect-visible identity (%T, %#v, DeepEqual) matches gc.
func reserveNamedCarrier(t *mtype.Type, layoutRT reflect.Type) reflect.Type {
	name := qualifiedTypeName(t)
	key := sharedStructKey{name: name, layoutSig: layoutRT.String()}
	derivedMu.Lock()
	defer derivedMu.Unlock()
	res := sharedCarriers[key]
	if res == nil {
		vr, err := runtype.ReserveMethods(layoutRT, name, t.PkgPath)
		if err != nil {
			return layoutRT
		}
		// Carriers never fill methods; see ClearUncommon for the StructOf constraint.
		runtype.ClearUncommon(vr.Type())
		res = &synthReservation{value: vr}
		sharedCarriers[key] = res
	}
	reservations[t] = res
	return res.value.Type()
}

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
		rt := materialize(t.Base)
		t.Rtype = rt
		return rt, true
	}
	res := lookupReservation(t)
	if res == nil {
		if !hasReservableMethods(t) && !hasPromotedShapedMethods(t) {
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
	markMaterializing(reserved)
	for _, f := range t.Fields {
		materialize(f)
	}
	if !fieldsMaterialized(t.Fields) {
		clearMaterializing(reserved)
		t.Rtype = nil // a field references a not-yet-finalized sibling; retry later
		return nil, true
	}
	// Non-memoized build so a deferred re-fill (below) picks up the real size once
	// an in-flight embedded field is patched, rather than a stale memoized layout.
	realLayout := mtype.BuildStructRtype(t.Fields, t.Embedded, t.Tags)
	if nativeLayoutRegistered(t) {
		// Keep interface fields as iface so the value matches its native rtype when
		// stored into native code (e.g. log.Logger -> http.Server.ErrorLog).
		realLayout = mtype.BuildStructRtypeKeepIface(t.Fields, t.Embedded, t.Tags)
	}
	if byValueDepInFlight(t) || anyFieldPending(t) {
		// An embedded by-value struct is still a word-sized placeholder, so realLayout's
		// size is provisional; or a func field's interface IO erased before its sigs were
		// ready. Fill the reserved rtype to this best-effort shape (so compile-time walks
		// see a real struct) and defer the share-publish + final fill to FinalizeDeferred,
		// which re-enters once the cycle settles / the IO synths.
		runtype.FillStructLayout(reserved, realLayout)
		markPending(t)
		t.Rtype = reserved
		return reserved, true
	}
	clearPending(t)
	// Methodless-table identity is safe to share across Evals (see sharedStructs).
	if !hasSynthTableMethods(t) {
		key := sharedStructKey{name: qualifiedTypeName(t), layoutSig: realLayout.String()}
		derivedMu.Lock()
		if shared := sharedStructs[key]; shared != nil {
			sharedRT := shared.value.Type()
			reservations[t] = shared
			derivedMu.Unlock()
			clearMaterializing(reserved)
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
		clearMaterializing(reserved)
		t.Rtype = reserved
		return reserved, true
	}
	// Method-bearing: share per Machine across re-Evals. See sharedMethodStructs.
	if mach := ActiveMachine(); mach != nil {
		key := methodStructKey{
			machine:   mach,
			name:      qualifiedTypeName(t),
			layoutSig: realLayout.String(),
			methodSig: methodFingerprint(t),
		}
		derivedMu.Lock()
		if shared := sharedMethodStructs[key]; shared != nil {
			sharedRT := shared.value.Type()
			reservations[t] = shared
			derivedMu.Unlock()
			clearMaterializing(reserved)
			t.Rtype = sharedRT
			if shared.ptr != nil {
				AttachPtrDerived(t, shared.ptr.Type())
			}
			return sharedRT, true
		}
		runtype.FillStructLayout(reserved, realLayout)
		sharedMethodStructs[key] = res
		derivedMu.Unlock()
		clearMaterializing(reserved)
		t.Rtype = reserved
		return reserved, true
	}
	runtype.FillStructLayout(reserved, realLayout)
	clearMaterializing(reserved)
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
	// derivedMu serializes the derived/synthIface side tables.
	// Contended only when parallel tests share a std *Type.
	// Uncontended within one single-threaded Compiler.
	derivedMu       sync.Mutex
	derivedCache    = map[*mtype.Type]*derivedTypes{}
	synthIfaceCache = map[*mtype.Type]reflect.Type{}
	// synthIfaceNamed dedupes named synth ifaces across clone *Types: clones
	// must share one InterfaceOf rtype or rtype-identity compares split
	// (goldmark ast.Node.SortChildren).
	synthIfaceNamed = map[string]reflect.Type{}
)

var materializeMu sync.Mutex // serializes the whole materialization pass

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
		// Defined over an imported basic whose kind never resolved: recover the layout from Base.
		return true
	}
	return false
}

// selfRefFuncPlaceholder stands in for the self-referencing positions of a
// self-referential func type; a func value is word-sized, so it's layout-correct.
var selfRefFuncPlaceholder = reflect.FuncOf(nil, nil, false)

// MaterializeRtype is the public entry to materialization. It serializes the
// pass under materializeMu (see that var) so concurrent compilations don't race
// on shared reserved rtypes, then delegates to the recursive core. The core and
// every helper it reaches call materialize directly, never this wrapper, so the
// non-reentrant lock is taken exactly once per top-level request.
func MaterializeRtype(t *mtype.Type) reflect.Type {
	materializeMu.Lock()
	defer materializeMu.Unlock()
	return materialize(t)
}

// materialize builds and caches t.Rtype from t's symbolic graph (Kind +
// ElemType/KeyType/Fields/Params/Returns + ArrayLen/ChanDir/Variadic/Tags) when
// it is not already set, recursing into children first.
// This is the comp-side materialization that lets goparser build a *Type without an rtype.
// Callers must hold materializeMu (via MaterializeRtype or FinalizeDeferred).
//
// A named leaf (a primitive or struct that carries methods) must already hold
// its rtype so an un-materialized leaf here yields nil.
func materialize(t *mtype.Type) reflect.Type {
	if t == nil {
		return nil
	}
	if t.Rtype != nil && !isPending(t) {
		return t.Rtype
	}
	if t.Placeholder {
		return nil // forward-declared struct/interface not yet finalized
	}
	// No own structure: materialize from the underlying. A method-bearing
	// defined-over-basic type (e.g. `type Confidence int` with methods) reserves
	// its identity over the base layout so attach fills methods in place.
	if definedOverBase(t) {
		if base := materialize(t.Base); base != nil {
			rt := reserveDefinedOverBase(t, base)
			t.Rtype = rt
			return rt
		}
	}
	if rt, handled := materializeSelfRef(t); handled {
		return rt
	}
	var rt reflect.Type
	switch t.Kind() {
	case reflect.Pointer:
		elem := materialize(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DerivePointerTo(elem)
	case reflect.Slice:
		elem := materialize(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveSliceOf(elem)
	case reflect.Array:
		elem := materialize(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveArrayOf(t.ArrayLen, elem)
		if geomDepInFlight(t.ElemType) { // elem grows later; recompute stride in FinalizeDeferred
			markPendingGeom(rt)
		}
	case reflect.Chan:
		elem := materialize(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveChanOf(t.ChanDir, elem)
	case reflect.Map:
		key, elem := materialize(t.KeyType), materialize(t.ElemType)
		if key == nil || elem == nil {
			return nil
		}
		rt = runtype.DeriveMapOf(key, elem)
		if geomDepInFlight(t.KeyType) || geomDepInFlight(t.ElemType) { // key/elem grows later
			markPendingGeom(rt)
		}
	case reflect.Func:
		if t.Name != "" {
			// A named func type may be self-referential (type parseFn func() parseFn),
			// which reflect.FuncOf can't build. Pre-set a placeholder a self-reference
			// in Params/Returns resolves to; when t isn't self-referential nothing
			// reads it and the real signature below overwrites it unobserved.
			t.Rtype = selfRefFuncPlaceholder
		}
		in := make([]reflect.Type, len(t.Params))
		for i, p := range t.Params {
			if in[i] = materializeFuncIO(p); in[i] == nil {
				t.Rtype = nil
				return nil
			}
		}
		out := make([]reflect.Type, len(t.Returns))
		for i, r := range t.Returns {
			if out[i] = materializeFuncIO(r); out[i] == nil {
				t.Rtype = nil
				return nil
			}
		}
		rt = reflect.FuncOf(in, out, t.Variadic)
		// An interface IO that erased only because its method sigs were not yet
		// materialized will synth precisely later; keep t pending so FinalizeDeferred
		// rebuilds it (and any struct field holding it) instead of caching erased.
		if funcIODeferrable(t) {
			markPending(t)
		} else {
			clearPending(t)
		}
	case reflect.Struct:
		if len(t.Fields) == 0 && t.Base != nil && t.Base != t && t.Base.Kind() == reflect.Struct {
			// Defined type (type T1 T) whose Fields were cloned empty before the
			// underlying was finalized: materialize from the underlying's layout.
			rt = materialize(t.Base)
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
			// resolves to it), then patch the placeholder in place. A pending re-entry
			// (deferred finalize, below) reuses the same placeholder to keep identity.
			ph := t.Rtype
			if ph == nil {
				ph = mtype.NewPlaceholderRtype(t.Name)
			}
			t.Rtype = ph
			markMaterializing(ph)
			// Stamp the real name before materializing fields so a self-referential
			// field (S *S, []*S, map[K]*S, ...) bakes the correct name into its
			// derived rtype: reflect.PointerTo/SliceOf/MapOf snapshot the element's
			// String() at derivation time, so naming ph afterward would leave the
			// derived types reading the placeholder name (*struct{...}).
			// Method-bearing types get their name from attach instead.
			named := len(t.Methods) == 0
			if named {
				runtype.StampName(ph, qualifiedTypeName(t))
			}
			for _, f := range t.Fields {
				materialize(f)
			}
			if !fieldsMaterialized(t.Fields) {
				// A field references a not-yet-finalized placeholder (e.g. *T sibling):
				// reset Rtype so a later call retries once it is finalized.
				clearMaterializing(ph)
				t.Rtype = nil
				return nil
			}
			// Patch ph to the current best-effort layout (struct-shaped with the real
			// field types) so compile-time walks -- e.g. embedded-method resolution --
			// see a valid struct. If an embedded by-value struct is still an in-flight
			// placeholder its size is wrong here; defer to FinalizeDeferred, which
			// re-patches once the cycle settles. Use the non-memoized builder so the
			// re-patch is not served the stale first layout.
			mtype.PatchRtype(ph, mtype.BuildStructRtype(t.Fields, t.Embedded, t.Tags))
			// PatchRtype copies the real layout's TFlag (clearing tflagNamed) but
			// preserves ph's Str; re-stamp to restore the named flag in place.
			if named {
				runtype.StampName(ph, qualifiedTypeName(t))
			}
			return finishStructOrDefer(t, ph)
		}
		// Anonymous struct. Only a by-value struct/array field can make an anon struct
		// part of a by-value pointer cycle; without one, the direct build is correct
		// and preserves reflect's embedded-interface method promotion. With one, use
		// the cycle-safe path: reserve a shared placeholder under the structural key
		// (identical anon shapes converge on one rtype), materialize fields, then patch
		// -- deferring if a by-value field is still in-flight. A pending re-entry
		// (FinalizeDeferred) skips the reserve/field steps and falls through to patch.
		if !hasByValueStructField(t) {
			for _, f := range t.Fields {
				materialize(f)
			}
			if !fieldsMaterialized(t.Fields) {
				return nil
			}
			rt = mtype.StructOf(t.Fields, t.Embedded, t.Tags).Rtype
			break
		}
		if !isPending(t) {
			resv, fresh := mtype.ReserveStruct(t.Fields, t.Embedded, t.Tags)
			t.Rtype = resv.Rtype
			if !fresh {
				return resv.Rtype // a sibling reserved this shape; share its placeholder
			}
			markMaterializing(resv.Rtype)
			for _, f := range t.Fields {
				materialize(f)
			}
			if !fieldsMaterialized(t.Fields) {
				clearMaterializing(resv.Rtype)
				mtype.UnreserveStruct(t.Fields, t.Embedded, t.Tags)
				t.Rtype = nil
				return nil
			}
		}
		ph := t.Rtype
		// Patch ph to the current best-effort layout so compile-time walks see a real
		// struct shape; re-patch in FinalizeDeferred if an embedded field is in-flight.
		layout := mtype.FinalizeStruct(t)
		runtype.StampName(ph, layout.String()) // PatchRtype keeps ph's Str; restore the anon shape
		return finishStructOrDefer(t, ph)
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

// compositeReachesSelf reports whether x's elem/key graph reaches target
// without crossing a struct, interface, or func (those break their own cycles).
func compositeReachesSelf(x, target *mtype.Type, seen map[*mtype.Type]bool) bool {
	for x != nil {
		if x == target {
			return true
		}
		if seen[x] {
			return false
		}
		seen[x] = true
		switch x.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			x = x.ElemType
		case reflect.Map:
			if compositeReachesSelf(x.KeyType, target, seen) {
				return true
			}
			x = x.ElemType
		default:
			return false
		}
	}
	return false
}

// materializeSelfRef materializes a named composite whose elem graph reaches t
// (type P *P / S []S / M map[int]M), which neither elem-first recursion nor reflect can build.
// It reserves t's identity over an elem-independent donor layout, sets t.Rtype so the self-reference resolves to it, then patches the reserved rtype's Elem in place.
// handled=true means do not fall through; rt may still be nil for unsupported shapes.
func materializeSelfRef(t *mtype.Type) (rt reflect.Type, handled bool) {
	if t.Name == "" || t.ElemType == nil {
		return nil, false
	}
	k := t.Kind()
	switch k {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Chan:
	default:
		return nil, false
	}
	if !compositeReachesSelf(t.ElemType, t, map[*mtype.Type]bool{}) {
		if k != reflect.Map {
			return nil, false
		}
		if !compositeReachesSelf(t.KeyType, t, map[*mtype.Type]bool{}) {
			return nil, false
		}
		return nil, true // self-referential map key: not materializable
	}
	var donor reflect.Type
	switch k {
	case reflect.Pointer:
		donor = reflect.TypeFor[*int]()
	case reflect.Slice:
		donor = reflect.TypeFor[[]int]()
	case reflect.Chan:
		donor = reflect.ChanOf(t.ChanDir, reflect.TypeFor[int]())
	case reflect.Map:
		if compositeReachesSelf(t.KeyType, t, map[*mtype.Type]bool{}) {
			return nil, true // self-referential map key: not materializable
		}
		key := materialize(t.KeyType)
		standIn := selfRefStandIn(t.ElemType)
		if key == nil || standIn == nil {
			return nil, true
		}
		donor = runtype.MapOf(key, standIn)
	}
	rt = reserveSelfRef(t, donor)
	if rt == nil {
		return nil, true
	}
	t.Rtype = rt // the self-reference below resolves to the reserved identity
	elem := materialize(t.ElemType)
	if elem == nil {
		t.Rtype = nil
		return nil, true
	}
	runtype.SetElem(rt, elem)
	return rt, true
}

// reserveSelfRef reserves t's named identity over the donor layout, never via
// the shared-carrier cache: the donor erases the real layout, so same-named
// self-ref types would collide and the second SetElem would corrupt the first.
// The reservations entry makes a deferred retry reuse the same identity.
func reserveSelfRef(t *mtype.Type, donor reflect.Type) reflect.Type {
	if res := lookupReservation(t); res != nil {
		return res.value.Type()
	}
	if hasReservableMethods(t) {
		return reserveValueAndPtr(t, donor)
	}
	vr, err := runtype.ReserveMethods(donor, qualifiedTypeName(t), t.PkgPath)
	if err != nil {
		return nil
	}
	// Carriers never fill methods; see ClearUncommon for the StructOf constraint.
	runtype.ClearUncommon(vr.Type())
	derivedMu.Lock()
	reservations[t] = &synthReservation{value: vr}
	derivedMu.Unlock()
	return vr.Type()
}

// selfRefStandIn returns a native type with the same size and pointer shape
// as et, usable as the donor map-elem while the real elem is in a cycle.
func selfRefStandIn(et *mtype.Type) reflect.Type {
	switch et.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return reflect.TypeFor[*int]()
	case reflect.Slice:
		return reflect.TypeFor[[]int]()
	case reflect.Array:
		// type M map[int][2]M: same-shape array of stand-ins.
		if e := selfRefStandIn(et.ElemType); e != nil {
			return reflect.ArrayOf(et.ArrayLen, e)
		}
	}
	return nil
}

// materializeFuncIO materializes a func param/return type.
// A named interpreted interface yields its synth rtype, so the func signature exposes the method set to reflect (go-cmp probes In(0).NumMethod()); the interface itself still materializes to any for value storage.
func materializeFuncIO(p *mtype.Type) reflect.Type {
	// Native-bridged interfaces (error, io.Writer) keep their rtype.
	// Unexported methods never enter synth tables, so such an interface could never build an itab; keep it as any.
	if p != nil && p.Name != "" && p.Kind() == reflect.Interface && len(p.IfaceMethods) > 0 &&
		(p.Rtype == nil || p.Rtype == mtype.AnyRtype) && allIfaceMethodsExported(p) {
		if rt := synthIfaceRtype(p); rt != nil {
			return rt
		}
	}
	return materialize(p)
}

// ifaceSynthDeferrable reports whether p is a named, all-exported, method-bearing
// interface whose synth rtype cannot be built yet because a method signature is
// still unmaterialized (im.Rtype nil).
// materializeFuncIO erases such an IO to interface{} for now, but it will synth
// precisely once the sigs fill, so a func type carrying it must not be cached final.
func ifaceSynthDeferrable(p *mtype.Type) bool {
	if p == nil || p.Name == "" || p.Kind() != reflect.Interface ||
		len(p.IfaceMethods) == 0 || !allIfaceMethodsExported(p) {
		return false
	}
	if p.Rtype != nil && p.Rtype != mtype.AnyRtype {
		return false // already a concrete iface rtype (native-bridged or built)
	}
	for _, im := range p.IfaceMethods {
		if im.Rtype == nil {
			return true
		}
	}
	return false
}

// funcIODeferrable reports whether func type t has any param/result that is a
// synth-deferrable interface (see ifaceSynthDeferrable).
func funcIODeferrable(t *mtype.Type) bool {
	return slices.ContainsFunc(t.Params, ifaceSynthDeferrable) ||
		slices.ContainsFunc(t.Returns, ifaceSynthDeferrable)
}

// anyFieldPending reports whether any direct field of t is pending re-materialization
// (e.g. a func field whose interface IO was erased before its sigs were ready).
// Such a struct must be re-patched by FinalizeDeferred once the field rebuilds precisely.
func anyFieldPending(t *mtype.Type) bool {
	if pendingCount.Load() == 0 {
		return false
	}
	return slices.ContainsFunc(t.Fields, isPending)
}

func allIfaceMethodsExported(p *mtype.Type) bool {
	for _, im := range p.IfaceMethods {
		if !isExportedName(im.Name) {
			return false
		}
	}
	return true
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
	reflect.Bool:          reflect.TypeFor[bool](),
	reflect.Int:           reflect.TypeFor[int](),
	reflect.Int8:          reflect.TypeFor[int8](),
	reflect.Int16:         reflect.TypeFor[int16](),
	reflect.Int32:         reflect.TypeFor[int32](),
	reflect.Int64:         reflect.TypeFor[int64](),
	reflect.Uint:          reflect.TypeFor[uint](),
	reflect.Uint8:         reflect.TypeFor[uint8](),
	reflect.Uint16:        reflect.TypeFor[uint16](),
	reflect.Uint32:        reflect.TypeFor[uint32](),
	reflect.Uint64:        reflect.TypeFor[uint64](),
	reflect.Uintptr:       reflect.TypeFor[uintptr](),
	reflect.Float32:       reflect.TypeFor[float32](),
	reflect.Float64:       reflect.TypeFor[float64](),
	reflect.Complex64:     reflect.TypeFor[complex64](),
	reflect.Complex128:    reflect.TypeFor[complex128](),
	reflect.String:        reflect.TypeFor[string](),
	reflect.UnsafePointer: reflect.TypeFor[unsafe.Pointer](),
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
// bridge stays and a later call retries). A named interface also dedupes by
// synthIfaceNameKey so clone *Types share one rtype identity.
func cachedSynthIface(t *mtype.Type, build func() reflect.Type) reflect.Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	if st := synthIfaceCache[t]; st != nil {
		return st
	}
	nameKey := synthIfaceNameKey(t)
	if nameKey != "" {
		if st := synthIfaceNamed[nameKey]; st != nil {
			synthIfaceCache[t] = st
			return st
		}
	}
	if st := build(); st != nil {
		synthIfaceCache[t] = st
		if nameKey != "" {
			synthIfaceNamed[nameKey] = st
		}
		return st
	}
	return nil
}

// synthIfaceNameKey fingerprints a named interface for cross-clone dedupe:
// package, name, and sorted method name:signature pairs, so a same-named
// interface with different sigs never adopts the other's rtype. Returns ""
// (no dedupe) for unnamed interfaces or while any method sig is unmaterialized.
func synthIfaceNameKey(t *mtype.Type) string {
	if t == nil || t.Name == "" {
		return ""
	}
	names := make([]string, 0, len(t.IfaceMethods))
	for _, im := range t.IfaceMethods {
		if im.Rtype == nil {
			return ""
		}
		names = append(names, im.Name+":"+im.Rtype.String())
	}
	sort.Strings(names)
	return t.PkgPath + "." + t.Name + "{" + strings.Join(names, ",") + "}"
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
