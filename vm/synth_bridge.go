package vm

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/mvm-sh/mvm/vm/synth"
)

// AttachSynthMethods installs t's interpreted methods on a fresh synthesized
// rtype via vm/synth and replaces t.Rtype.
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
	derivedMu.Lock()
	defer derivedMu.Unlock()
	if t.synthIface != nil {
		return t.synthIface
	}
	ims := make([]synth.Imethod, 0, len(t.IfaceMethods))
	for _, im := range t.IfaceMethods {
		if im.Rtype == nil || im.Rtype.Kind() != reflect.Func {
			return nil
		}
		ims = append(ims, synth.Imethod{
			Name:     im.Name,
			Exported: isExportedName(im.Name),
			Sig:      im.Rtype,
		})
	}
	if len(ims) == 0 {
		return nil
	}
	st := synth.InterfaceOf(t.Rtype.String(), t.PkgPath, ims)
	t.synthIface = st
	return st
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
		reflect.Slice, reflect.Array, reflect.Map:
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
	newRT, err := synth.AttachMethods(t.Rtype, qualifiedTypeName(t), t.PkgPath,
		toSynthMethods(m, t, specs))
	if err != nil {
		return false, err
	}
	t.RefreshRtype(newRT)
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
	if !elemReady {
		clone, err := synth.Clone(t.Rtype, t.PkgPath)
		if err != nil {
			return err
		}
		t.RefreshRtype(clone)
	}
	newPtrRT, err := synth.AttachPtrMethods(t.Rtype, "*"+qualifiedTypeName(t), t.PkgPath,
		toSynthMethods(m, t, specs))
	if err != nil {
		return err
	}
	// Propagate the *T-with-methods rtype to t.derived.ptr. Materialize if
	// missing so a subsequent vm.PointerTo(t) returns this rtype rather than
	// building a fresh methodless *T via synth.PointerTo (which would diverge
	// from reflect.PointerTo(t.Rtype), the latter following PtrToThis to the
	// with-methods *T).
	derivedMu.Lock()
	d := t.ensureDerivedLocked()
	if d.ptr == nil {
		d.ptr = &Type{Name: t.Name, Rtype: newPtrRT, ElemType: t}
	} else {
		d.ptr.refreshLocked(newPtrRT)
	}
	derivedMu.Unlock()
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
	shape   synth.Shape
	ptrRecv bool
}

// toSynthMethods materializes the slice of synth.Method passed to
// synth.AttachMethods / AttachPtrMethods.
// Each method's handler closure is built per its shape, with recv dereferencing
// driven by the method's own receiver kind (s.ptrRecv).
func toSynthMethods(
	m *Machine, t *Type, specs []synthMethodSpec,
) []synth.Method {
	out := make([]synth.Method, len(specs))
	for i, s := range specs {
		var handler any
		switch s.shape {
		case synth.ShapeS1:
			handler = makeHandlerS1(m, t, s.method, s.ptrRecv)
		case synth.ShapeS2:
			handler = makeHandlerS2(m, t, s.method, s.ptrRecv)
		case synth.ShapeS3:
			handler = makeHandlerS3(m, t, s.method, s.ptrRecv)
		case synth.ShapeS4:
			handler = makeHandlerS4(m, t, s.method, s.ptrRecv)
		case synth.ShapeS5:
			handler = makeHandlerS5(m, t, s.method, s.ptrRecv)
		case synth.ShapeS6:
			handler = makeHandlerS6(m, t, s.method, s.ptrRecv)
		case synth.ShapeS7:
			handler = makeHandlerS7(m, t, s.method, s.ptrRecv)
		case synth.ShapeS8:
			handler = makeHandlerS8(m, t, s.method, s.ptrRecv)
		case synth.ShapeS9:
			handler = makeHandlerS9(m, t, s.method, s.ptrRecv)
		case synth.ShapeS10:
			handler = makeHandlerS10(m, t, s.method, s.ptrRecv)
		case synth.ShapeS11:
			handler = makeHandlerS11(m, t, s.method, s.ptrRecv)
		case synth.ShapeS12:
			handler = makeHandlerS12(m, t, s.method, s.ptrRecv)
		case synth.ShapeS13:
			handler = makeHandlerS13(m, t, s.method, s.ptrRecv)
		}
		out[i] = synth.Method{
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
	const synthMaxMethods = 16 // matches vm/synth.maxMethods
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
// matching synth.Shape if any.
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
func detectShape(sig reflect.Type) (synth.Shape, bool) {
	if sig == nil || sig.Kind() != reflect.Func {
		return 0, false
	}
	nin, nout := sig.NumIn(), sig.NumOut()
	switch {
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.String:
		return synth.ShapeS1, true
	case nin == 0 && nout == 1 && isErrorType(sig.Out(0)):
		return synth.ShapeS6, true
	case nin == 0 && nout == 1 && isErrorSlice(sig.Out(0)):
		return synth.ShapeS7, true
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.Int:
		return synth.ShapeS8, true
	case nin == 0 && nout == 1 && isAnyType(sig.Out(0)):
		return synth.ShapeS12, true
	case nin == 0 && nout == 2 &&
		isByteSlice(sig.Out(0)) && isErrorType(sig.Out(1)):
		return synth.ShapeS2, true
	case nin == 1 && nout == 1 &&
		isByteSlice(sig.In(0)) && isErrorType(sig.Out(0)):
		return synth.ShapeS3, true
	case nin == 1 && nout == 1 &&
		isErrorType(sig.In(0)) && sig.Out(0).Kind() == reflect.Bool:
		return synth.ShapeS4, true
	case nin == 1 && nout == 1 &&
		isAnyType(sig.In(0)) && sig.Out(0).Kind() == reflect.Bool:
		return synth.ShapeS5, true
	case nin == 1 && nout == 0 && isAnyType(sig.In(0)):
		return synth.ShapeS11, true
	case nin == 1 && nout == 2 && isByteSlice(sig.In(0)) &&
		sig.Out(0).Kind() == reflect.Int && isErrorType(sig.Out(1)):
		return synth.ShapeS13, true
	case nin == 2 && nout == 1 &&
		sig.In(0).Kind() == reflect.Int && sig.In(1).Kind() == reflect.Int &&
		sig.Out(0).Kind() == reflect.Bool:
		return synth.ShapeS9, true
	case nin == 2 && nout == 0 &&
		sig.In(0).Kind() == reflect.Int && sig.In(1).Kind() == reflect.Int:
		return synth.ShapeS10, true
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
	errorIface     = reflect.TypeOf((*error)(nil)).Elem()
	byteSliceType  = reflect.TypeOf([]byte(nil))
	anyIface       = reflect.TypeOf((*any)(nil)).Elem()
	errorSliceType = reflect.TypeOf([]error(nil))
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
func makeHandlerS1(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS1 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) string {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, rv, method, methodSig, nil)
		if err != nil {
			return fmt.Sprintf("<synth dispatch error: %v>", err)
		}
		if len(out) != 1 {
			return ""
		}
		return out[0].String()
	}
}

func makeHandlerS2(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS2 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) ([]byte, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, rv, method, methodSig, nil)
		if err != nil {
			return nil, err
		}
		if len(out) != 2 {
			return nil, errors.New("synth: S2 dispatch produced wrong arity")
		}
		var data []byte
		if !out[0].IsNil() {
			data = out[0].Bytes()
		}
		var rerr error
		if !out[1].IsNil() {
			rerr, _ = out[1].Interface().(error)
		}
		return data, rerr
	}
}

func makeHandlerS3(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS3 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, data []byte) error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(data)}
		out, err := callMethod(m, t, rv, method, methodSig, argv)
		if err != nil {
			return err
		}
		if len(out) != 1 {
			return errors.New("synth: S3 dispatch produced wrong arity")
		}
		if out[0].IsNil() {
			return nil
		}
		rerr, _ := out[0].Interface().(error)
		return rerr
	}
}

