package vm

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"github.com/mvm-sh/mvm/internal/derive"
	"github.com/mvm-sh/mvm/internal/runtype"
	"github.com/mvm-sh/mvm/internal/stubs"
	"github.com/mvm-sh/mvm/internal/wordabi"
	"github.com/mvm-sh/mvm/mtype"
)

// AttachSynthMethods fills t's interpreted methods into the synth rtype that was
// reserved for t at materialize.
// Native code that asserts the rtype to an interface dispatches the method directly.
//
// Up to synth's per-attach method cap (runtype.MaxMethods).
// Excess methods of the same receiver kind are silently dropped.
func (m *Machine) AttachSynthMethods(t *mtype.Type) error {
	if t == nil || t.Rtype == nil {
		return nil
	}
	if !runtype.SupportedKind(t.Rtype.Kind()) {
		return nil
	}
	// An unnamed type carries only promoted methods (Go forbids methods on an
	// anonymous type, e.g. struct{io.Reader}); those dispatch through the embedded
	// field's own rtype, so the container needs no synth attach.
	if t.Name == "" {
		return nil
	}

	if err := m.attachValueRecv(t); err != nil {
		return err
	}
	return m.attachPtrRecv(t)
}

func (m *Machine) bridgePtrToIface(ifc Iface, val, fn reflect.Value) reflect.Value {
	if ifc.Typ == nil || ifc.Typ.Rtype == nil || ifc.Typ.Rtype.Kind() != reflect.Pointer {
		return reflect.Value{}
	}
	et := ifc.Typ.ElemType
	if et == nil || et.Rtype == nil || et.Rtype.Kind() != reflect.Interface ||
		len(et.IfaceMethods) == 0 {
		return reflect.Value{}
	}
	if !val.IsValid() || val.Kind() != reflect.Pointer {
		return reflect.Value{}
	}
	if !isSynthIfaceTargetFunc(fn) {
		return reflect.Value{}
	}
	// A native-bridged interface (error, io.Writer) already carries its canonical
	// rtype; reuse it so the retyped pointer's element keeps that identity
	// (reflect.TypeFor[error]() stays == a func's error result). Only an
	// interpreted/anonymous interface erased to interface{} needs a synth carrier
	// built here to expose its method set to the native callee.
	st := et.Rtype
	if st == mtype.AnyRtype || st.NumMethod() == 0 {
		// Fill unmaterialized method sigs before building the synth rtype.
		// Unconditional: an unlocked Rtype==nil pre-check would race a concurrent materialize.
		// Safe only because this boundary, unlike materializeFuncIO, does not hold materializeMu.
		for i := range et.IfaceMethods {
			derive.MaterializeIfaceMethod(&et.IfaceMethods[i])
		}
		st = derive.SynthIfaceRtype(et)
	}
	if st == nil {
		return reflect.Value{}
	}
	if val.IsNil() {
		// Nil ptr-to-interface used as a pure type tag.
		return reflect.Zero(reflect.PointerTo(st))
	}
	return reflect.NewAt(st, val.UnsafePointer())
}

// synthIfaceTargetPCs holds native-func PCs whose pointer-to-interface arg may be retyped.
var (
	synthIfaceTargetPCs      sync.Map // uintptr -> struct{}
	synthIfaceWriteTargetPCs sync.Map // uintptr -> struct{}
)

func storeFuncPC(s *sync.Map, fn reflect.Value) {
	if fn.IsValid() && fn.Kind() == reflect.Func {
		s.Store(fn.Pointer(), struct{}{})
	}
}

func hasFuncPC(s *sync.Map, fn reflect.Value) bool {
	if !fn.IsValid() || fn.Kind() != reflect.Func {
		return false
	}
	_, ok := s.Load(fn.Pointer())
	return ok
}

// RegisterSynthIfaceTargetFunc allowlists fn for synth-interface target retyping.
func RegisterSynthIfaceTargetFunc(fn reflect.Value) { storeFuncPC(&synthIfaceTargetPCs, fn) }

func isSynthIfaceTargetFunc(fn reflect.Value) bool { return hasFuncPC(&synthIfaceTargetPCs, fn) }

// RegisterSynthIfaceWriteTargetFunc allowlists fn as a synth-interface write target.
func RegisterSynthIfaceWriteTargetFunc(fn reflect.Value) { storeFuncPC(&synthIfaceWriteTargetPCs, fn) }

