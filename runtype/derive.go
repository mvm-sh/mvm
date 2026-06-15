package runtype

import (
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"unsafe"
)

// PointerTo, SliceOf, ArrayOf, ChanOf, MapOf are synth-safe analogs of the
// reflect.* derived-type constructors.
// They build *T / []T / [N]T / chan T / map[K]V rtypes whose Elem (and Key)
// fields point at any rtype -- synth-built or native.
// The returned rtype is anonymous (no tflagNamed) and carries no methods.
//
// reflect.PointerTo and siblings are unsafe on synth rtypes: they clone a
// prototype (e.g. *unsafe.Pointer) then call resolveReflectName on
// "*"+elem.String(), and earlier paths in the ptrTo / sliceTo flow hit
// resolveNameOff with a base pointer outside any moduledata type range,
// throwing "name offset base pointer out of range".
// These constructors bypass that path entirely by stamping their own Str via
// addReflectOff.

// deriveKey identifies an anonymous derived composite by kind + component rtype
// identity (n holds an array length or chan dir).
type deriveKey struct {
	kind byte
	key  uintptr
	elem uintptr
	n    int
}

var (
	deriveCacheMu sync.Mutex
	deriveCache   = map[deriveKey]reflect.Type{}
)

// cachedDerive memoizes anonymous synth composites by component identity, so the
// same []T / *T / [n]T / chan T / map[K]V resolves to ONE rtype across every
// derivation -- matching reflect.*Of's global cache, which mvm bypasses for synth
// components. Without it two fields of the same synth-element type compare unequal
// under reflect.DeepEqual (distinct nextSyntheticHash identities).
func cachedDerive(k deriveKey, build func() reflect.Type) reflect.Type {
	deriveCacheMu.Lock()
	defer deriveCacheMu.Unlock()
	if rt := deriveCache[k]; rt != nil {
		return rt
	}
	rt := build()
	if rt != nil {
		deriveCache[k] = rt
	}
	return rt
}

func ptrID(rt *abiType) uintptr { return uintptr(unsafe.Pointer(rt)) }

// PointerTo returns the *elem rtype.
// *T is one machine word for every elem; we clone the layout from *int.
func PointerTo(elem reflect.Type) reflect.Type {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil
	}
	return cachedDerive(deriveKey{kind: 1, elem: ptrID(elemRT)}, func() reflect.Type {
		return buildPointerTo(elem, elemRT)
	})
}

func buildPointerTo(elem reflect.Type, elemRT *abiType) reflect.Type {
	intPtrRT := (*abiPtrType)(unsafe.Pointer(rtypePtr(reflect.TypeFor[*int]())))

	b := new(abiPtrType)
	b.abiType = intPtrRT.abiType
	// Anonymous derived; clear Uncommon/Named/ExtraStar.
	// RegularMemory must stay or runtime.typehash treats *T as unhashable.
	b.TFlag = tflagDirectIface | tflagRegularMemory
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName("*"+elem.String(), false).Bytes))
	b.Elem = elemRT

	intRT := rtypePtr(reflect.TypeFor[*int]())
	registerLayout(&b.abiType, intRT)
	return asReflectType(&b.abiType)
}

// SliceOf returns the []elem rtype.
// Slice header layout (Size 24, one pointer at offset 0) is independent of
// elem; we clone from []int.
func SliceOf(elem reflect.Type) reflect.Type {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil
	}
	return cachedDerive(deriveKey{kind: 2, elem: ptrID(elemRT)}, func() reflect.Type {
		return buildSliceOf(elem, elemRT)
	})
}

func buildSliceOf(elem reflect.Type, elemRT *abiType) reflect.Type {
	intSliceRT := (*abiSliceType)(unsafe.Pointer(rtypePtr(reflect.TypeFor[[]int]())))

	b := new(abiSliceType)
	b.abiType = intSliceRT.abiType
	b.TFlag = 0
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName("[]"+elem.String(), false).Bytes))
	b.Elem = elemRT

	layoutRT := rtypePtr(reflect.TypeFor[[]int]())
	registerLayout(&b.abiType, layoutRT)
	return asReflectType(&b.abiType)
}

