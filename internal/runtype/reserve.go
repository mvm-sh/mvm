package runtype

import (
	"errors"
	"reflect"
	"sync"
	"unsafe"
)

// Reservation is a synth rtype whose method table has room allocated but is
// initially empty (Mcount 0). Fill installs the methods in place, preserving the
// rtype's identity, so a container that captured the reserved rtype before Fill
// observes the methods afterward (the basis for cascade-free materialize-once).
type Reservation struct {
	rt reflect.Type
	u  *abiUncommon
	m  []abiMethod // the container's [maxMethods] method array, full length
}

// Type returns the reserved rtype. It is a valid named type with zero methods
// until Fill runs.
func (r *Reservation) Type() reflect.Type { return r.rt }

var errNilReservation = errors.New("runtype: Fill on nil Reservation")

// Fill installs methods into the reserved method table in place, publishing the
// counts last so a reader seeing them also sees written slots; the caller must
// establish happens-before to other goroutines (attach runs before the VM resumes).
// len(methods) must be in [1, maxMethods].
func (r *Reservation) Fill(methods []MethodSpec) error {
	if r == nil || r.u == nil {
		return errNilReservation
	}
	if err := checkMethodCount(methods); err != nil {
		return err
	}
	installMethods(r.m[:len(methods)], methods)
	xcount := 0
	for _, m := range methods {
		if m.Exported {
			xcount++
		}
	}
	r.u.Xcount = uint16(xcount)
	r.u.Mcount = uint16(len(methods)) // published last
	return nil
}

// ClearUncommon strips the uncommon-section flag, making a pure named carrier
// (no method table, PkgPath() == "") that StructOf accepts as an embedded field.
// Func kinds keep the flag: their in/out array sits after the uncommon struct,
// so clearing it would make reflect read uncommon bytes as type pointers.
func ClearUncommon(t reflect.Type) {
	if t.Kind() == reflect.Func {
		return
	}
	rtypePtr(t).TFlag &^= tflagUncommon
}

// EmbedTripsStructOf reports whether t embedded in a multi-field struct would
// panic reflect.StructOf: a direct-iface type with an uncommon section (even empty).
func EmbedTripsStructOf(t reflect.Type) bool {
	rt := rtypePtr(t)
	if rt == nil {
		return false
	}
	return rt.TFlag&tflagUncommon != 0 && rt.TFlag&tflagDirectIface != 0
}

// ReserveMethods reserves a method-bearing identity for layout (dispatching by
// kind) without yet installing any method. Fill the returned Reservation once the
// method stubs are known.
func ReserveMethods(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	switch k := layout.Kind(); {
	case k == reflect.Struct:
		return reserveStruct(layout, name, pkgPath)
	case isPrimitiveKind(k):
		return reservePrimitive(layout, name, pkgPath)
	case k == reflect.Slice:
		return reserveSlice(layout, name, pkgPath)
	case k == reflect.Array:
		return reserveArray(layout, name, pkgPath)
	case k == reflect.Map:
		return reserveMap(layout, name, pkgPath)
	case k == reflect.Func:
		return reserveFunc(layout, name, pkgPath)
	case k == reflect.Pointer:
		return reservePtr(layout, name, pkgPath)
	case k == reflect.Chan:
		return reserveChan(layout, name, pkgPath)
	}
	return nil, ErrKindUnsupported
}

func reserveStruct(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiStructType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthStruct)
	b.st = *src
	stampHeader(&b.st.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	registerLayout(&b.st.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.st.abiType), u: &b.u, m: b.m[:]}, nil
}

func reservePrimitive(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := rtypePtr(layout)
	b := new(synthPrim)
	b.t = *src
	stampHeader(&b.t, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	registerLayout(&b.t, src)
	return &Reservation{rt: asReflectType(&b.t), u: &b.u, m: b.m[:]}, nil
}

func reserveSlice(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiSliceType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthSlice)
	b.t = *src
	stampHeader(&b.t.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	registerLayout(&b.t.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t.abiType), u: &b.u, m: b.m[:]}, nil
}

