package vm

import (
	"encoding/xml"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/mvm-sh/mvm/runtype"
	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// AttachSynthMethods installs t's interpreted methods on a fresh synthesized
// rtype (via runtype + stdlib/stubs) and replaces t.Rtype.
// Native code that asserts the new rtype to an interface (fmt.Stringer,
// error, json.Marshaler, json.Unmarshaler, etc.) then dispatches the
// method directly, with no bridge proxy.
//
// Phase 2d: any combination of shapes S1 (func() string) / S2 (func()
// ([]byte, error)) / S3 (func([]byte) error) on any supported kind, plus
// pointer-receiver variants on *T via attachPtrType.
// Up to synth's per-attach method cap (currently 16); excess methods of
// the same receiver kind are silently dropped.
//
// Re-allocation of existing values is out of scope: global slots populated
// before this call keep their old rtype.
// New values allocated via vm.NewValue against t.Rtype after this call see
// the synth rtype.
func (m *Machine) AttachSynthMethods(t *Type) error {
	if t == nil || t.Rtype == nil {
		return nil
	}
	if !synthSupportedKind(t.Rtype.Kind()) {
		return nil
	}
	// An unnamed type carries only promoted methods (Go forbids methods on an
	// anonymous type, e.g. struct{io.Reader}); those dispatch through the embedded
	// field's own rtype, so the container needs no synth attach.
	if t.Name == "" {
		return nil
	}

	valueAttached, err := m.attachValueRecv(t)
	if err != nil {
		return err
	}
	return m.attachPtrRecv(t, valueAttached)
}

// bridgePtrToIface retypes a bridged pointer-to-interpreted-interface (e.g.
// &timeout) to *synthIface so the callee sees the real method set, not a
// methodless any; without it errors.As treats the target as matching every error.
// Returns an invalid Value (the common case) to fall through to the default bridge.
//
// Gated to an allowlist (fn), because mvm stores interfaces as eface (type+data)
// while a non-empty interface is iface (itab+data): relabeling is only safe for
// callees that use the pointer as a type-tagged out-param (errors.As) or merely
// describe it (reflect.ValueOf/TypeOf, used to read the result back). A callee
// that reads the pointee as an iface (gob, json) would misread eface bytes.
// val (= ifc.Val.Reflect()) shares storage with the result, so a callee write is
// visible to a later read through the same mvm pointer.
func (m *Machine) bridgePtrToIface(ifc Iface, val, fn reflect.Value) reflect.Value {
	if ifc.Typ == nil || ifc.Typ.Rtype == nil || ifc.Typ.Rtype.Kind() != reflect.Pointer {
		return reflect.Value{}
	}
	et := ifc.Typ.ElemType
	if et == nil || et.Rtype == nil || et.Rtype.Kind() != reflect.Interface ||
		len(et.IfaceMethods) == 0 {
		return reflect.Value{}
	}
	if !val.IsValid() || val.Kind() != reflect.Pointer || val.IsNil() {
		return reflect.Value{}
	}
	if !isSynthIfaceTargetFunc(fn) {
		return reflect.Value{}
	}
	st := synthIfaceRtype(et)
	if st == nil {
		return reflect.Value{}
	}
	return reflect.NewAt(st, val.UnsafePointer())
}

// synthIfaceTargetPCs is the set of native-func PCs whose pointer-to-interface
// argument may be retyped (see bridgePtrToIface); init-write, concurrent-read.
var synthIfaceTargetPCs sync.Map // uintptr -> struct{}

// RegisterSynthIfaceTargetFunc allowlists fn for synth-interface target
// retyping. Call from package init.
func RegisterSynthIfaceTargetFunc(fn reflect.Value) {
	if fn.IsValid() && fn.Kind() == reflect.Func {
		synthIfaceTargetPCs.Store(fn.Pointer(), struct{}{})
	}
}

func isSynthIfaceTargetFunc(fn reflect.Value) bool {
	if !fn.IsValid() || fn.Kind() != reflect.Func {
		return false
	}
	_, ok := synthIfaceTargetPCs.Load(fn.Pointer())
	return ok
}

// synthIfaceRtype builds and caches t's method-bearing synth interface rtype.
// Returns nil if any method signature is unknown, keeping the AnyRtype bridge.
func synthIfaceRtype(t *Type) reflect.Type {
	return cachedSynthIface(t, func() reflect.Type {
		ims := make([]runtype.Imethod, 0, len(t.IfaceMethods))
		for _, im := range t.IfaceMethods {
			if im.Rtype == nil || im.Rtype.Kind() != reflect.Func {
				return nil
			}
			ims = append(ims, runtype.Imethod{
				Name:     im.Name,
				Exported: isExportedName(im.Name),
				Sig:      im.Rtype,
			})
		}
		if len(ims) == 0 {
			return nil
		}
		return runtype.InterfaceOf(t.Rtype.String(), t.PkgPath, ims)
	})
}

func isExportedName(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

func synthSupportedKind(k reflect.Kind) bool {
	switch k {
	case reflect.Struct,
		reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String,
		reflect.Slice, reflect.Array, reflect.Map, reflect.Func:
		return true
	}
	return false
}

// qualifiedTypeName returns the Str-form name stamped into the synth rtype:
// "pkgBase.Name" when the type has a package path, otherwise just Name.
// reflect.Type.String() returns this verbatim, so fmt %T and similar matches
// what the Go compiler emits for native types.
// Name() strips back to the last "." to recover the bare type name.
func qualifiedTypeName(t *Type) string {
	if t.PkgPath == "" || t.Name == "" {
		return t.Name
	}
	base := t.PkgPath
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base + "." + t.Name
}

func (m *Machine) attachValueRecv(t *Type) (bool, error) {
	specs := m.allSynthMethods(t, false)
	if len(specs) == 0 {
		return false, nil
	}
	methods := toSynthMethods(m, t, specs)
	// Reserve/fill path: t.Rtype is already the reserved synth identity; fill
	// methods in place so composites that captured it need no patching.
	if res := lookupReservation(t); res != nil && res.value != nil {
		if err := stubs.FillMethods(res.value, methods); err != nil {
			return false, err
		}
		return true, nil
	}
	// Fallback swap+cascade: reserve cannot yet reach two timing cases (an
	// imported method-bearing type materialized eager during import, and a
	// defined type over a named base, type Y X).
	newRT, err := stubs.AttachMethods(t.Rtype, qualifiedTypeName(t), t.PkgPath, methods)
	if err != nil {
		return false, err
	}
	cascadeHit("RefreshRtype-value", t)
	RefreshRtype(t, newRT)
	return true, nil
}

// attachPtrRecv installs ptr-recv methods on *T.
// elemReady reports whether t.Rtype is already a fresh synth elem we own.
// If not, clone the original layout first so attachPtrType writes
// PtrToThis into our own rtype rather than the layout shared with reflect's
// caches (structLookupCache for struct, the canonical native rtype for
// primitives/slices/arrays/maps).
func (m *Machine) attachPtrRecv(t *Type, elemReady bool) error {
	specs := m.allSynthMethods(t, true)
	if len(specs) == 0 {
		return nil
	}
	methods := toSynthMethods(m, t, specs)
	// Reserve/fill path: *T was reserved + wired (PtrToThis, AttachPtrDerived) at
	// materialize; just fill its methods in place.
	if res := lookupReservation(t); res != nil && res.ptr != nil {
		return stubs.FillMethods(res.ptr, methods)
	}
	// Fallback swap+cascade for the residual reserve-timing cases above.
	if !elemReady {
		clone, err := runtype.Clone(t.Rtype, t.PkgPath)
		if err != nil {
			return err
		}
		cascadeHit("RefreshRtype-ptrclone", t)
		RefreshRtype(t, clone)
	}
	newPtrRT, err := stubs.AttachPtrMethods(t.Rtype, "*"+qualifiedTypeName(t), t.PkgPath, methods)
	if err != nil {
		return err
	}
	// Propagate the *T-with-methods rtype to t's derived pointer so a later
	// PointerTo(t) returns it rather than a fresh methodless *T.
	AttachPtrDerived(t, newPtrRT)
	return nil
}

// synthMethodSpec describes a single method picked for synth attachment.
// shape is the matched signature shape; name comes from MethodNames.
// ptrRecv is the method's own receiver kind, which drives recv dereferencing
// in the handler: a value-receiver method promoted onto *T must deref recv to
// T, while a pointer-receiver method keeps the *T.
type synthMethodSpec struct {
	name    string
	method  Method
	shape   stubs.Shape
	ptrRecv bool
}

// toSynthMethods materializes the slice of stubs.Method passed to
// stubs.AttachMethods / AttachPtrMethods.
// Each method's handler closure is built per its shape, with recv dereferencing
// driven by the method's own receiver kind (s.ptrRecv).
func toSynthMethods(
	m *Machine, t *Type, specs []synthMethodSpec,
) []stubs.Method {
	out := make([]stubs.Method, len(specs))
	for i, s := range specs {
		var handler any
		switch s.shape {
		case stubs.ShapeS1:
			handler = makeHandlerS1(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS2:
			handler = makeHandlerS2(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS3:
			handler = makeHandlerS3(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS4:
			handler = makeHandlerS4(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS5:
			handler = makeHandlerS5(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS6:
			handler = makeHandlerS6(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS7:
			handler = makeHandlerS7(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS8:
			handler = makeHandlerS8(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS9:
			handler = makeHandlerS9(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS10:
			handler = makeHandlerS10(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS11:
			handler = makeHandlerS11(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS12:
			handler = makeHandlerS12(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS13:
			handler = makeHandlerS13(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS14:
			handler = makeHandlerS14(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS15:
			handler = makeHandlerS15(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS16:
			handler = makeHandlerS16(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS17:
			handler = makeHandlerS17(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS18:
			handler = makeHandlerS18(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS19:
			handler = makeHandlerS19(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS20:
			handler = makeHandlerS20(m, t, s.method, s.name, s.ptrRecv)
		case stubs.ShapeS21:
			handler = makeHandlerS21(m, t, s.method, s.name, s.ptrRecv)
		}
		out[i] = stubs.Method{
			Name:     s.name,
			Exported: true,
			Sig:      s.method.Rtype,
			Shape:    s.shape,
			Handler:  handler,
		}
	}
	return out
}

// allSynthMethods returns the resolved, shape-matching methods to install on
// one synth rtype. includePtr selects which method set is built:
//   - value rtype T (includePtr=false): value-receiver methods only.
//   - pointer rtype *T (includePtr=true): value + pointer receiver methods,
//     matching Go's rule that method-set(*T) = value methods + ptr methods.
//     Without the value methods here, *T fails native interface satisfaction
//     (e.g. *errorUncomparable would not satisfy error though Error() is
//     value-receiver).
//
// Iteration follows t.Methods order (declaration order); the result is
// truncated to synth's per-attach cap to avoid attach failure on rare
// large method sets.
// Name filtering is intentionally absent: which method names matter is a
// stdlib-layer concern, not a vm concern.
func (m *Machine) allSynthMethods(
	t *Type, includePtr bool,
) []synthMethodSpec {
	const synthMaxMethods = 16 // matches runtype.maxMethods
	var specs []synthMethodSpec
	for i, method := range t.Methods {
		if !method.IsResolved() || i >= len(m.MethodNames) {
			continue
		}
		if method.PtrRecv && !includePtr {
			continue
		}
		shape, ok := detectShape(method.Rtype)
		if !ok {
			continue
		}
		specs = append(specs, synthMethodSpec{
			name:    m.MethodNames[i],
			method:  method,
			shape:   shape,
			ptrRecv: method.PtrRecv,
		})
		if len(specs) == synthMaxMethods {
			break
		}
	}
	return specs
}

// detectShape inspects a method signature (receiver elided) and returns the
// matching stubs.Shape if any.
// Recognized shapes:
//
//	S1: func() string
//	S2: func() ([]byte, error)
//	S3: func([]byte) error
//	S4:  func(error) bool       (errors.Is)
//	S5:  func(any) bool          (errors.As)
//	S6:  func() error            (single-error Unwrap)
//	S7:  func() []error          (multi-error Unwrap)
//	S8:  func() int              (sort.Interface.Len)
//	S9:  func(int, int) bool     (sort.Interface.Less)
//	S10: func(int, int)          (sort.Interface.Swap)
//	S11: func(any)               (heap.Interface.Push)
//	S12: func() any              (heap.Interface.Pop)
//	S13: func([]byte) (int, error) (io.Reader.Read / io.Writer.Write)
//	S14: func(fmt.State, rune)    (fmt.Formatter.Format)
func detectShape(sig reflect.Type) (stubs.Shape, bool) {
	if sig == nil || sig.Kind() != reflect.Func {
		return 0, false
	}
	nin, nout := sig.NumIn(), sig.NumOut()
	switch {
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.String:
		return stubs.ShapeS1, true
	case nin == 0 && nout == 1 && isErrorType(sig.Out(0)):
		return stubs.ShapeS6, true
	case nin == 0 && nout == 1 && isErrorSlice(sig.Out(0)):
		return stubs.ShapeS7, true
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.Int:
		return stubs.ShapeS8, true
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS21, true
	case nin == 0 && nout == 1 && isAnyType(sig.Out(0)):
		return stubs.ShapeS12, true
	case nin == 0 && nout == 2 &&
		isByteSlice(sig.Out(0)) && isErrorType(sig.Out(1)):
		return stubs.ShapeS2, true
	case nin == 0 && nout == 2 &&
		sig.Out(0).Kind() == reflect.Int && sig.Out(1).Kind() == reflect.Bool:
		return stubs.ShapeS17, true
	case nin == 1 && nout == 1 &&
		sig.In(0).Kind() == reflect.Int && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS18, true
	case nin == 1 && nout == 1 &&
		isByteSlice(sig.In(0)) && isErrorType(sig.Out(0)):
		return stubs.ShapeS3, true
	case nin == 1 && nout == 1 &&
		sig.In(0).Kind() == reflect.String && isErrorType(sig.Out(0)):
		return stubs.ShapeS20, true
	case nin == 1 && nout == 1 &&
		isErrorType(sig.In(0)) && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS4, true
	case nin == 1 && nout == 1 &&
		isAnyType(sig.In(0)) && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS5, true
	case nin == 1 && nout == 0 && isAnyType(sig.In(0)):
		return stubs.ShapeS11, true
	case nin == 1 && nout == 2 && isByteSlice(sig.In(0)) &&
		sig.Out(0).Kind() == reflect.Int && isErrorType(sig.Out(1)):
		return stubs.ShapeS13, true
	case nin == 2 && nout == 1 &&
		sig.In(0).Kind() == reflect.Int && sig.In(1).Kind() == reflect.Int &&
		sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS9, true
	case nin == 2 && nout == 0 &&
		sig.In(0).Kind() == reflect.Int && sig.In(1).Kind() == reflect.Int:
		return stubs.ShapeS10, true
	case nin == 2 && nout == 0 &&
		sig.In(0) == fmtStateIface && sig.In(1).Kind() == reflect.Int32:
		return stubs.ShapeS14, true
	case nin == 2 && nout == 1 && sig.In(0) == xmlEncoderPtr &&
		sig.In(1) == xmlStartElem && isErrorType(sig.Out(0)):
		return stubs.ShapeS15, true
	case nin == 2 && nout == 1 && sig.In(0) == xmlDecoderPtr &&
		sig.In(1) == xmlStartElem && isErrorType(sig.Out(0)):
		return stubs.ShapeS16, true
	case nin == 2 && nout == 1 && sig.In(0) == fmtScanStateIface &&
		sig.In(1).Kind() == reflect.Int32 && isErrorType(sig.Out(0)):
		return stubs.ShapeS19, true
	}
	return 0, false
}

// Identity-based predicates: a named alias like `type MyBytes []byte` or
// `type Failure interface{ Error() string }` is structurally compatible but
// has a distinct reflect.Type identity.
// Native iface satisfaction (json.Marshaler etc.) keys on exact type
// identity, so accepting aliases here would burn slot-pool entries on
// types that never satisfy the target interface.
var (
	errorIface        = reflect.TypeOf((*error)(nil)).Elem()
	byteSliceType     = reflect.TypeOf([]byte(nil))
	anyIface          = reflect.TypeOf((*any)(nil)).Elem()
	errorSliceType    = reflect.TypeOf([]error(nil))
	fmtStateIface     = reflect.TypeOf((*fmt.State)(nil)).Elem()
	fmtScanStateIface = reflect.TypeOf((*fmt.ScanState)(nil)).Elem()
	xmlEncoderPtr     = reflect.TypeOf((*xml.Encoder)(nil))
	xmlDecoderPtr     = reflect.TypeOf((*xml.Decoder)(nil))
	xmlStartElem      = reflect.TypeOf(xml.StartElement{})
)

func isByteSlice(t reflect.Type) bool { return t == byteSliceType }

func isErrorType(t reflect.Type) bool { return t == errorIface }

// isAnyType matches the empty interface exactly (errors.As targets `any`),
// distinguishing S5 from S4 whose param is the one-method `error` interface.
func isAnyType(t reflect.Type) bool { return t == anyIface }

func isErrorSlice(t reflect.Type) bool { return t == errorSliceType }

// makeHandlerS1 builds the per-method bridge closure for shape S1.
// For ptrRecv methods, recv from the stub IS the *T pointer (direct-iface);
// the receiver Value is reflect.NewAt(t.Rtype, recv).
// For value-recv methods, recv points at boxed T storage; the receiver Value
// is reflect.NewAt(t.Rtype, recv).Elem().
//
// t.Rtype is read lazily inside the closure: the bridge replaces it with the
// synth rtype after AttachMethods returns, so capturing it at construction
// time would freeze the pre-synth layout identity and produce mismatched
// reflect.Value vs ifcType.Rtype at dispatch.
func makeHandlerS1(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS1 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) string {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil {
			raiseMethodErr(err)
		}
		if len(out) != 1 {
			return ""
		}
		return out[0].String()
	}
}

// raiseMethodErr re-raises a failed synth dispatch as a Go panic so a native
// caller's recover handles it (e.g. fmt's catchPanic -> "%!s(PANIC=...)", or its
// nil-pointer-receiver special case -> "<nil>"). An interpreted-method panic
// (surfaced as a *PanicError) is re-raised with its original value; any other
// dispatch error (e.g. a reflect error from a nil receiver) is re-raised as is.
// Calling a method that fails always panics in Go, so this never returns.
func raiseMethodErr(err error) {
	var pe *PanicError
	if errors.As(err, &pe) {
		panic(pe.Raw)
	}
	panic(err)
}

func makeHandlerS2(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS2 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) ([]byte, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S2 dispatch produced wrong arity")
		}
		var data []byte
		if out[0].IsValid() && (out[0].Kind() != reflect.Slice || !out[0].IsNil()) {
			data = out[0].Bytes()
		}
		return data, reflectToError(out[1])
	}
}

func makeHandlerS3(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS3 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, data []byte) error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(data)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			return err
		}
		if len(out) != 1 {
			return errors.New("synth: S3 dispatch produced wrong arity")
		}
		// reflectToError, not bare IsNil: the return may be a concrete struct error.
		return reflectToError(out[0])
	}
}

// makeHandlerS4 bridges shape S4: (T).Is(target error) bool.
// target is passed through its static error type (reflect.ValueOf(&target)
// .Elem() stays valid and interface-typed even when target is nil).
func makeHandlerS4(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS4 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, target error) bool {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(&target).Elem()}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		// A dispatch error (incl. an interpreted-method panic surfaced as a
		// *PanicError) is swallowed to false: re-panicking it back through the
		// native caller crashes the nested-panic-across-native-boundary path
		// (machine stack left inconsistent on unwind). See the skipped
		// interp.TestStruct errors_is_panic_propagates.
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

// makeHandlerS5 bridges shape S5: (T).As(target any) bool.
// target boxes the *E pointer errors.As wants populated; passing it through
// lets the interpreted As write back into the caller's storage.
func makeHandlerS5(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS5 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, target any) bool {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(&target).Elem()}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		// A dispatch error (incl. an interpreted-method panic surfaced as a
		// *PanicError) is swallowed to false: re-panicking it back through the
		// native caller crashes the nested-panic-across-native-boundary path
		// (machine stack left inconsistent on unwind). See the skipped
		// interp.TestStruct errors_is_panic_propagates.
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

// makeHandlerS6 bridges shape S6: (T).Unwrap() error.
func makeHandlerS6(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS6 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return nil
		}
		return reflectToError(out[0])
	}
}

// makeHandlerS7 bridges shape S7: (T).Unwrap() []error.
func makeHandlerS7(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS7 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) []error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return nil
		}
		return reflectToErrorSlice(out[0])
	}
}

// makeHandlerS8 bridges shape S8: (T).Len() int.
func makeHandlerS8(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS8 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) int {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return 0
		}
		return int(out[0].Int())
	}
}

// makeHandlerS9 bridges shape S9: (T).Less(i, j int) bool.
func makeHandlerS9(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS9 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, i, j int) bool {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(i), reflect.ValueOf(j)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

// makeHandlerS10 bridges shape S10: (T).Swap(i, j int).
func makeHandlerS10(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS10 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, i, j int) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(i), reflect.ValueOf(j)}
		_, _ = callMethod(m, t, name, rv, method, methodSig, argv)
	}
}

// makeHandlerS11 bridges shape S11: (T).Push(x any).
// x is passed through reflect.ValueOf(&x).Elem() so it stays interface-typed.
func makeHandlerS11(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS11 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, x any) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(&x).Elem()}
		_, _ = callMethod(m, t, name, rv, method, methodSig, argv)
	}
}

// makeHandlerS12 bridges shape S12: (T).Pop() any.
func makeHandlerS12(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS12 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) any {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 || !out[0].IsValid() {
			return nil
		}
		return Exportable(out[0]).Interface()
	}
}

// makeHandlerS13 bridges shape S13: (T).Read/Write(p []byte) (int, error).
func makeHandlerS13(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS13 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, p []byte) (int, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(p)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			// An interpreted-method panic must propagate through the native caller
			// (e.g. bytes.Buffer.ReadFrom) to an outer recover(), as in Go;
			// invokeNative's recover re-establishes it as an mvm panic.
			var pe *PanicError
			if errors.As(err, &pe) {
				panic(pe)
			}
			return 0, err
		}
		if len(out) != 2 {
			return 0, errors.New("synth: S13 dispatch produced wrong arity")
		}
		return int(out[0].Int()), reflectToError(out[1])
	}
}