func isSynthIfaceWriteTargetFunc(fn reflect.Value) bool {
	return hasFuncPC(&synthIfaceWriteTargetPCs, fn)
}

// ifaceWriteback is a retyped synth-interface pointer arg to normalize after the native write-target call.
type ifaceWriteback struct {
	ptr    unsafe.Pointer
	st     reflect.Type // synth interface rtype the callee wrote through
	before [2]uintptr   // cell's two words (eface) before the call
}

func (m *Machine) normalizeIfaceWritebacks(wb []ifaceWriteback) {
	for _, w := range wb {
		// Unchanged cell: no match written, so it still holds mvm form.
		if *(*[2]uintptr)(w.ptr) == w.before {
			continue
		}
		synthVal := reflect.NewAt(w.st, w.ptr).Elem()
		if synthVal.IsNil() {
			continue // wrote a nil interface; nothing to wrap (errors.As never does)
		}
		concrete := runtype.Exportable(synthVal.Elem())
		loc := reflect.NewAt(mtype.AnyRtype, w.ptr).Elem()
		if t := m.typeByRtype(concrete.Type()); t != nil {
			loc.Set(reflect.ValueOf(any(Iface{Typ: t, Val: FromReflect(concrete)})))
		} else {
			loc.Set(concrete)
		}
	}
}

// ifaceEfaceWord is the eface type word of an any boxing vm.Iface, the form
// storeIfaceFromReflect writes.
// An itab can never alias an rtype allocation, so it discriminates that form
// from a genuine native iface in the same slot.
var ifaceEfaceWord = func() unsafe.Pointer {
	var a any = Iface{}
	return (*[2]unsafe.Pointer)(unsafe.Pointer(&a))[0]
}()

// mvmEfaceInSynthSlot decodes the mvm eface form out of an addressable
// synth-iface slot (see storeIfaceFromReflect); native reads (v.Elem) would
// misinterpret its rtype word as an itab.
func mvmEfaceInSynthSlot(v reflect.Value, t reflect.Type) (Iface, bool) {
	if !v.CanAddr() || !runtype.IsSynth(t) {
		return Iface{}, false
	}
	p := v.Addr().UnsafePointer()
	if (*[2]unsafe.Pointer)(p)[0] != ifaceEfaceWord {
		return Iface{}, false
	}
	ifc, ok := reflect.NewAt(mtype.AnyRtype, p).Elem().Interface().(Iface)
	return ifc, ok
}

// storeIfaceFromReflect writes src into the addressable synth-interface slot dst in
// mvm's interface form (an eface boxing vm.Iface), so a later interpreted method
// dispatch on dst reads a proper Iface.
// A native reflect write would store a native iface (itab+data) the interpreter can't decode.
func (m *Machine) storeIfaceFromReflect(dst, src reflect.Value) {
	if !dst.CanAddr() {
		return
	}
	loc := reflect.NewAt(mtype.AnyRtype, dst.Addr().UnsafePointer()).Elem()
	el := src
	for el.IsValid() && el.Kind() == reflect.Interface {
		if el.IsNil() {
			loc.Set(reflect.Zero(mtype.AnyRtype))
			return
		}
		el = el.Elem()
	}
	if !el.IsValid() {
		loc.Set(reflect.Zero(mtype.AnyRtype))
		return
	}
	el = runtype.Exportable(el)
	if el.Type() == ifaceRtype { // already an mvm Iface
		loc.Set(reflect.ValueOf(el.Interface()))
		return
	}
	if t := m.typeByRtype(el.Type()); t != nil {
		loc.Set(reflect.ValueOf(any(Iface{Typ: t, Val: FromReflect(el)})))
		return
	}
	loc.Set(el)
}

// InstallReflectSetSynthIfaceHook stores eface form for a reflect.Value.Set into a
// synth-interface slot; the native itab form is undecodable by the interpreter's slots.
// Reached once errors.As's anon-iface target is retyped to a synth iface.
func InstallReflectSetSynthIfaceHook() {
	RegisterNativeMethodHook(reflect.Value{}, "Set", func(m *Machine, recv reflect.Value, args []reflect.Value) []reflect.Value {
		dst, _ := recv.Interface().(reflect.Value)
		var src reflect.Value
		if len(args) > 0 {
			src, _ = args[0].Interface().(reflect.Value)
		}
		if dst.IsValid() && dst.CanAddr() && dst.Kind() == reflect.Interface && runtype.IsSynth(dst.Type()) {
			m.storeIfaceFromReflect(dst, src)
			return nil
		}
		dst.Set(src)
		return nil
	})
}