func reserveArray(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiArrayType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthArray)
	b.t = *src
	stampHeader(&b.t.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	registerLayout(&b.t.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t.abiType), u: &b.u, m: b.m[:]}, nil
}

func reserveMap(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiMapType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthMap)
	b.t = *src
	stampHeader(&b.t.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	registerLayout(&b.t.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t.abiType), u: &b.u, m: b.m[:]}, nil
}

func reserveFunc(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	nin, nout := layout.NumIn(), layout.NumOut()
	if nin+nout > maxFuncIO {
		return nil, errFuncTooManyIO
	}
	src := (*abiFuncType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthFunc)
	b.t = *src
	stampHeader(&b.t.abiType, name)
	for i := range nin {
		b.io[i] = (*abiType)(unsafe.Pointer(rtypePtr(layout.In(i))))
	}
	for i := range nout {
		b.io[nin+i] = (*abiType)(unsafe.Pointer(rtypePtr(layout.Out(i))))
	}
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	registerLayout(&b.t.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t.abiType), u: &b.u, m: b.m[:]}, nil
}

// reservePtr clones a *T layout under a new name.
// Unlike ReservePtrMethods it leaves the donor elem untouched; SetElem patches it later.
func reservePtr(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiPtrType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthPtr)
	b.t = src.abiType
	b.elem = src.Elem
	stampHeader(&b.t, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	registerLayout(&b.t, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t), u: &b.u, m: b.m[:]}, nil
}

func reserveChan(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiChanType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthChan)
	b.t = *src
	stampHeader(&b.t.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	registerLayout(&b.t.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t.abiType), u: &b.u, m: b.m[:]}, nil
}

// SetElem repoints a composite rtype's Elem in place (self-referential type P *P).
// The rtype must be a reservation or synth derive, never native.
func SetElem(t, elem reflect.Type) {
	rt := rtypePtr(t)
	ert := rtypePtr(elem)
	switch t.Kind() {
	case reflect.Pointer:
		(*abiPtrType)(unsafe.Pointer(rt)).Elem = ert
	case reflect.Slice:
		(*abiSliceType)(unsafe.Pointer(rt)).Elem = ert
	case reflect.Chan:
		(*abiChanType)(unsafe.Pointer(rt)).Elem = ert
	case reflect.Map:
		(*abiMapType)(unsafe.Pointer(rt)).Elem = ert
	}
}

// ReservePtrMethods reserves a *T identity carrying eventual ptr-receiver
// methods and wires elem.PtrToThis so reflect.PointerTo(elem) returns it, all
// before Fill.
func ReservePtrMethods(elem reflect.Type, name, pkgPath string) (*Reservation, error) {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil, errNilElemType
	}
	b := new(synthPtr)
	b.elem = elemRT
	intPtrRT := rtypePtr(reflect.TypeFor[*int]())
	// No tflagNamed: a derived *T is unnamed in Go, methods or not; only a
	// declared `type P *T` is named (reservePtr). The uncommon section still
	// carries pkgPath for unexported method-name matching, like native *T.
	b.t = abiType{
		Size:       unsafe.Sizeof(uintptr(0)),
		PtrBytes:   unsafe.Sizeof(uintptr(0)),
		Hash:       nextSyntheticHash(),
		TFlag:      tflagUncommon | tflagDirectIface | tflagRegularMemory,
		Align:      uint8(unsafe.Alignof(uintptr(0))),
		FieldAlign: uint8(unsafe.Alignof(uintptr(0))),
		Kind:       kindPointer,
		Equal:      intPtrRT.Equal,
		GCData:     intPtrRT.GCData,
		Str:        addReflectOff(unsafe.Pointer(encodeName(name, true).Bytes)),
		PtrToThis:  0,
	}
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, uint32(moff))
	elemRT.PtrToThis = addReflectOff(unsafe.Pointer(&b.t))
	registerLayout(&b.t, intPtrRT)
	return &Reservation{rt: asReflectType(&b.t), u: &b.u, m: b.m[:]}, nil
}