// ChanOf returns the chan-elem rtype with the given direction.
// Channels are direct-iface single-word values regardless of elem; we clone
// the type-header from chan int.
func ChanOf(dir reflect.ChanDir, elem reflect.Type) reflect.Type {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil
	}
	return cachedDerive(deriveKey{kind: 3, elem: ptrID(elemRT), n: int(dir)}, func() reflect.Type {
		return buildChanOf(dir, elem, elemRT)
	})
}

func buildChanOf(dir reflect.ChanDir, elem reflect.Type, elemRT *abiType) reflect.Type {
	intChanRT := (*abiChanType)(unsafe.Pointer(rtypePtr(reflect.TypeFor[chan int]())))

	var prefix string
	switch dir {
	case reflect.RecvDir:
		prefix = "<-chan "
	case reflect.SendDir:
		prefix = "chan<- "
	default:
		prefix = "chan "
	}

	b := new(abiChanType)
	b.abiType = intChanRT.abiType
	b.TFlag = tflagRegularMemory // chans hash by ref; see buildPointerTo
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName(prefix+elem.String(), false).Bytes))
	b.Elem = elemRT
	b.Dir = uintptr(dir)

	layoutRT := rtypePtr(reflect.TypeFor[chan int]())
	registerLayout(&b.abiType, layoutRT)
	return asReflectType(&b.abiType)
}

// ArrayOf returns the [n]elem rtype.
// The cloned array's Slice field is repointed at the synth []elem so
// reflect.Value.Slice on a synth array (e.g. arr[:]) yields []elem, not the
// methodless shadow []layoutElem.
func ArrayOf(n int, elem reflect.Type) reflect.Type {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil
	}
	// SliceOf before cachedDerive: it re-enters cachedDerive, whose lock is held
	// for the whole build callback (non-reentrant).
	sliceRT := rtypePtr(SliceOf(elem))
	return cachedDerive(deriveKey{kind: 4, elem: ptrID(elemRT), n: n}, func() reflect.Type {
		return buildArrayOf(n, elem, elemRT, sliceRT)
	})
}

func buildArrayOf(n int, elem reflect.Type, elemRT, sliceRT *abiType) reflect.Type {
	layoutArr := reflect.ArrayOf(n, layoutFor(elem))
	src := (*abiArrayType)(unsafe.Pointer(rtypePtr(layoutArr)))

	b := new(abiArrayType)
	*b = *src
	b.TFlag = 0
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName("["+strconv.Itoa(n)+"]"+elem.String(), false).Bytes))
	b.Elem = elemRT
	b.Slice = sliceRT

	registerLayout(&b.abiType, rtypePtr(layoutArr))
	return asReflectType(&b.abiType)
}

// MapOf returns the map[key]elem rtype.
func MapOf(key, elem reflect.Type) reflect.Type {
	keyRT := rtypePtr(key)
	elemRT := rtypePtr(elem)
	if keyRT == nil || elemRT == nil {
		return nil
	}
	return cachedDerive(deriveKey{kind: 5, key: ptrID(keyRT), elem: ptrID(elemRT)}, func() reflect.Type {
		return buildMapOf(key, elem, keyRT, elemRT)
	})
}

func buildMapOf(key, elem reflect.Type, keyRT, elemRT *abiType) reflect.Type {
	layoutShadow := reflect.MapOf(layoutFor(key), layoutFor(elem))
	src := (*abiMapType)(unsafe.Pointer(rtypePtr(layoutShadow)))

	b := new(abiMapType)
	*b = *src
	// Keep src's layout flags (esp. DirectIface -- a map is one word); clear only
	// the name/uncommon bits we manage. Zeroing TFlag drops DirectIface, so boxing
	// the map (reflect packEface) panics "bad indir".
	b.TFlag = src.TFlag &^ (tflagNamed | tflagUncommon | tflagExtraStar)
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName("map["+key.String()+"]"+elem.String(), false).Bytes))
	b.Key = keyRT
	b.Elem = elemRT

	registerLayout(&b.abiType, rtypePtr(layoutShadow))
	return asReflectType(&b.abiType)
}

// Swiss-map constants, mirroring internal/abi (MaxKey==MaxElem across go1.24-1.26).
const (
	mapMaxKeyBytes  = 128
	mapMaxElemBytes = 128
	mapGroupSlots   = 8
	mapIndirectKey  = 1 << 2 // abi.MapIndirectKey
	mapIndirectElem = 1 << 3 // abi.MapIndirectElem
)

