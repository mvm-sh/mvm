// Package derive builds reflect.Types from mvm's symbolic mtype.Type graph:
// memoized derived-type construction, materialization via runtype, the
// synth-rtype reserve/fill gate, and the synth-interface rtype cache.
// It sits above mtype and runtype, below vm.
package derive

import (
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/mvm-sh/mvm/internal/runtype"
	"github.com/mvm-sh/mvm/mtype"
)

// SynthReservation holds a named type's reserved value (method-set T) and
// pointer (method-set *T) rtypes, awaiting Fill at attach.
type SynthReservation struct {
	value *runtype.Reservation // method-set T
	ptr   *runtype.Reservation // method-set *T
}

// Value returns the method-set-T (value receiver) reservation.
func (r *SynthReservation) Value() *runtype.Reservation { return r.value }

// Ptr returns the method-set-*T (pointer receiver) reservation.
func (r *SynthReservation) Ptr() *runtype.Reservation { return r.ptr }

var reservations = map[*mtype.Type]*SynthReservation{} // guarded by derivedMu

// sharedStructs shares one reserved rtype per (name, layout) across Evals so a
// process-global registry keyed by reflect.Type (gob's nameToConcreteType) sees a
// single rtype per name.
type sharedStructKey struct {
	name      string
	layoutSig string
}

var sharedStructs = map[sharedStructKey]*SynthReservation{} // guarded by derivedMu

var sharedCarriers = map[sharedStructKey]*SynthReservation{} // guarded by derivedMu

// sharedPlainStructs converges methodless named struct rtypes, whose placeholder build mints a fresh identity per compile (see shareOrAdoptPlain).
type plainStructKey struct {
	name      string
	pkgPath   string
	layoutSig string
}

var sharedPlainStructs = map[plainStructKey]reflect.Type{} // guarded by derivedMu

// MethodStructKey keys the per-execution rtype cache (ActiveRtypeCache), deduping
// a method-bearing struct's rtype across re-Evals so reflect.Type identity holds.
// Per-execution, not global: the stub captures the Machine, so a global would pin it.
type MethodStructKey struct {
	name      string
	layoutSig string
	methodSig string
}

func methodFingerprint(t *mtype.Type) string {
	var b strings.Builder
	for i := range t.Methods {
		if t.Methods[i].IsResolved() || t.Methods[i].Sig != nil {
			fmt.Fprintf(&b, "%d,", i)
		}
	}
	return b.String()
}

// flagSet is a keyed in-flight/deferred marker: a mutex-guarded set with an
// atomic size mirror so the common empty case is a lock-free check.
type flagSet[K comparable] struct {
	mu sync.Mutex
	m  map[K]bool
	n  atomic.Int64
}

func (s *flagSet[K]) empty() bool { return s.n.Load() == 0 }

func (s *flagSet[K]) mark(k K) {
	var zero K
	if k == zero {
		return
	}
	s.mu.Lock()
	if s.m == nil {
		s.m = map[K]bool{}
	}
	if !s.m[k] {
		s.m[k] = true
		s.n.Add(1)
	}
	s.mu.Unlock()
}

func (s *flagSet[K]) clear(k K) {
	s.mu.Lock()
	if s.m[k] {
		delete(s.m, k)
		s.n.Add(-1)
	}
	s.mu.Unlock()
}

func (s *flagSet[K]) has(k K) bool {
	if s.empty() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k]
}

// trySet marks k and reports true, or false if already marked.
func (s *flagSet[K]) trySet(k K) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[k] {
		return false
	}
	if s.m == nil {
		s.m = map[K]bool{}
	}
	s.m[k] = true
	s.n.Add(1)
	return true
}

// keys snapshots the set without draining it.
func (s *flagSet[K]) keys() []K {
	if s.empty() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]K, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	return keys
}

// take drains the set and returns its keys.
func (s *flagSet[K]) take() []K {
	if s.empty() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]K, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	clear(s.m)
	s.n.Store(0)
	return keys
}

// Markers scoped to a materialization pass (all under materializeMu).
var (
	// materializing: struct rtypes whose placeholder is installed but layout not final.
	materializing flagSet[reflect.Type]
	// reserving: structs whose reserve pass is on the stack (by-value cycle re-entry guard).
	reserving flagSet[*mtype.Type]
	// pendingFinalize: types that installed a best-effort layout and await a
	// FinalizeDeferred re-patch (a by-value field was still in-flight).
	pendingFinalize flagSet[*mtype.Type]
	// pendingGeom: map/array rtypes derived over an in-flight placeholder key or
	// elem; RebuildMap/ArrayGeometry fixes their stride once it is final.
	pendingGeom flagSet[reflect.Type]
	// materializingFunc: named func types mid-signature (self-ref placeholder live).
	materializingFunc flagSet[*mtype.Type]
	// funcElemDeferred: named containers whose func elem/key signature was deferred.
	funcElemDeferred flagSet[*mtype.Type]
)

func propagateGeom(layoutRT, carrier reflect.Type) reflect.Type {
	if k := layoutRT.Kind(); k != reflect.Map && k != reflect.Array {
		return carrier
	}
	if carrier != layoutRT && pendingGeom.has(layoutRT) {
		pendingGeom.mark(carrier)
	}
	return carrier
}