func (m *Machine) attachValueRecv(t *mtype.Type) error {
	if derive.HasNativeIdentity(t) {
		return nil // methods dispatch natively
	}
	specs := m.allSynthMethods(t, false)
	if len(specs) == 0 {
		return nil
	}
	methods := toSynthMethods(m, t, specs)
	res := derive.LookupReservation(t)
	if res == nil && derive.IsGenericInstanceName(t.Name) {
		return nil // see attachPtrRecv
	}
	if res == nil || res.Value() == nil {
		return fmt.Errorf("synth: value-method type %s has no reservation at attach", derive.QualifiedTypeName(t))
	}
	return m.fillSynthMethods(res.Value(), methods)
}

// fillSynthMethods installs methods, recording the release closure for ReleaseSynthMethods.
func (m *Machine) fillSynthMethods(res *runtype.Reservation, methods []stubs.Method) error {
	release, err := stubs.FillMethods(res, methods)
	if release != nil {
		m.synthReleasesMu.Lock()
		m.synthReleases = append(m.synthReleases, release)
		m.synthReleasesMu.Unlock()
	}
	return err
}

// ReleaseSynthMethods nils the stub-pool handler slots this Machine acquired so a
// disposed interpreter becomes collectable (slot indices stay consumed). Do not
// dispatch its synth methods after. Backs Interp.Close; no-op on wasm.
func (m *Machine) ReleaseSynthMethods() {
	m.synthReleasesMu.Lock()
	releases := m.synthReleases
	m.synthReleases = nil
	m.synthReleasesMu.Unlock()
	for _, r := range releases {
		r()
	}
}

func (m *Machine) attachPtrRecv(t *mtype.Type) error {
	if derive.HasNativeIdentity(t) {
		return nil // methods dispatch natively
	}
	specs := m.allSynthMethods(t, true)
	if len(specs) == 0 {
		return nil
	}
	methods := toSynthMethods(m, t, specs)
	res := derive.LookupReservation(t)
	if res == nil && derive.IsGenericInstanceName(t.Name) {
		// A monomorphized generic instance materialized before its method table was filled.
		return nil
	}
	if res == nil || res.Ptr() == nil {
		return fmt.Errorf("synth: ptr-method type %s has no pointer reservation at attach", derive.QualifiedTypeName(t))
	}
	return m.fillSynthMethods(res.Ptr(), methods)
}

// recvForm tells a handler how to reconstruct the receiver from the stub's iface data word.
type recvForm uint8

const (
	recvPtr   recvForm = iota // pointer-receiver method: the word is the *T receiver
	recvDeref                 // value-receiver method: the word is an address
	recvWord                  // value-receiver method on direct-iface: the word is the receiver value
)

func recvFormFor(rtype reflect.Type, ptrRecv, ptrIdent bool) recvForm {
	switch {
	case ptrRecv:
		return recvPtr
	case !ptrIdent && derive.IsDirectIface(rtype):
		return recvWord
	default:
		return recvDeref
	}
}

// synthMethodSpec describes a single method picked for synth attachment.
type synthMethodSpec struct {
	name       string
	method     mtype.Method
	shape      stubs.Shape // matched signature shape
	wordKey    string      // non-empty => word-class path (shape ignored)
	swallowErr bool        // word path: swallow dispatch errors to zero results
	form       recvForm    //
	pkgName    string      // declaring package for an unexported method ("" if exported)
}

func (s *synthMethodSpec) resolveDispatch(erased, precise reflect.Type) bool {
	if synthSharedPC {
		// wasm: attach every method via one shared PC; no shape needed.
		s.method.Rtype = precise
		return true
	}
	shape, shapeOK := detectShape(erased)
	if shapeOK && !forceWordShape && erased == precise {
		s.shape = shape
		s.method.Rtype = erased
		return true
	}
	if shapeOK {
		// A typed fallback exists: probe silently, a miss is not a drop.
		if key, ok := wordShapeKey(precise); ok {
			s.wordKey = key
			s.swallowErr = shapeSwallowsDispatchErr(shape)
			s.method.Rtype = precise
			return true
		}
		wordabi.RecordDegradedDrop("erased typed fallback", precise)
		s.shape = shape
		s.method.Rtype = erased
		return true
	}
	if key, ok := detectWordShape(precise); ok {
		s.wordKey = key
		s.method.Rtype = precise
		return true
	}
	return false
}