var geomSeq atomic.Uint64

// RebuildMapGeometry recomputes mapRT's slot/group geometry in place (identity
// preserved) after its key or elem was resized in place from a forward placeholder.
// Unique field names stop reflect.StructOf returning the placeholder-sized layout
// (reflect caches by identity, which the in-place resize preserves).
func RebuildMapGeometry(mapRT reflect.Type) {
	rt := rtypePtr(mapRT)
	if rt == nil || mapRT.Kind() != reflect.Map {
		return
	}
	mt := (*abiMapType)(unsafe.Pointer(rt))
	key := layoutFor(asReflectType(mt.Key))
	elem := layoutFor(asReflectType(mt.Elem))
	// Mirror reflect.groupAndSlotOf: large key/elem are stored indirectly.
	keyIndirect, elemIndirect := false, false
	if key.Size() > mapMaxKeyBytes {
		key, keyIndirect = reflect.PointerTo(key), true
	}
	if elem.Size() > mapMaxElemBytes {
		elem, elemIndirect = reflect.PointerTo(elem), true
	}
	n := strconv.FormatUint(geomSeq.Add(1), 36)
	slot := reflect.StructOf([]reflect.StructField{
		{Name: "K" + n, Type: key},
		{Name: "E" + n, Type: elem},
	})
	group := reflect.StructOf([]reflect.StructField{
		{Name: "C" + n, Type: reflect.TypeFor[uint64]()},
		{Name: "S" + n, Type: reflect.ArrayOf(mapGroupSlots, slot)},
	})
	mt.Group = rtypePtr(group)
	mt.GroupSize = group.Size()
	mt.SlotSize = slot.Size()
	mt.ElemOff = slot.Field(1).Offset
	mt.Flags &^= mapIndirectKey | mapIndirectElem
	if keyIndirect {
		mt.Flags |= mapIndirectKey
	}
	if elemIndirect {
		mt.Flags |= mapIndirectElem
	}
	// MapNeedKeyUpdate/MapHashMightPanic left as built: the Hasher self-heals on the
	// in-place resize, and they affect stored-key identity, not memory layout.
}

// RebuildArrayGeometry recomputes arrRT's size/pointer-layout in place after its
// elem was resized from a forward placeholder; reports whether it changed.
// [n]elem and a struct of n elem fields share a layout, so a cache-busting
// StructOf recomputes it from the grown elem.
func RebuildArrayGeometry(arrRT reflect.Type) bool {
	rt := rtypePtr(arrRT)
	if rt == nil || arrRT.Kind() != reflect.Array {
		return false
	}
	at := (*abiArrayType)(unsafe.Pointer(rt))
	elem := layoutFor(asReflectType(at.Elem))
	if at.Size == at.Len*elem.Size() {
		return false // already sized for the current elem (Go arrays have no inter-element padding)
	}
	n := strconv.FormatUint(geomSeq.Add(1), 36)
	fields := make([]reflect.StructField, int(at.Len))
	for i := range fields {
		fields[i] = reflect.StructField{Name: "E" + n + "_" + strconv.Itoa(i), Type: elem}
	}
	src := rtypePtr(reflect.StructOf(fields))
	at.Size = src.Size
	at.PtrBytes = src.PtrBytes
	at.Align = src.Align
	at.FieldAlign = src.FieldAlign
	at.GCData = src.GCData
	at.Equal = src.Equal
	registerLayout(rt, src) // refresh the shadow so a containing map/array reads the new size
	return true
}

// layoutFor returns the native-layout rtype matching a synth rtype, or t
// itself if t is native (not in the registry).
// The layout shadow has the same Size/Align/PtrBytes/GCData/Equal as the
// synth rtype but lives in a registered Go module, so it can be safely
// passed to reflect.ArrayOf / reflect.MapOf.
func layoutFor(t reflect.Type) reflect.Type {
	rt := rtypePtr(t)
	layoutMu.RLock()
	layout, ok := layoutMap[rt]
	layoutMu.RUnlock()
	if !ok {
		return t
	}
	return asReflectType(layout)
}