// CloneStructLayoutWithFields returns a fresh, unnamed struct rtype with the same
// memory layout as src but the listed fields' Typ repointed. Each replacement type
// must have the same size and pointer shape as the field it replaces (both
// pointer-kind), so the struct's size, offsets, and gc data stay valid. The src
// rtype (often a reflect.StructOf-interned type shared by other structs) is left
// untouched: the clone gets its own Fields backing array.
//
// Used to restore a method-bearing embedded field's named identity after the
// struct was built with a methodless layout to satisfy reflect.StructOf, which
// refuses to promote a method-bearing embed at a non-first field. The field stays
// Anonymous (so json/fmt still flatten it) but reflect now reports its real,
// canonical type so reflect.DeepEqual and == see the same identity as a literal.
func CloneStructLayoutWithFields(src reflect.Type, fieldTypes map[int]reflect.Type) reflect.Type {
	s := (*abiStructType)(unsafe.Pointer(rtypePtr(src)))
	b := new(abiStructType)
	*b = *s
	// src may be a cached NATIVE type (StructOf shape collision) whose module-relative
	// Str/PtrToThis are invalid on this heap clone; re-register them like the derive
	// constructors. tflagExtraStar off so reflect keeps the fresh name's first byte.
	b.TFlag &^= tflagExtraStar
	b.PtrToThis = 0
	b.Str = addReflectOff(unsafe.Pointer(encodeName(src.String(), false).Bytes))
	b.Fields = make([]abiStructField, len(s.Fields))
	copy(b.Fields, s.Fields)
	for i, ft := range fieldTypes {
		if ft != nil && i >= 0 && i < len(b.Fields) {
			b.Fields[i].Typ = rtypePtr(ft)
		}
	}
	registerLayout(&b.abiType, &s.abiType)
	return asReflectType(&b.abiType)
}

// SetFieldEmbedded sets field idx's embedded bit, which StructOf refuses on an unexported embed (anon+PkgPath).
// rt MUST be a heap clone mvm owns: StructOf interns shapes, so editing a cached result corrupts every value of that shape.
func SetFieldEmbedded(rt reflect.Type, idx int, name, tag string) {
	s := (*abiStructType)(unsafe.Pointer(rtypePtr(rt)))
	if idx < 0 || idx >= len(s.Fields) {
		return
	}
	s.Fields[idx].Name = encodeFieldName(name, tag, false, true)
}

const abiStructTypeSize = unsafe.Sizeof(abiStructType{})

// fillKeepAlive pins realLayouts whose Fields slices FillStructLayout aliases.
var (
	fillKeepAliveMu sync.Mutex
	fillKeepAlive   []reflect.Type
)

// FillStructLayout patches realLayout's struct layout into a reserved struct
// rtype in place, preserving the reservation's Str/PtrToThis (offsets 40-48),
// its uncommon+named flags, and its method table (past the struct header). Use
// it to fill a struct reserved with a provisional layout once its fields are
// known -- so a pointer cycle (field *T) resolves to the reserved identity.
func FillStructLayout(reserved, realLayout reflect.Type) {
	fillKeepAliveMu.Lock()
	fillKeepAlive = append(fillKeepAlive, realLayout)
	fillKeepAliveMu.Unlock()

	d := rtypePtr(reserved)
	keep := d.TFlag & (tflagUncommon | tflagNamed)
	dp, sp := unsafe.Pointer(d), unsafe.Pointer(rtypePtr(realLayout))
	for i := range uintptr(40) {
		*(*byte)(unsafe.Add(dp, i)) = *(*byte)(unsafe.Add(sp, i))
	}
	for i := uintptr(48); i < abiStructTypeSize; i++ {
		*(*byte)(unsafe.Add(dp, i)) = *(*byte)(unsafe.Add(sp, i))
	}
	// Clear ExtraStar: an empty-struct realLayout carries it (struct {} shares name
	// bytes with *struct {}), which would make String() drop the reserved name's
	// first char.
	d.TFlag = (d.TFlag &^ tflagExtraStar) | keep
	// Re-register the real layout: reserveStruct shadowed d with the placeholder,
	// and layoutFor(reserved) sizes MapOf/ArrayOf buckets/strides.
	registerLayout(d, rtypePtr(realLayout))
}