// makeHandlerS14 bridges shape S14: (T).Format(fmt.State, rune).
// st is passed through reflect.ValueOf(&st).Elem() so it keeps its fmt.State
// type, letting the interpreted body call State methods on it.
func makeHandlerS14(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS14 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, st fmt.State, verb rune) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(&st).Elem(), reflect.ValueOf(verb)}
		_, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			raiseMethodErr(err)
		}
	}
}

// makeHandlerS15 bridges shape S15: (T).MarshalXML(*xml.Encoder, xml.StartElement) error.
func makeHandlerS15(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS15 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, e *xml.Encoder, start xml.StartElement) error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(e), reflect.ValueOf(start)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			return err
		}
		if len(out) != 1 {
			return errors.New("synth: S15 dispatch produced wrong arity")
		}
		return reflectToError(out[0])
	}
}

// makeHandlerS16 bridges shape S16: (T).UnmarshalXML(*xml.Decoder, xml.StartElement) error.
func makeHandlerS16(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS16 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, d *xml.Decoder, start xml.StartElement) error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(d), reflect.ValueOf(start)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			return err
		}
		if len(out) != 1 {
			return errors.New("synth: S16 dispatch produced wrong arity")
		}
		return reflectToError(out[0])
	}
}

// makeHandlerS17 bridges shape S17: (T).Width/Precision() (int, bool).
func makeHandlerS17(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS17 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) (int, bool) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 2 {
			return 0, false
		}
		return int(out[0].Int()), out[1].Bool()
	}
}