// StampName overwrites t's type name in place: registers name via
// addReflectOff, points Str at it, sets tflagNamed, and clears
// tflagExtraStar.
// Layout is untouched (a type name is a label, not structure), so t's
// identity, fields, methods, and any derived types stay valid.
// This is the safest possible synth mutation.
// CALLER CONTRACT: t must be a heap rtype mvm owns (e.g. a
// reflect.StructOf placeholder).  Stamping a shared canonical rtype
// (int, a native struct like time.Time) would corrupt the name of every
// value of that type process-wide.
func StampName(t reflect.Type, name string) {
	rt := rtypePtr(t)
	if rt == nil {
		return
	}
	rt.TFlag = (rt.TFlag &^ tflagExtraStar) | tflagNamed
	rt.Str = addReflectOff(unsafe.Pointer(encodeName(name, true).Bytes))
}

// IsSynth reports whether t is a synth-built rtype (produced by the Reserve* or
// derive constructors in this package).
// The Derive* helpers below route between reflect.*Of (native rtype identity
// preserved) and the synth-safe constructors above based on this predicate.
func IsSynth(t reflect.Type) bool {
	if t == nil {
		return false
	}
	rt := rtypePtr(t)
	layoutMu.RLock()
	_, ok := layoutMap[rt]
	layoutMu.RUnlock()
	return ok
}

// HasPtrToThis reports whether t's PtrToThis field is wired (i.e., a
// ReservePtrMethods call has registered a *T-with-methods rtype reachable
// via reflect.PointerTo(t)).
// DerivePointerTo consults this to prefer reflect.PointerTo over the synth
// PointerTo when the wired *T exists, so the two *T identities coincide.
func HasPtrToThis(t reflect.Type) bool {
	if t == nil {
		return false
	}
	rt := rtypePtr(t)
	if rt == nil {
		return false
	}
	return rt.PtrToThis != 0
}

// DerivePointerTo, DeriveSliceOf, DeriveArrayOf, DeriveChanOf, DeriveMapOf return
// the derived rtype for a possibly-synth element, choosing the right builder:
// the synth-safe constructors above for synth components (reflect.*Of crashes on
// them via resolveNameOff), reflect.* otherwise to keep the canonical native
// identity. They are the entry points the materialization layer calls; callers
// need not test IsSynth themselves.

// DerivePointerTo returns *elem. For a synth elem whose PtrToThis is already
// wired (a reserved *T-with-methods), reflect.PointerTo returns that wired *T, so
// the synth and reflect sides share one *T identity; otherwise a synth elem gets
// a fresh synth *T and a native elem its canonical reflect.PointerTo.
func DerivePointerTo(elem reflect.Type) reflect.Type {
	if IsSynth(elem) && !HasPtrToThis(elem) {
		return PointerTo(elem)
	}
	return reflect.PointerTo(elem)
}

// DeriveSliceOf returns []elem.
func DeriveSliceOf(elem reflect.Type) reflect.Type {
	if IsSynth(elem) {
		return SliceOf(elem)
	}
	return reflect.SliceOf(elem)
}

// DeriveArrayOf returns [n]elem.
func DeriveArrayOf(n int, elem reflect.Type) reflect.Type {
	if IsSynth(elem) {
		return ArrayOf(n, elem)
	}
	return reflect.ArrayOf(n, elem)
}

// DeriveChanOf returns the chan-elem rtype with the given direction.
func DeriveChanOf(dir reflect.ChanDir, elem reflect.Type) reflect.Type {
	if IsSynth(elem) {
		return ChanOf(dir, elem)
	}
	return reflect.ChanOf(dir, elem)
}

// DeriveMapOf returns map[key]elem; synth if either component is synth.
func DeriveMapOf(key, elem reflect.Type) reflect.Type {
	if IsSynth(key) || IsSynth(elem) {
		return MapOf(key, elem)
	}
	return reflect.MapOf(key, elem)
}

// registerLayout records the native-layout rtype for a synth rtype.
// Called by every reserve path and by the derive constructors above so chained
// derivations (e.g. SliceOf(PointerTo(synthStruct))) resolve the outermost
// layout correctly.
func registerLayout(synthRT, layoutRT *abiType) {
	if synthRT == nil || layoutRT == nil {
		return
	}
	layoutMu.Lock()
	layoutMap[synthRT] = layoutRT
	layoutMu.Unlock()
}

var (
	layoutMu  sync.RWMutex
	layoutMap = map[*abiType]*abiType{}
)