func shapeSwallowsDispatchErr(shape stubs.Shape) bool {
	switch shape {
	case stubs.ShapeS5, stubs.ShapeS11, stubs.ShapeS12:
		return true
	}
	return false
}

func toSynthMethods(
	m *Machine, t *mtype.Type, specs []synthMethodSpec,
) []stubs.Method {
	out := make([]stubs.Method, len(specs))
	for i, s := range specs {
		if synthSharedPC {
			// wasm: shared-PC attach ignores shape/handler; only the method's
			// name/signature metadata is needed for reflect introspection.
			out[i] = stubs.Method{
				Name:     s.name,
				Exported: derive.IsExportedName(s.name),
				PkgPath:  s.pkgName,
				Sig:      s.method.Rtype,
			}
			continue
		}
		if s.wordKey != "" {
			out[i] = stubs.Method{
				Name:     s.name,
				Exported: derive.IsExportedName(s.name),
				PkgPath:  s.pkgName,
				Sig:      s.method.Rtype,
				WordKey:  s.wordKey,
				Core:     m.makeWordCore(t, s.method, s.name, s.form, s.swallowErr),
			}
			continue
		}
		var handler any
		if makeHandlerHook != nil {
			handler = makeHandlerHook(
				SynthCall{m: m, t: t, name: s.name, method: s.method, form: s.form}, s.shape)
		}
		out[i] = stubs.Method{
			Name:     s.name,
			Exported: derive.IsExportedName(s.name),
			PkgPath:  s.pkgName,
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
	t *mtype.Type, includePtr bool,
) []synthMethodSpec {
	const synthMaxMethods = runtype.MaxMethods
	var specs []synthMethodSpec
	seen := map[string]bool{}
	for i, method := range t.Methods {
		if !method.IsResolved() || i >= len(m.MethodNames) {
			continue
		}
		if method.PtrRecv && !includePtr {
			continue
		}
		spec := synthMethodSpec{
			name:    m.MethodNames[i],
			method:  method,
			form:    recvFormFor(t.Rtype, method.PtrRecv, includePtr),
			pkgName: derive.UnexportedMethodPkg(t, method, m.MethodNames[i]),
		}
		// Typed-shape tables erase synth-iface params to any.
		if !spec.resolveDispatch(derive.EraseSynthIfaceParams(method.Rtype), method.Rtype) {
			continue
		}
		specs = append(specs, spec)
		seen[m.MethodNames[i]] = true
		if len(specs) == synthMaxMethods {
			return specs
		}
	}
	// Methods promoted from embedded fields are absent from t.Methods. Attatch them explicetely.
	for _, spec := range m.promotedSynthMethods(t, includePtr, seen) {
		specs = append(specs, spec)
		if len(specs) == synthMaxMethods {
			break
		}
	}
	return specs
}

func (m *Machine) promotedSynthMethods(t *mtype.Type, includePtr bool, seen map[string]bool) []synthMethodSpec {
	var specs []synthMethodSpec
	for _, emb := range t.Embedded {
		if emb.FieldIdx < 0 || emb.FieldIdx >= t.Rtype.NumField() {
			continue
		}
		ft := t.Rtype.Field(emb.FieldIdx).Type
		// Embedded interface methods are promoted by reflect.StructOf itself and
		// dispatched via the EmbedIface path; skip them here.
		if ft.Kind() == reflect.Interface {
			continue
		}
		// A pointer embed contributes all of the pointee's methods to both T and *T.
		// A value embed contributes value-receiver methods to T and value+pointer methods to *T.
		setType := ft
		if includePtr && ft.Kind() != reflect.Pointer {
			setType = reflect.PointerTo(ft)
		}
		for _, meth := range runtype.TypeMethods(setType) {
			if !meth.IsExported() || seen[meth.Name] || t.HiddenMethods[meth.Name] {
				continue
			}
			sig := derive.StripRecvType(meth.Type)
			spec := synthMethodSpec{
				name:   meth.Name,
				method: mtype.Method{Index: -1, Path: []int{emb.FieldIdx}, Rtype: sig, PtrRecv: includePtr},
				form:   recvFormFor(t.Rtype, includePtr, includePtr),
			}
			if !spec.resolveDispatch(sig, sig) {
				continue
			}
			seen[meth.Name] = true
			specs = append(specs, spec)
		}
	}
	return specs
}

// errorIface is the error interface's canonical rtype, used to recognize
// native-bridged error values (synth_native_shim.go).
var errorIface = reflect.TypeFor[error]()

// RaiseIfInterpPanic re-raises err as its raw interpreter panic value when err
// wraps a PanicError, so a native recover sees the original; otherwise it returns,
// letting the caller surface err as an ordinary error result. Used by out-of-vm
// synth shape handlers that re-enter the interpreter.
func RaiseIfInterpPanic(err error) {
	var pe *PanicError
	if errors.As(err, &pe) {
		panic(reraisedPanic{pe})
	}
}

func raiseMethodErr(err error) {
	var pe *PanicError
	if errors.As(err, &pe) {
		panic(reraisedPanic{pe})
	}
	panic(err)
}

// ReflectToError unboxes v to an error, treating a typed-nil as nil.
func ReflectToError(v reflect.Value) error {
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
	rerr, _ := runtype.Exportable(v).Interface().(error)
	return rerr
}

func makeRecvValue(rtype reflect.Type, recv unsafe.Pointer, form recvForm) reflect.Value {
	switch form {
	case recvPtr:
		return reflect.NewAt(rtype, recv)
	case recvWord:
		return reflect.NewAt(rtype, unsafe.Pointer(&recv)).Elem()
	default: // recvDeref
		// Value receiver: copy the boxed value so a struct/array field write in
		// the method body stays local and does not leak back into the caller's
		// interface storage (matches the IfaceCall opcode detach).
		v := reflect.NewAt(rtype, recv).Elem()
		cp := reflect.New(rtype).Elem()
		cp.Set(runtype.Exportable(v))
		return cp
	}
}

func callMethod(
	m *Machine, ifcType *mtype.Type, name string, rv reflect.Value,
	method mtype.Method, methodSig reflect.Type, args []reflect.Value,
) ([]reflect.Value, error) {
	ifc := Iface{Typ: ifcType, Val: FromReflect(rv)}
	if method.EmbedIface {
		return m.callEmbedIface(ifc, method, name, methodSig, args)
	}
	if method.Index < 0 && method.Path != nil {
		return m.callPromotedConcrete(rv, name, method.Path, methodSig, args)
	}
	fval := m.MakeMethodCallable(ifc, method)
	// Run on a pooled runner, not m itself.
	// native callers run on several interpreted goroutines,
	// and CallFunc's save/restore of m's frame state is single-threaded.
	rs := m.captureRunnerState()
	runner := rs.acquireRunner()
	defer rs.releaseRunner(runner)
	return runner.callPooled(fval, methodSig, args)
}

func (m *Machine) callPromotedConcrete(
	rv reflect.Value, name string, path []int, methodSig reflect.Type, args []reflect.Value,
) ([]reflect.Value, error) {
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	for _, fi := range path {
		if !rv.IsValid() {
			panic(nilPointerPanicValue)
		}
		rv = rv.Field(fi)
	}
	rv = runtype.Exportable(rv)
	embedded := FromReflect(rv)
	if embedded.IsIface() {
		ic := embedded.IfaceVal()
		if mid := m.methodID(name); mid >= 0 && mid < len(ic.Typ.Methods) {
			return callMethod(m, ic.Typ, name, ic.Val.Reflect(), ic.Typ.Methods[mid], methodSig, args)
		}
		return nil, fmt.Errorf("synth: promoted method %q unresolved", name)
	}
	if isNilReceiver(rv) {
		return nil, errors.New("synth: nil promoted receiver")
	}
	if mv := nativeMethodLookup(m, rv, name); mv.IsValid() {
		return callBound(mv, args), nil
	}
	// A pointer-receiver method promoted from a value embed lives in *E's method set, not E's;
	// retry on the addressable field's address.
	if rv.CanAddr() {
		if mv := nativeMethodLookup(m, rv.Addr(), name); mv.IsValid() {
			return callBound(mv, args), nil
		}
	}
	if et := m.typeByRtype(rv.Type()); et != nil {
		if mid := m.methodID(name); mid >= 0 && mid < len(et.Methods) {
			return callMethod(m, et, name, rv, et.Methods[mid], methodSig, args)
		}
	}
	return nil, fmt.Errorf("synth: promoted method %q not found", name)
}

// bindPromotedNative resolves, on the shared-PC (wasm) build, a method promoted
// from a native embed of a synth receiver.
func (m *Machine) bindPromotedNative(recv reflect.Value, name string) (reflect.Value, bool) {
	if !synthSharedPC {
		return reflect.Value{}, false
	}
	cv := recv
	if cv.Kind() == reflect.Interface && !cv.IsNil() {
		cv = cv.Elem()
	}
	if !cv.IsValid() || !isSynthOrSynthPtr(cv.Type()) {
		return reflect.Value{}, false
	}
	var st *mtype.Type
	if t := m.typeByRtype(cv.Type()); t != nil {
		if st = t; st.IsPtr() {
			st = st.ElemType
		}
	}
	return m.promotedNativeMethod(cv, st, name)
}

// promotedNativeMethod binds a method promoted from a NATIVE embedded field of a synth struct receiver.
func (m *Machine) promotedNativeMethod(rv reflect.Value, st *mtype.Type, name string) (reflect.Value, bool) {
	base := reflect.Indirect(rv)
	if !base.IsValid() || base.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	bt := base.Type()
	seen := map[int]bool{}
	tryIdx := func(idx int) (reflect.Value, bool) {
		if idx < 0 || idx >= base.NumField() || seen[idx] {
			return reflect.Value{}, false
		}
		seen[idx] = true
		ft := bt.Field(idx).Type
		if ft.Kind() == reflect.Interface || isSynthOrSynthPtr(ft) {
			return reflect.Value{}, false
		}
		fv := runtype.Exportable(base.Field(idx))
		if mv := nativeMethodLookup(m, fv, name); mv.IsValid() {
			return mv, true
		}
		if fv.CanAddr() {
			if mv := nativeMethodLookup(m, fv.Addr(), name); mv.IsValid() {
				return mv, true
			}
		}
		return reflect.Value{}, false
	}
	if st != nil {
		for _, emb := range st.Embedded {
			if mv, ok := tryIdx(emb.FieldIdx); ok {
				return mv, true
			}
		}
	}
	for i := range base.NumField() {
		if bt.Field(i).Anonymous {
			if mv, ok := tryIdx(i); ok {
				return mv, true
			}
		}
	}
	return reflect.Value{}, false
}

func callBound(mv reflect.Value, args []reflect.Value) []reflect.Value {
	if mv.Type().IsVariadic() {
		return mv.CallSlice(args)
	}
	return mv.Call(args)
}

func (m *Machine) methodID(name string) int {
	return mtype.MethodID(m.MethodNames, name)
}

func (m *Machine) callEmbedIface(ifc Iface, method mtype.Method, name string, methodSig reflect.Type, args []reflect.Value,
) ([]reflect.Value, error) {
	methodID := m.methodID(name)
	for method.EmbedIface {
		rv := ifc.Val.Reflect()
		if rv.Kind() == reflect.Pointer {
			rv = rv.Elem()
		}
		for _, fi := range method.Path {
			rv = rv.Field(fi)
		}
		// Embedded fields are often unexported, value carries RO flag. Clear it before dispatch.
		rv = runtype.Exportable(rv)
		embedded := FromReflect(rv)
		if !embedded.IsIface() {
			if isNilReceiver(rv) {
				return nil, errors.New("synth: nil embedded receiver")
			}
			mv := nativeMethodLookup(m, rv, name)
			if !mv.IsValid() {
				return nil, fmt.Errorf("synth: embedded method %q not found", name)
			}
			return callBound(mv, args), nil
		}
		ifc = embedded.IfaceVal()
		if methodID < 0 || methodID >= len(ifc.Typ.Methods) {
			return nil, fmt.Errorf("synth: embedded method %q unresolved", name)
		}
		method = ifc.Typ.Methods[methodID]
	}
	return m.CallFunc(m.MakeMethodCallable(ifc, method), methodSig, args)
}