// makeHandlerS18 bridges shape S18: (T).Flag(c int) bool.
func makeHandlerS18(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS18 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, c int) bool {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(c)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

// makeHandlerS19 bridges shape S19: (T).Scan(fmt.ScanState, rune) error.
// st is passed through reflect.ValueOf(&st).Elem() so it keeps its
// fmt.ScanState type, letting the interpreted body call ScanState methods on it.
func makeHandlerS19(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS19 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, st fmt.ScanState, verb rune) error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(&st).Elem(), reflect.ValueOf(verb)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			return err
		}
		if len(out) != 1 {
			return errors.New("synth: S19 dispatch produced wrong arity")
		}
		return reflectToError(out[0])
	}
}

// makeHandlerS20 bridges shape S20: (T).Set(string) error.
func makeHandlerS20(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS20 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, value string) error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(value)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			return err
		}
		if len(out) != 1 {
			return errors.New("synth: S20 dispatch produced wrong arity")
		}
		return reflectToError(out[0])
	}
}

// makeHandlerS21 bridges shape S21: (T).IsBoolFlag() bool.
func makeHandlerS21(m *Machine, t *Type, method Method, name string, ptrRecv bool) stubs.HandlerS21 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) bool {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

// reflectToError extracts a native error from a method-return Value, tolerating
// both an interface-typed result and a concrete (struct/ptr) error that
// collectReturns left unboxed.
func reflectToError(v reflect.Value) error {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Slice, reflect.Map,
		reflect.Chan, reflect.Func:
		if v.IsNil() {
			return nil
		}
	}
	rerr, _ := Exportable(v).Interface().(error)
	return rerr
}

