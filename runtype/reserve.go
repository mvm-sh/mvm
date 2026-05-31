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

// ReserveMethods reserves a method-bearing identity for layout without yet
// installing any method, mirroring AttachMethods' kind dispatch. Fill the
// returned Reservation once the method stubs are known.
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
	}
	return nil, ErrKindUnsupported
}

func reserveStruct(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiStructType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthStruct)
	b.st = *src
	stampHeader(&b.st.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, nil, uint32(moff))
	registerLayout(&b.st.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.st.abiType), u: &b.u, m: b.m[:]}, nil
}

func reservePrimitive(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := rtypePtr(layout)
	b := new(synthPrim)
	b.t = *src
	stampHeader(&b.t, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, nil, uint32(moff))
	registerLayout(&b.t, src)
	return &Reservation{rt: asReflectType(&b.t), u: &b.u, m: b.m[:]}, nil
}

func reserveSlice(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiSliceType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthSlice)
	b.t = *src
	stampHeader(&b.t.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, nil, uint32(moff))
	registerLayout(&b.t.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t.abiType), u: &b.u, m: b.m[:]}, nil
}

func reserveArray(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiArrayType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthArray)
	b.t = *src
	stampHeader(&b.t.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, nil, uint32(moff))
	registerLayout(&b.t.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t.abiType), u: &b.u, m: b.m[:]}, nil
}

func reserveMap(layout reflect.Type, name, pkgPath string) (*Reservation, error) {
	src := (*abiMapType)(unsafe.Pointer(rtypePtr(layout)))
	b := new(synthMap)
	b.t = *src
	stampHeader(&b.t.abiType, name)
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, nil, uint32(moff))
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
	b.u = makeUncommon(pkgPath, nil, uint32(moff))
	registerLayout(&b.t.abiType, &src.abiType)
	return &Reservation{rt: asReflectType(&b.t.abiType), u: &b.u, m: b.m[:]}, nil
}

// ReservePtrMethods reserves a *T identity carrying eventual ptr-receiver
// methods and wires elem.PtrToThis so reflect.PointerTo(elem) returns it, all
// before Fill. Mirrors AttachPtrMethods minus method installation.
func ReservePtrMethods(elem reflect.Type, name, pkgPath string) (*Reservation, error) {
	elemRT := rtypePtr(elem)
	if elemRT == nil {
		return nil, errNilElemType
	}
	b := new(synthPtr)
	b.elem = elemRT
	intPtrRT := rtypePtr(reflect.TypeOf((*int)(nil)))
	b.t = abiType{
		Size:       unsafe.Sizeof(uintptr(0)),
		PtrBytes:   unsafe.Sizeof(uintptr(0)),
		Hash:       nextSyntheticHash(),
		TFlag:      tflagUncommon | tflagNamed | tflagDirectIface,
		Align:      uint8(unsafe.Alignof(uintptr(0))),
		FieldAlign: uint8(unsafe.Alignof(uintptr(0))),
		Kind:       kindPointer,
		Equal:      intPtrRT.Equal,
		GCData:     intPtrRT.GCData,
		Str:        addReflectOff(unsafe.Pointer(encodeName(name, true).Bytes)),
		PtrToThis:  0,
	}
	moff := unsafe.Offsetof(b.m) - unsafe.Offsetof(b.u)
	b.u = makeUncommon(pkgPath, nil, uint32(moff))
	elemRT.PtrToThis = addReflectOff(unsafe.Pointer(&b.t))
	registerLayout(&b.t, intPtrRT)
	return &Reservation{rt: asReflectType(&b.t), u: &b.u, m: b.m[:]}, nil
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
	for i := uintptr(0); i < 40; i++ {
		*(*byte)(unsafe.Add(dp, i)) = *(*byte)(unsafe.Add(sp, i))
	}
	for i := uintptr(48); i < abiStructTypeSize; i++ {
		*(*byte)(unsafe.Add(dp, i)) = *(*byte)(unsafe.Add(sp, i))
	}
	d.TFlag |= keep
}
