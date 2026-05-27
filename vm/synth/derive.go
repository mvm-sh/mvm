package synth

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

// PointerTo returns the *elem rtype.
// *T is one machine word for every elem; we clone the layout from *int.
func PointerTo(elem reflect.Type) reflect.Type {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil
	}
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
// Array layout (Size, Align, GCData, Equal) depends on elem's layout, so we
// build the bones via reflect.ArrayOf on the layout shadow of elem, then
// clone and patch Elem to point at the synth elem.
// The cloned array's Slice field is left pointing at the shadow []layoutElem;
// reflect.Value.Slice on a synth array therefore yields the shadow slice
// type, which is acceptable since the use case for these constructors is
// type identity, not full Value-API parity.
func ArrayOf(n int, elem reflect.Type) reflect.Type {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil
	}
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
// Hasher and Group fields depend on the key's layout (kind/size/pointer
// presence); we build the bones via reflect.MapOf on the layout shadows of
// both key and elem, then clone and patch Key/Elem to the synth rtypes.
func MapOf(key, elem reflect.Type) reflect.Type {
	keyRT := rtypePtr(key)
	elemRT := rtypePtr(elem)
	if keyRT == nil || elemRT == nil {
		return nil
	}
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

// HasPtrToThis reports whether t's PtrToThis field is wired (i.e., an
// AttachPtrMethods call has registered a *T-with-methods rtype reachable
// via reflect.PointerTo(t)).
// vm.PointerTo consults this to prefer reflect.PointerTo over synth.PointerTo
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
// Called by every Attach*/Clone* path and by the derive constructors above
// so chained derivations (e.g. SliceOf(PointerTo(synthStruct))) resolve the
// outermost layout correctly.
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