// reflectToErrorSlice extracts []error from a multi-unwrap return, tolerating a
// named slice type (e.g. `type multiErr []error`) that collectReturns left as
// its own concrete type instead of []error.
func reflectToErrorSlice(v reflect.Value) []error {
	v = Exportable(v)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.IsNil() {
		return nil
	}
	if res, ok := v.Interface().([]error); ok {
		return res
	}
	res := make([]error, v.Len())
	for i := range res {
		res[i] = reflectToError(v.Index(i))
	}
	return res
}

func makeRecvValue(rtype reflect.Type, recv unsafe.Pointer, ptrRecv bool) reflect.Value {
	if ptrRecv {
		return reflect.NewAt(rtype, recv)
	}
	return reflect.NewAt(rtype, recv).Elem()
}

func callMethod(
	m *Machine, ifcType *Type, name string, rv reflect.Value,
	method Method, methodSig reflect.Type, args []reflect.Value,
) ([]reflect.Value, error) {
	ifc := Iface{Typ: ifcType, Val: FromReflect(rv)}
	if method.EmbedIface {
		return m.callEmbedIface(ifc, method, name, methodSig, args)
	}
	fval := m.MakeMethodCallable(ifc, method)
	return m.CallFunc(fval, methodSig, args)
}

// callEmbedIface dispatches a method promoted from an embedded interface field
// (Method.EmbedIface, Index == -1, so makeMethodCell can't build a cell).
// It walks the embedded chain like the IfaceCall path (see the Run loop): for
// each EmbedIface hop, navigate Path to the embedded value; a native embedded
// value (e.g. `struct{ error }` holding a native error) is dispatched by name
// via reflect, an interpreted one recurses to its concrete method.
func (m *Machine) callEmbedIface(
	ifc Iface, method Method, name string, methodSig reflect.Type, args []reflect.Value,
) ([]reflect.Value, error) {
	methodID := -1
	for id, n := range m.MethodNames {
		if n == name {
			methodID = id
			break
		}
	}
	for method.EmbedIface {
		rv := ifc.Val.Reflect()
		if rv.Kind() == reflect.Pointer {
			rv = rv.Elem()
		}
		for _, fi := range method.Path {
			rv = rv.Field(fi)
		}
		// Embedded fields are often unexported (named after the type), so the
		// navigated value carries reflect's read-only flag; clear it before
		// dispatch or reflect.Value.Call panics.
		rv = Exportable(rv)
		embedded := FromReflect(rv)
		if !embedded.IsIface() {
			if isNilReceiver(rv) {
				return nil, errors.New("synth: nil embedded receiver")
			}
			mv := nativeMethodLookup(m, rv, name)
			if !mv.IsValid() {
				return nil, fmt.Errorf("synth: embedded method %q not found", name)
			}
			return mv.Call(args), nil
		}
		ifc = embedded.IfaceVal()
		if methodID < 0 || methodID >= len(ifc.Typ.Methods) {
			return nil, fmt.Errorf("synth: embedded method %q unresolved", name)
		}
		method = ifc.Typ.Methods[methodID]
	}
	return m.CallFunc(m.MakeMethodCallable(ifc, method), methodSig, args)
}