func enterReserving(t *mtype.Type) (release func(), reentrant bool) {
	if !reserving.trySet(t) {
		return nil, true
	}
	return func() { reserving.clear(t) }, false
}

func geomDepInFlight(t *mtype.Type) bool {
	if materializing.empty() {
		return false
	}
	return geomDepSeen(t, map[*mtype.Type]bool{})
}

func geomDepSeen(t *mtype.Type, seen map[*mtype.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if materializing.has(t.Rtype) {
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

func hasByValueStructField(t *mtype.Type) bool {
	return anyByValueStructField(t, func(*mtype.Type) bool { return true })
}

func byValueDepInFlight(t *mtype.Type) bool {
	return anyByValueStructField(t, func(f *mtype.Type) bool { return materializing.has(f.Rtype) })
}

// structDeferred reports a field dependency forcing t's final layout to wait for
// FinalizeDeferred: an embedded by-value struct still word-sized, a pending
// field, a func field mid-materialization (signature erased), or a named-iface
// field whose synth rtype is not buildable yet.
func structDeferred(t *mtype.Type) bool {
	return byValueDepInFlight(t) || anyFieldPending(t) || anyFuncFieldInFlight(t) || synthIfaceFieldDeferred(t)
}

func finishStructOrDefer(t *mtype.Type, ph reflect.Type) reflect.Type {
	if structDeferred(t) {
		pendingFinalize.mark(t)
		return ph
	}
	materializing.clear(ph)
	pendingFinalize.clear(t)
	return ph
}

// FinalizeDeferred patches deferred struct layouts and recomputes maps/arrays
// derived over an in-flight placeholder.
func FinalizeDeferred() {
	materializeMu.Lock()
	defer materializeMu.Unlock()
	var arrays, maps []reflect.Type
	for _, rt := range pendingGeom.take() {
		switch rt.Kind() {
		case reflect.Array:
			arrays = append(arrays, rt)
		case reflect.Map:
			maps = append(maps, rt)
		}
	}
	for {
		progress := false
		// Arrays first, so a struct re-patch this round reads the corrected stride.
		for _, a := range arrays {
			if runtype.RebuildArrayGeometry(a) {
				progress = true
			}
		}
		pending := pendingFinalize.keys()
		for _, t := range pending {
			materialize(t)
			if !pendingFinalize.has(t) {
				progress = true
			}
		}
		if !progress {
			// Genuine deadlock (e.g. an illegal infinite by-value cycle gc also
			// rejects): keep each type's best-effort layout but drop the global-map
			// entries so they cannot contaminate a later Eval.
			for _, t := range pending {
				materializing.clear(t.Rtype)
				pendingFinalize.clear(t)
				funcElemDeferred.clear(t)
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
		if (ft != nil && ft.Kind() == reflect.Interface) || (emb.Type != nil && emb.Type.IsInterface()) {
			// A struct embedding a method-bearing interface may not get usable
			// reflect.StructOf promotion, so its promoted EmbedIface methods are
			// synth-attached and the struct must be reserved.
			if embIfaceHasShapedMethod(emb.Type) {
				return true
			}
			continue
		}
		if ft != nil {
			sets := []reflect.Type{ft}
			if ft.Kind() != reflect.Pointer {
				sets = append(sets, reflect.PointerTo(ft))
			}
			for _, st := range sets {
				for meth := range st.Methods() {
					if !meth.IsExported() {
						continue
					}
					sig := StripRecvType(meth.Type)
					if ShapeAvailable(sig) {
						return true
					}
				}
			}
		}
		if embTypeHasMethods(emb.Type) {
			return true
		}
	}
	return false
}

func sigHasSynthShape(sig reflect.Type) bool {
	es := EraseSynthIfaceParams(sig)
	if IfaceShapeLog != nil {
		IfaceShapeLog(es)
	}
	return ShapeAvailable(es)
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

func embTypeHasMethods(e *mtype.Type) bool { return embTypeHasMethodsAt(e, 0) }

// embTypeHasMethodsAt also sees native rtype methods and nested embeds, so a type reached only through two embedding levels (mmapper -> sync.Mutex) still gets a reservation before attach.
// The cap bounds pointer-embed cycles.
func embTypeHasMethodsAt(e *mtype.Type, depth int) bool {
	if depth >= maxEmbedWalkDepth {
		return false
	}
	if e != nil && e.IsPtr() && e.ElemType != nil {
		e = e.ElemType
	}
	for i := 0; e != nil && i < canonicalTypeMaxDepth; i, e = i+1, e.Base {
		if len(e.Methods) > 0 {
			return true
		}
		if e.Rtype != nil && (e.Rtype.NumMethod() > 0 || reflect.PointerTo(e.Rtype).NumMethod() > 0) {
			return true
		}
		for _, emb := range e.Embedded {
			if embTypeHasMethodsAt(emb.Type, depth+1) {
				return true
			}
		}
	}
	return false
}

const maxEmbedWalkDepth = 64

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

// LookupReservation returns t's reservation, or nil.
func LookupReservation(t *mtype.Type) *SynthReservation {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	return reservations[t]
}

// SetValueReservation seeds t's method-set-T reservation directly.
func SetValueReservation(t *mtype.Type, value *runtype.Reservation) {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	reservations[t] = &SynthReservation{value: value}
}

// DeleteReservation drops t's reservation.
func DeleteReservation(t *mtype.Type) {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	delete(reservations, t)
}

// TypeForReservedRtype recovers the *Type whose reserved synth rtype is rt.
func TypeForReservedRtype(rt reflect.Type) *mtype.Type {
	if rt == nil {
		return nil
	}
	derivedMu.Lock()
	defer derivedMu.Unlock()
	for t, res := range reservations {
		if res.value != nil && res.value.Type() == rt {
			return t
		}
		if res.ptr != nil && res.ptr.Type() == rt {
			if d := derivedCache[t]; d != nil && d.ptr != nil {
				return d.ptr
			}
		}
	}
	return nil
}

func isFieldClone(t *mtype.Type) bool {
	if t.Defined {
		return false // a top-level `type X T` definition owns its identity
	}
	if t.Base == nil || t.Base.Name == "" || t.Base.Kind() != t.Kind() {
		return false
	}
	return len(t.Base.Methods) > 0 || t.Base.PkgName != ""
}

func maybeReserve(t *mtype.Type, layoutRT reflect.Type) reflect.Type {
	if layoutRT == nil || t.Name == "" ||
		layoutRT.Kind() == reflect.Struct || !runtype.SupportedKind(layoutRT.Kind()) {
		return layoutRT
	}
	if LookupReservation(t) != nil {
		return t.Rtype
	}
	if isFieldClone(t) {
		if materializingFunc.has(t.Base) {
			pendingFinalize.mark(t)
			return layoutRT
		}
		return materialize(t.Base)
	}
	if !hasReservableMethods(t) {
		if !t.Defined || t.PkgName == "" {
			return layoutRT
		}
		return propagateGeom(layoutRT, reserveNamedCarrier(t, layoutRT))
	}
	if !t.Defined && t.Base != nil {
		return layoutRT
	}
	return propagateGeom(layoutRT, reserveValueAndPtr(t, layoutRT))
}

// newReservation reserves t's value and pointer method sets over layoutRT and
// records the derived pointer rtype. Nil if the value reservation fails.
// The caller publishes it in reservations.
func newReservation(t *mtype.Type, layoutRT reflect.Type) *SynthReservation {
	name := QualifiedTypeName(t)
	pkgPath := RtypePkgPath(t)
	vr, err := runtype.ReserveMethods(layoutRT, name, pkgPath)
	if err != nil {
		return nil
	}
	res := &SynthReservation{value: vr}
	if pr, err := runtype.ReservePtrMethods(vr.Type(), "*"+name, pkgPath); err == nil {
		res.ptr = pr
		AttachPtrDerived(t, pr.Type())
	}
	return res
}

// newCarrier reserves a methodless named identity for t over donor.
func newCarrier(t *mtype.Type, donor reflect.Type) *SynthReservation {
	vr, err := runtype.ReserveMethods(donor, QualifiedTypeName(t), RtypePkgPath(t))
	if err != nil {
		return nil
	}
	// Carriers never fill methods; keep the uncommon (it carries PkgPath) unless embedding it would trip StructOf.
	if runtype.EmbedTripsStructOf(vr.Type()) {
		runtype.ClearUncommon(vr.Type())
	}
	return &SynthReservation{value: vr}
}

// adoptShared points t at an equivalent reservation published by a clone.
func adoptShared(t *mtype.Type, shared *SynthReservation) reflect.Type {
	t.Rtype = shared.value.Type()
	if shared.ptr != nil {
		AttachPtrDerived(t, shared.ptr.Type())
	}
	return t.Rtype
}

func reserveNamedCarrier(t *mtype.Type, layoutRT reflect.Type) reflect.Type {
	key := sharedStructKey{name: QualifiedTypeName(t), layoutSig: layoutRT.String()}
	derivedMu.Lock()
	defer derivedMu.Unlock()
	res := sharedCarriers[key]
	if res == nil {
		if res = newCarrier(t, layoutRT); res == nil {
			return layoutRT
		}
		sharedCarriers[key] = res
	}
	reservations[t] = res
	return res.value.Type()
}

func reserveValueAndPtr(t *mtype.Type, layoutRT reflect.Type) reflect.Type {
	// wasm: dedup a method-bearing named non-struct (token.Pos) to one rtype, so a
	// method sig carrying it matches across interface and concrete in reflect.Implements.
	var cacheKey MethodStructKey
	var cachep *map[MethodStructKey]*SynthReservation
	if ShareMethodCarriers {
		if cachep = ActiveRtypeCache(); cachep != nil {
			cacheKey = MethodStructKey{name: QualifiedTypeName(t), layoutSig: layoutRT.String(), methodSig: methodFingerprint(t)}
			derivedMu.Lock()
			if shared := (*cachep)[cacheKey]; shared != nil {
				reservations[t] = shared
				derivedMu.Unlock()
				return adoptShared(t, shared)
			}
			derivedMu.Unlock()
		}
	}
	res := newReservation(t, layoutRT)
	if res == nil {
		return layoutRT
	}
	derivedMu.Lock()
	reservations[t] = res
	if cachep != nil {
		if *cachep == nil {
			*cachep = map[MethodStructKey]*SynthReservation{}
		}
		(*cachep)[cacheKey] = res
	}
	derivedMu.Unlock()
	return res.value.Type()
}

func reserveDefinedOverBase(t *mtype.Type, base reflect.Type) reflect.Type {
	if LookupReservation(t) != nil {
		return t.Rtype
	}
	if t.Name != "" && hasReservableMethods(t) {
		return reserveValueAndPtr(t, base)
	}
	// maybeReserve skips structs; a methodless defined struct over a native base
	// (type t time.Time) must not collapse to the base's identity.
	if base.Kind() == reflect.Struct {
		if t.Defined && t.Name != "" && t.PkgName != "" {
			return reserveNamedCarrier(t, base)
		}
		return base
	}
	return maybeReserve(t, base)
}

func maybeReserveStruct(t *mtype.Type) (rt reflect.Type, handled bool) {
	if isFieldClone(t) {
		rt := materialize(t.Base)
		t.Rtype = rt
		return rt, true
	}
	res := LookupReservation(t)
	if res == nil {
		if !hasReservableMethods(t) && !hasPromotedShapedMethods(t) {
			return nil, false // methodless struct: native identity is stable already
		}
		if res = newReservation(t, mtype.NewPlaceholderRtype(t.Name)); res == nil {
			return nil, false
		}
		derivedMu.Lock()
		reservations[t] = res
		derivedMu.Unlock()
	}
	reserved := res.value.Type()
	t.Rtype = reserved // stable identity for field cycles during this pass
	materializing.mark(reserved)
	release, reentrant := enterReserving(t)
	if reentrant {
		// A by-value type cycle re-entered this struct (a func-typed field taking
		// the struct by value); hand back the reserved identity instead of recursing.
		// The outer call still fills the real layout in place once its loop completes.
		return reserved, true
	}
	defer release()
	materializeStructFields(t)
	if !fieldsMaterialized(t.Fields) {
		materializing.clear(reserved)
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
	if structDeferred(t) {
		// An embedded by-value struct is still a word-sized placeholder, so realLayout's
		// size is provisional; or a func field's interface IO erased before its sigs were
		// ready; or a named func field is mid-materialization (signature erased to func());
		// or a named-iface field's synth rtype is not buildable yet.
		// Fill the reserved rtype to this best-effort shape (so compile-time walks
		// see a real struct) and defer the share-publish + final fill to FinalizeDeferred,
		// which re-enters once the cycle settles / the IO synths / the signature settles.
		runtype.FillStructLayout(reserved, realLayout)
		pendingFinalize.mark(t)
		t.Rtype = reserved
		return reserved, true
	}
	pendingFinalize.clear(t)
	// Methodless-table identity is safe to share across Evals (see sharedStructs).
	if !hasSynthTableMethods(t) {
		key := sharedStructKey{name: QualifiedTypeName(t), layoutSig: realLayout.String()}
		return shareOrFill(t, reserved, realLayout,
			func() *SynthReservation { return sharedStructs[key] },
			func() { sharedStructs[key] = res }), true
	}
	// Method-bearing: share per execution across re-Evals via the injected cache.
	if cachep := ActiveRtypeCache(); cachep != nil {
		key := MethodStructKey{
			name:      QualifiedTypeName(t),
			layoutSig: realLayout.String(),
			methodSig: methodFingerprint(t),
		}
		return shareOrFill(t, reserved, realLayout,
			func() *SynthReservation { return (*cachep)[key] },
			func() {
				if *cachep == nil {
					*cachep = map[MethodStructKey]*SynthReservation{}
				}
				(*cachep)[key] = res
			}), true
	}
	runtype.FillStructLayout(reserved, realLayout)
	materializing.clear(reserved)
	t.Rtype = reserved
	return reserved, true
}

// shareOrFill adopts an already-published equivalent reservation, or fills
// reserved with realLayout and publishes t's own via publish. The fill happens
// before the publish so a concurrent hit never adopts the placeholder.
// lookup and publish run under derivedMu.
func shareOrFill(t *mtype.Type, reserved, realLayout reflect.Type,
	lookup func() *SynthReservation, publish func(),
) reflect.Type {
	derivedMu.Lock()
	if shared := lookup(); shared != nil {
		reservations[t] = shared
		derivedMu.Unlock()
		materializing.clear(reserved)
		return adoptShared(t, shared)
	}
	runtype.FillStructLayout(reserved, realLayout)
	publish()
	derivedMu.Unlock()
	materializing.clear(reserved)
	t.Rtype = reserved
	return reserved
}

type derivedTypes struct {
	ptr   *mtype.Type
	slice *mtype.Type
	array map[int]*mtype.Type
	chans map[reflect.ChanDir]*mtype.Type
	maps  map[*mtype.Type]*mtype.Type // keyed by this type, indexed by elem
}

var (
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

var selfRefFuncPlaceholder = reflect.FuncOf(nil, nil, false)

func funcTypeInFlight(t *mtype.Type) bool {
	if t == nil || materializingFunc.empty() {
		return false
	}
	materializingFunc.mu.Lock()
	defer materializingFunc.mu.Unlock()
	// Depth-capped like the other Base walks (embTypeHasMethods, canonicalBase):
	// a multi-node Base cycle would otherwise spin here holding the lock.
	for i, c := 0, t; c != nil && i < canonicalTypeMaxDepth; i, c = i+1, c.Base {
		if materializingFunc.m[c] {
			return true
		}
		if c.Base == c {
			break
		}
	}
	return false
}

func anyFuncFieldInFlight(t *mtype.Type) bool {
	if materializingFunc.empty() {
		return false
	}
	return slices.ContainsFunc(t.Fields, funcTypeInFlight)
}

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

// MaterializeIfaceMethod fills im.Rtype from im.Sig under materializeMu (no-op if
// already set or Sig nil). The guard and write must be under the lock: a shared
// interface *Type is materialized by multiple parallel compilations, and an
// unsynchronized write here races a concurrent materialize that reads im.Rtype.
func MaterializeIfaceMethod(im *mtype.IfaceMethod) {
	materializeMu.Lock()
	defer materializeMu.Unlock()
	if im.Rtype == nil && im.Sig != nil {
		im.Rtype = materialize(im.Sig)
	}
}

// MaterializeMethod fills a concrete method's m.Rtype from m.Sig under
// materializeMu, for the same reason as MaterializeIfaceMethod.
func MaterializeMethod(m *mtype.Method) {
	materializeMu.Lock()
	defer materializeMu.Unlock()
	if m.Rtype == nil && m.Sig != nil {
		m.Rtype = materialize(m.Sig)
	}
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
	if t.Rtype != nil && !pendingFinalize.has(t) {
		return t.Rtype
	}
	if t.Placeholder {
		return nil // forward-declared struct/interface not yet finalized
	}
	if rt := nativeIdentityFor(t); rt != nil {
		t.Rtype = rt
		return rt
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
		elem := materializeContainerElem(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveSliceOf(elem)
	case reflect.Array:
		elem := materializeContainerElem(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveArrayOf(t.ArrayLen, elem)
		if geomDepInFlight(t.ElemType) { // elem grows later; recompute stride in FinalizeDeferred
			pendingGeom.mark(rt)
		}
	case reflect.Chan:
		elem := materializeContainerElem(t.ElemType)
		if elem == nil {
			return nil
		}
		rt = runtype.DeriveChanOf(t.ChanDir, elem)
	case reflect.Map:
		key, elem := materializeContainerElem(t.KeyType), materializeContainerElem(t.ElemType)
		if key == nil || elem == nil {
			return nil
		}
		rt = runtype.DeriveMapOf(key, elem)
		if geomDepInFlight(t.KeyType) || geomDepInFlight(t.ElemType) { // key/elem grows later
			pendingGeom.mark(rt)
		}
	case reflect.Func:
		if t.Name != "" {
			// A named func type may be self-referential (type parseFn func() parseFn),
			// which reflect.FuncOf can't build. Pre-set a placeholder a self-reference
			// in Params/Returns resolves to; when t isn't self-referential nothing
			// reads it and the real signature below overwrites it unobserved. While the
			// placeholder is live a struct field sharing this *Type would bake the erased
			// func(); mark it in-flight so such a struct defers (see anyFuncFieldInFlight)
			// and FinalizeDeferred re-patches it once the real signature settles.
			t.Rtype = selfRefFuncPlaceholder
			materializingFunc.mark(t)
			// Clear via defer so a panic in the IO materialization below (or any
			// recovered compile panic) can't leak the entry and leave the count
			// elevated for later Evals. Fires at this materialize call's return,
			// after the full signature (incl. nested cycle re-entry) is built.
			defer materializingFunc.clear(t)
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
			pendingFinalize.mark(t)
		} else {
			pendingFinalize.clear(t)
		}
	case reflect.Struct:
		return materializeStruct(t)
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

func materializeStruct(t *mtype.Type) reflect.Type {
	if len(t.Fields) == 0 && t.Base != nil && t.Base != t && t.Base.Kind() == reflect.Struct {
		// Defined type (type T1 T) whose Fields were cloned empty before the
		// underlying was finalized: materialize from the underlying's layout.
		rt := materialize(t.Base)
		if rt != nil {
			t.Rtype = rt
		}
		return rt
	}
	// A struct-field clone carries the field name in t.Name (for buildStructRtype)
	// but its type is the anonymous struct in t.Base; materialize that base so the
	// field type keeps reflect's empty name rather than inheriting the field name.
	if !t.Defined && t.Name != "" && t.Base != nil && t.Base != t &&
		t.Base.Kind() == reflect.Struct && t.Base.Name == "" {
		if rt := materialize(t.Base); rt != nil {
			t.Rtype = rt
			return rt
		}
	}
	if t.Name != "" {
		return materializeNamedStruct(t)
	}
	return materializeAnonStruct(t)
}

func materializeNamedStruct(t *mtype.Type) reflect.Type {
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
	materializing.mark(ph)
	release, reentrant := enterReserving(t)
	if reentrant {
		return ph // by-value cycle re-entered this struct; reuse its placeholder
	}
	defer release()
	// Stamp the real name before materializing fields so a self-referential
	// field (S *S, []*S, map[K]*S, ...) bakes the correct name into its
	// derived rtype: reflect.PointerTo/SliceOf/MapOf snapshot the element's
	// String() at derivation time, so naming ph afterward would leave the
	// derived types reading the placeholder name (*struct{...}).
	// Method-bearing types get their name from attach instead.
	named := len(t.Methods) == 0
	if named {
		runtype.StampNamePkg(ph, QualifiedTypeName(t), RtypePkgPath(t))
	}
	materializeStructFields(t)
	if !fieldsMaterialized(t.Fields) {
		// A field references a not-yet-finalized placeholder (e.g. *T sibling):
		// reset Rtype so a later call retries once it is finalized.
		materializing.clear(ph)
		t.Rtype = nil
		return nil
	}
	// Patch ph to the current best-effort layout.
	layout := mtype.BuildStructRtype(t.Fields, t.Embedded, t.Tags)
	mtype.PatchRtype(ph, layout)
	if named {
		runtype.StampNamePkg(ph, QualifiedTypeName(t), RtypePkgPath(t))
		if !structDeferred(t) {
			if shared := shareOrAdoptPlain(t, ph, layout); shared != ph {
				return shared
			}
		}
	}
	return finishStructOrDefer(t, ph)
}

// shareOrAdoptPlain converges a finalized methodless named struct on one rtype per name+layout: reflect cannot even compare two identities of a recursive layout (structural walk, no cycle check).
// Publishes ph on first sight, else adopts the published rtype and orphans ph.
func shareOrAdoptPlain(t *mtype.Type, ph reflect.Type, layout reflect.Type) reflect.Type {
	key := plainStructKey{name: QualifiedTypeName(t), pkgPath: RtypePkgPath(t), layoutSig: plainLayoutSig(layout, ph)}
	derivedMu.Lock()
	shared := sharedPlainStructs[key]
	if shared == nil || shared == ph {
		sharedPlainStructs[key] = ph
		derivedMu.Unlock()
		return ph
	}
	dropDerivedRtypes(t)
	derivedMu.Unlock()
	materializing.clear(ph)
	pendingFinalize.clear(t)
	t.Rtype = shared
	return shared
}

// plainLayoutSig fingerprints layout by field-type identity, expanding only what reaches ph (the self-reference, printed "@").
// A name-based sig (layout.String()) would alias same-named fields of different layouts; identities compose bottom-up, so equal sigs imply equal layouts.
func plainLayoutSig(layout, ph reflect.Type) string {
	var b strings.Builder
	for f := range layout.Fields() {
		fmt.Fprintf(&b, "%s %s %q %v;", f.Name, identSig(f.Type, ph, nil), f.Tag, f.Anonymous)
	}
	return b.String()
}

func identSig(rt, self reflect.Type, path map[reflect.Type]bool) string {
	if !reachesType(rt, self, nil) {
		return fmt.Sprintf("%p", rt)
	}
	if rt == self {
		return "@"
	}
	if path[rt] {
		return "^" + rt.String() // back-ref into an expansion cycle
	}
	if path == nil {
		path = map[reflect.Type]bool{}
	}
	path[rt] = true
	defer delete(path, rt)
	switch rt.Kind() {
	case reflect.Pointer:
		return "*" + identSig(rt.Elem(), self, path)
	case reflect.Slice:
		return "[]" + identSig(rt.Elem(), self, path)
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", rt.Len(), identSig(rt.Elem(), self, path))
	case reflect.Chan:
		return fmt.Sprintf("chan%d ", rt.ChanDir()) + identSig(rt.Elem(), self, path)
	case reflect.Map:
		return "map[" + identSig(rt.Key(), self, path) + "]" + identSig(rt.Elem(), self, path)
	case reflect.Struct:
		var b strings.Builder
		b.WriteString(rt.String() + "{") // keep the name: expansion must not alias same-shaped named types
		for f := range rt.Fields() {
			fmt.Fprintf(&b, "%s %s %q %v;", f.Name, identSig(f.Type, self, path), f.Tag, f.Anonymous)
		}
		b.WriteString("}")
		return b.String()
	case reflect.Func:
		var b strings.Builder
		b.WriteString("func(")
		for in := range rt.Ins() {
			b.WriteString(identSig(in, self, path) + ",")
		}
		b.WriteString(")(")
		for out := range rt.Outs() {
			b.WriteString(identSig(out, self, path) + ",")
		}
		b.WriteString(")")
		return b.String()
	case reflect.Interface:
		var b strings.Builder
		b.WriteString(rt.String() + "iface{")
		for m := range rt.Methods() {
			b.WriteString(m.Name + " " + identSig(m.Type, self, path) + ";")
		}
		b.WriteString("}")
		return b.String()
	}
	return fmt.Sprintf("%p", rt)
}

// reachesType reports whether t's structure reaches target (pass a nil path).
func reachesType(t, target reflect.Type, path map[reflect.Type]bool) bool {
	if t == target {
		return true
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan,
		reflect.Map, reflect.Struct, reflect.Func, reflect.Interface:
	default:
		return false
	}
	if path[t] {
		return false
	}
	if path == nil {
		path = map[reflect.Type]bool{}
	}
	path[t] = true
	defer delete(path, t)
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		return reachesType(t.Elem(), target, path)
	case reflect.Map:
		return reachesType(t.Key(), target, path) || reachesType(t.Elem(), target, path)
	case reflect.Struct:
		for f := range t.Fields() {
			if reachesType(f.Type, target, path) {
				return true
			}
		}
	case reflect.Func:
		for in := range t.Ins() {
			if reachesType(in, target, path) {
				return true
			}
		}
		for out := range t.Outs() {
			if reachesType(out, target, path) {
				return true
			}
		}
	case reflect.Interface:
		for m := range t.Methods() {
			if reachesType(m.Type, target, path) {
				return true
			}
		}
	}
	return false
}

// dropDerivedRtypes nils derived Rtypes minted against t's orphaned placeholder so they re-derive from the adopted identity.
// Caller holds derivedMu.
func dropDerivedRtypes(t *mtype.Type) {
	d := derivedCache[t]
	if d == nil {
		return
	}
	nodes := []*mtype.Type{d.ptr, d.slice}
	for _, n := range d.array {
		nodes = append(nodes, n)
	}
	for _, n := range d.chans {
		nodes = append(nodes, n)
	}
	for _, n := range d.maps {
		nodes = append(nodes, n)
	}
	for _, n := range nodes {
		if n != nil && n.Rtype != nil {
			n.Rtype = nil
			dropDerivedRtypes(n)
		}
	}
}

// materializeAnonStruct handles an anonymous struct. Only a by-value struct/array
// field can make it part of a by-value pointer cycle; without one, the direct
// build is correct and preserves reflect's embedded-interface method promotion.
// With one, use the cycle-safe path: reserve a shared placeholder under the
// structural key (identical anon shapes converge on one rtype), materialize
// fields, then patch -- deferring if a by-value field is still in-flight. A
// pending re-entry (FinalizeDeferred) skips the reserve/field steps and falls
// through to patch.
func materializeAnonStruct(t *mtype.Type) reflect.Type {
	if !hasByValueStructField(t) {
		materializeStructFields(t)
		if !fieldsMaterialized(t.Fields) {
			return nil
		}
		if synthIfaceFieldDeferred(t) {
			// Defer rather than let StructOf memoize an erased field.
			pendingFinalize.mark(t)
			return nil
		}
		pendingFinalize.clear(t)
		rt := mtype.StructOf(t.Fields, t.Embedded, t.Tags).Rtype
		t.Rtype = rt
		return rt
	}
	if !pendingFinalize.has(t) {
		resv, fresh := mtype.ReserveStruct(t.Fields, t.Embedded, t.Tags)
		t.Rtype = resv.Rtype
		if !fresh {
			return resv.Rtype // a sibling reserved this shape; share its placeholder
		}
		materializing.mark(resv.Rtype)
		materializeStructFields(t)
		if !fieldsMaterialized(t.Fields) {
			materializing.clear(resv.Rtype)
			mtype.UnreserveStruct(t.Fields, t.Embedded, t.Tags)
			t.Rtype = nil
			return nil
		}
	}
	ph := t.Rtype
	// Patch ph to the current best-effort layout.
	layout := mtype.FinalizeStruct(t)
	runtype.StampString(ph, layout.String()) // PatchRtype keeps ph's Str; restore the anon shape (stays unnamed)
	return finishStructOrDefer(t, ph)
}

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
	elemReaches := compositeReachesSelf(t.ElemType, t, map[*mtype.Type]bool{})
	// A named func elem/key still mid-materialization is a *timing* self-reference:
	// resolving it now bakes the erased func() placeholder. Take the same
	// reserve+SetElem path and defer the real signature. funcElemDeferred keeps us
	// here on the FinalizeDeferred re-entry, after the func has settled and
	// funcTypeInFlight no longer reports it.
	funcInFlight := funcTypeInFlight(t.ElemType) || (k == reflect.Map && funcTypeInFlight(t.KeyType))
	if !elemReaches && !funcInFlight && !funcElemDeferred.has(t) {
		if k == reflect.Map && compositeReachesSelf(t.KeyType, t, map[*mtype.Type]bool{}) {
			return nil, true // self-referential map key: not materializable
		}
		return nil, false
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
	if funcInFlight {
		funcElemDeferred.mark(t)
		pendingFinalize.mark(t)
	} else {
		funcElemDeferred.clear(t)
		pendingFinalize.clear(t)
	}
	return rt, true
}

func reserveSelfRef(t *mtype.Type, donor reflect.Type) reflect.Type {
	if res := LookupReservation(t); res != nil {
		return res.value.Type()
	}
	if hasReservableMethods(t) {
		return reserveValueAndPtr(t, donor)
	}
	res := newCarrier(t, donor)
	if res == nil {
		return nil
	}
	derivedMu.Lock()
	reservations[t] = res
	derivedMu.Unlock()
	return res.value.Type()
}

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

// NamedSynthIface reports whether p is a named, non-generic, method-bearing
// interpreted interface still erased to any.
func NamedSynthIface(p *mtype.Type) bool {
	return p != nil && p.Name != "" && p.Kind() == reflect.Interface && len(p.IfaceMethods) > 0 &&
		(p.Rtype == nil || p.Rtype == mtype.AnyRtype) && !IsGenericInstanceName(p.Name)
}

// containerElemSynthSafe reports whether iface p's synth rtype can be satisfied
// by every conforming concrete: each method has a known, stub-shaped signature
// (an over-word-budget method never attaches, failing reflect.Implements), and
// unexported methods are declared in p's own package (a foreign marker like
// grpc SubConn's enforceSubConnEmbedding gets the wrong pkgpath in the synth
// method table). Otherwise the container elem must stay erased.
func containerElemSynthSafe(p *mtype.Type) bool {
	own := RtypePkgPath(p)
	for i := range p.IfaceMethods {
		im := &p.IfaceMethods[i]
		if im.PkgPath != "" && im.PkgPath != own {
			return false
		}
		sig := im.Rtype
		if sig == nil && im.Sig != nil {
			sig = materialize(im.Sig)
		}
		if sig == nil || sig.Kind() != reflect.Func || !sigHasSynthShape(sig) {
			return false
		}
	}
	return true
}

func materializeContainerElem(p *mtype.Type) reflect.Type {
	if NamedSynthIface(p) && containerElemSynthSafe(p) {
		if rt := SynthIfaceRtype(p); rt != nil {
			return rt
		}
	}
	return materialize(p)
}

func synthIfaceFieldBase(f *mtype.Type) *mtype.Type {
	if !ShareMethodCarriers {
		return nil
	}
	it := f
	if f.Base != nil && f.Base.Kind() == reflect.Interface {
		it = f.Base
	}
	if NamedSynthIface(it) {
		return it
	}
	return nil
}

func materializeStructFields(t *mtype.Type) {
	for _, f := range t.Fields {
		materialize(f)
		if it := synthIfaceFieldBase(f); it != nil {
			if rt := SynthIfaceRtype(it); rt != nil {
				f.Rtype = rt
			}
		}
	}
}

func synthIfaceFieldDeferred(t *mtype.Type) bool {
	if !ShareMethodCarriers {
		return false
	}
	for _, f := range t.Fields {
		it := synthIfaceFieldBase(f)
		if it == nil {
			continue
		}
		if f.Rtype == nil || (!runtype.IsSynth(f.Rtype) && f.Rtype != nativeIdentityFor(it)) {
			return true
		}
	}
	return false
}

func materializeFuncIO(p *mtype.Type) reflect.Type {
	// Unexported methods never build an itab, so keep them any here: a func arg is
	// dispatched, not just displayed like a container elem.
	if NamedSynthIface(p) && allIfaceMethodsExported(p) {
		if rt := SynthIfaceRtype(p); rt != nil {
			return rt
		}
	}
	return materialize(p)
}

func ifaceSynthDeferrable(p *mtype.Type) bool {
	if p == nil || p.Name == "" || p.Kind() != reflect.Interface ||
		len(p.IfaceMethods) == 0 || !allIfaceMethodsExported(p) ||
		IsGenericInstanceName(p.Name) { // generic instances erase to any, never synth precisely
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

func funcIODeferrable(t *mtype.Type) bool {
	return slices.ContainsFunc(t.Params, ifaceSynthDeferrable) ||
		slices.ContainsFunc(t.Returns, ifaceSynthDeferrable)
}

func anyFieldPending(t *mtype.Type) bool {
	if pendingFinalize.empty() {
		return false
	}
	return slices.ContainsFunc(t.Fields, pendingFinalize.has)
}

func allIfaceMethodsExported(p *mtype.Type) bool {
	for _, im := range p.IfaceMethods {
		if !IsExportedName(im.Name) {
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

func synthIfaceNameKey(t *mtype.Type) string {
	if t == nil || t.Name == "" {
		return ""
	}
	names := make([]string, 0, len(t.IfaceMethods))
	for _, im := range t.IfaceMethods {
		sig := imethodSigString(im)
		if sig == "" {
			return ""
		}
		names = append(names, im.Name+":"+sig)
	}
	sort.Strings(names)
	return t.PkgName + "." + t.Name + "{" + strings.Join(names, ",") + "}"
}

func imethodSigString(im mtype.IfaceMethod) string {
	if im.Sig != nil {
		return im.Sig.String()
	}
	if im.Rtype != nil {
		return im.Rtype.String()
	}
	return ""
}

// AttachPtrDerived records newPtrRT as t's derived pointer type.
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
