package runtype

import (
	"reflect"
	"strconv"
	"sync"
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
	intPtrRT := (*abiPtrType)(unsafe.Pointer(rtypePtr(reflect.TypeOf((*int)(nil)))))

	b := new(abiPtrType)
	b.abiType = intPtrRT.abiType
	b.TFlag = tflagDirectIface // anonymous derived; clear Uncommon/Named/ExtraStar
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName("*"+elem.String(), false).Bytes))
	b.Elem = elemRT

	intRT := rtypePtr(reflect.TypeOf((*int)(nil)))
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
	intSliceRT := (*abiSliceType)(unsafe.Pointer(rtypePtr(reflect.TypeOf([]int(nil)))))

	b := new(abiSliceType)
	b.abiType = intSliceRT.abiType
	b.TFlag = 0
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName("[]"+elem.String(), false).Bytes))
	b.Elem = elemRT

	layoutRT := rtypePtr(reflect.TypeOf([]int(nil)))
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
	intChanRT := (*abiChanType)(unsafe.Pointer(rtypePtr(reflect.TypeOf((chan int)(nil)))))

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
	b.TFlag = 0
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName(prefix+elem.String(), false).Bytes))
	b.Elem = elemRT
	b.Dir = uintptr(dir)

	layoutRT := rtypePtr(reflect.TypeOf((chan int)(nil)))
	registerLayout(&b.abiType, layoutRT)
	return asReflectType(&b.abiType)
}

// ArrayOf returns the [n]elem rtype.
// The cloned array's Slice field is left pointing at the shadow []layoutElem;
// reflect.Value.Slice on a synth array therefore yields the shadow slice
// type, which is acceptable since the use case for these constructors is
// type identity, not full Value-API parity.
func ArrayOf(n int, elem reflect.Type) reflect.Type {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil
	}
	return cachedDerive(deriveKey{kind: 4, elem: ptrID(elemRT), n: n}, func() reflect.Type {
		return buildArrayOf(n, elem, elemRT)
	})
}

func buildArrayOf(n int, elem reflect.Type, elemRT *abiType) reflect.Type {
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
	b.TFlag = 0
	b.Hash = nextSyntheticHash()
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(
		encodeName("map["+key.String()+"]"+elem.String(), false).Bytes))
	b.Key = keyRT
	b.Elem = elemRT

	registerLayout(&b.abiType, rtypePtr(layoutShadow))
	return asReflectType(&b.abiType)
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
// identity, fields, methods, and any derived types stay valid -- no
// cascade needed.  This is the safest possible synth mutation.
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

// IsSynth reports whether t is a synth-built rtype (produced by any of the
// Attach*, Clone*, or derive constructors in this package).
// Callers route between reflect.*Of (native rtype identity preserved) and
// the synth-safe constructors above based on this predicate.
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
// vm.PointerTo consults this to prefer reflect.PointerTo over runtype.PointerTo
// when the wired *T exists, so the vm-side derived *T and the reflect-side
// *T share identity.
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