// makeHandlerS4 bridges shape S4: (T).Is(target error) bool.
// target is passed through its static error type (reflect.ValueOf(&target)
// .Elem() stays valid and interface-typed even when target is nil).
func makeHandlerS4(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS4 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, target error) bool {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(&target).Elem()}
		out, err := callMethod(m, t, rv, method, methodSig, argv)
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
func makeHandlerS5(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS5 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, target any) bool {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(&target).Elem()}
		out, err := callMethod(m, t, rv, method, methodSig, argv)
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
func makeHandlerS6(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS6 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return nil
		}
		return reflectToError(out[0])
	}
}

// makeHandlerS7 bridges shape S7: (T).Unwrap() []error.
func makeHandlerS7(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS7 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) []error {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return nil
		}
		return reflectToErrorSlice(out[0])
	}
}

// makeHandlerS8 bridges shape S8: (T).Len() int.
func makeHandlerS8(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS8 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) int {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return 0
		}
		return int(out[0].Int())
	}
}

// makeHandlerS9 bridges shape S9: (T).Less(i, j int) bool.
func makeHandlerS9(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS9 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, i, j int) bool {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(i), reflect.ValueOf(j)}
		out, err := callMethod(m, t, rv, method, methodSig, argv)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

// makeHandlerS10 bridges shape S10: (T).Swap(i, j int).
func makeHandlerS10(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS10 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, i, j int) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(i), reflect.ValueOf(j)}
		_, _ = callMethod(m, t, rv, method, methodSig, argv)
	}
}

// makeHandlerS11 bridges shape S11: (T).Push(x any).
// x is passed through reflect.ValueOf(&x).Elem() so it stays interface-typed.
func makeHandlerS11(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS11 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, x any) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(&x).Elem()}
		_, _ = callMethod(m, t, rv, method, methodSig, argv)
	}
}

// makeHandlerS12 bridges shape S12: (T).Pop() any.
func makeHandlerS12(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS12 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) any {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		out, err := callMethod(m, t, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 || !out[0].IsValid() {
			return nil
		}
		return Exportable(out[0]).Interface()
	}
}

// makeHandlerS13 bridges shape S13: (T).Read/Write(p []byte) (int, error).
func makeHandlerS13(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS13 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, p []byte) (int, error) {
		rv := makeRecvValue(t.Rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(p)}
		out, err := callMethod(m, t, rv, method, methodSig, argv)
		if err != nil {
			return 0, err
		}
		if len(out) != 2 {
			return 0, errors.New("synth: S13 dispatch produced wrong arity")
		}
		return int(out[0].Int()), reflectToError(out[1])
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
	m *Machine, ifcType *Type, rv reflect.Value,
	method Method, methodSig reflect.Type, args []reflect.Value,
) ([]reflect.Value, error) {
	ifc := Iface{Typ: ifcType, Val: FromReflect(rv)}
	fval := m.MakeMethodCallable(ifc, method)
	return m.CallFunc(fval, methodSig, args)
}
