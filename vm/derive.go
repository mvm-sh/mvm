package vm

import (
	"reflect"
	"sync"

	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/runtype"
)

// Runtime materialization of mtype.Type: derived-type construction, the synth
// cascade, and field/elem patching. These are runtime concerns, kept out of the
// symbolic mtype package so it imports no runtype. The caches are side tables
// keyed by *mtype.Type identity, so a cloned *Type gets a fresh entry -- no
// shared derived cache, hence no placeholder aliasing.

type derivedTypes struct {
	ptr   *mtype.Type
	slice *mtype.Type
	array map[int]*mtype.Type
	chans map[reflect.ChanDir]*mtype.Type
	maps  map[*mtype.Type]*mtype.Type
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

// PointerTo returns the canonical pointer type with element t, memoized.
func PointerTo(t *mtype.Type) *mtype.Type {
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
	a := &mtype.Type{Rtype: deriveArrayOf(length, t.Rtype), ElemType: t}
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
	return m
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
	c := &mtype.Type{Rtype: deriveChanOf(dir, elem.Rtype), ElemType: elem}
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
