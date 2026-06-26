package vm

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/mvm-sh/mvm/runtype"
	"github.com/mvm-sh/mvm/stdlib/stubs"
)

// AttachSynthMethods fills t's interpreted methods into the synth rtype that was
// reserved for t at materialize.
// Native code that asserts the rtype to an interface dispatches the method directly.
//
// Up to synth's per-attach method cap (runtype.MaxMethods).
// Excess methods of the same receiver kind are silently dropped.
func (m *Machine) AttachSynthMethods(t *Type) error {
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
	if st == AnyRtype || st.NumMethod() == 0 {
		// Fill unmaterialized method sigs before building the synth rtype.
		// Unconditional: an unlocked Rtype==nil pre-check would race a concurrent materialize.
		// Safe only because this boundary, unlike materializeFuncIO, does not hold materializeMu.
		for i := range et.IfaceMethods {
			MaterializeIfaceMethod(&et.IfaceMethods[i])
		}
		st = synthIfaceRtype(et)
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
		concrete := Exportable(synthVal.Elem())
		loc := reflect.NewAt(AnyRtype, w.ptr).Elem()
		if t := m.typeByRtype(concrete.Type()); t != nil {
			loc.Set(reflect.ValueOf(any(Iface{Typ: t, Val: FromReflect(concrete)})))
		} else {
			loc.Set(concrete)
		}
	}
}

// storeIfaceFromReflect writes src into the addressable synth-interface slot dst in
// mvm's interface form (an eface boxing vm.Iface), so a later interpreted method
// dispatch on dst reads a proper Iface.
// A native reflect write would store a native iface (itab+data) the interpreter can't decode.
func (m *Machine) storeIfaceFromReflect(dst, src reflect.Value) {
	if !dst.CanAddr() {
		return
	}
	loc := reflect.NewAt(AnyRtype, dst.Addr().UnsafePointer()).Elem()
	el := src
	for el.IsValid() && el.Kind() == reflect.Interface {
		if el.IsNil() {
			loc.Set(reflect.Zero(AnyRtype))
			return
		}
		el = el.Elem()
	}
	if !el.IsValid() {
		loc.Set(reflect.Zero(AnyRtype))
		return
	}
	el = Exportable(el)
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

// synthIfaceRtype returns t's method-bearing synthetic interface rtype, building
// and caching it on first use (nil if a method signature cannot be built; the
// any bridge stays and a later call retries). Clones of one named interface
// dedupe by synthIfaceNameKey to a single rtype identity.
//
// When some method signature is still unmaterialized, t's rtype is reserved and
// cached before the signatures are built, so a self- or mutually-recursive
// reference within them (type EnumType <-> Enum) resolves to this final pointer
// rather than erasing a cycle-breaking back-edge whose location -- and thus a
// later reflect.Implements -- would depend on materialization order. That path
// materializes signatures and so requires materializeMu; the only caller that
// may pass unmaterialized signatures (materializeFuncIO) holds it, while callers
// off the lock (bridgePtrToIface) pre-materialize first, taking the atomic path.
func synthIfaceRtype(t *Type) reflect.Type {
	derivedMu.Lock()
	if st := synthIfaceCache[t]; st != nil {
		derivedMu.Unlock()
		return st
	}
	key := synthIfaceNameKey(t)
	if key != "" {
		if st := synthIfaceNamed[key]; st != nil {
			synthIfaceCache[t] = st
			derivedMu.Unlock()
			return st
		}
	}
	name := synthIfaceName(t)
	if name == "" {
		derivedMu.Unlock()
		return nil
	}
	if ims, ok := collectImethods(t, false); ok {
		// All signatures materialized: no cycle to break, build atomically.
		rt := runtype.InterfaceOf(name, rtypePkgPath(t), ims)
		cacheSynthIface(t, key, rt)
		derivedMu.Unlock()
		return rt
	}
	h := runtype.ReserveInterface(name, rtypePkgPath(t))
	rt := h.Type()
	cacheSynthIface(t, key, rt)
	derivedMu.Unlock()

	ims, ok := collectImethods(t, true)
	if !ok {
		derivedMu.Lock()
		uncacheSynthIface(t, key)
		derivedMu.Unlock()
		return nil
	}
	h.FillMethods(ims)
	return rt
}

// cacheSynthIface records rt as t's synth interface rtype and under its
// cross-clone dedupe key. Caller holds derivedMu.
func cacheSynthIface(t *Type, key string, rt reflect.Type) {
	synthIfaceCache[t] = rt
	if key != "" {
		synthIfaceNamed[key] = rt
	}
}

// uncacheSynthIface rolls back a reservation whose method sigs could not be
// built. Caller holds derivedMu.
func uncacheSynthIface(t *Type, key string) {
	delete(synthIfaceCache, t)
	if key != "" {
		delete(synthIfaceNamed, key)
	}
}

// synthIfaceName is t's interface name for the synth rtype, falling back to the
// reflect string of an already-bound rtype for an unnamed interface.
func synthIfaceName(t *Type) string {
	if name := qualifiedTypeName(t); name != "" {
		return name
	}
	if t.Rtype != nil {
		return t.Rtype.String()
	}
	return ""
}

// collectImethods builds the runtype.Imethod set from t's interface methods.
// With materializeSigs it fills an unset im.Rtype from im.Sig (needs materializeMu).
// ok is false if any method signature is still missing or non-func.
func collectImethods(t *Type, materializeSigs bool) (ims []runtype.Imethod, ok bool) {
	ims = make([]runtype.Imethod, 0, len(t.IfaceMethods))
	for i := range t.IfaceMethods {
		im := &t.IfaceMethods[i]
		if materializeSigs && im.Rtype == nil && im.Sig != nil {
			im.Rtype = materialize(im.Sig)
		}
		if im.Rtype == nil || im.Rtype.Kind() != reflect.Func {
			return nil, false
		}
		ims = append(ims, runtype.Imethod{
			Name:     im.Name,
			Exported: isExportedName(im.Name),
			Sig:      im.Rtype,
		})
	}
	return ims, len(ims) > 0
}

func isGenericInstanceName(name string) bool {
	return strings.IndexByte(name, '#') >= 0
}

func isExportedName(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// rtypePkgPath is the import path stamped into a synth rtype's uncommon section,
// surfaced by reflect.Type.PkgPath(). Native Go returns the full import path
// ("gorm.io/gorm"), not the short package name ("gorm"); prefer ImportPath and
// fall back to PkgName for main/REPL/synthetic types (ImportPath == "").
func rtypePkgPath(t *Type) string {
	if t.ImportPath != "" {
		return t.ImportPath
	}
	return t.PkgName
}

func qualifiedTypeName(t *Type) string {
	if t.PkgName == "" || t.Name == "" {
		return t.Name
	}
	base := t.PkgName
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base + "." + t.Name
}

func (m *Machine) attachValueRecv(t *Type) error {
	specs := m.allSynthMethods(t, false)
	if len(specs) == 0 {
		return nil
	}
	methods := toSynthMethods(m, t, specs)
	res := lookupReservation(t)
	if res == nil && isGenericInstanceName(t.Name) {
		return nil // see attachPtrRecv
	}
	if res == nil || res.value == nil {
		return fmt.Errorf("synth: value-method type %s has no reservation at attach", qualifiedTypeName(t))
	}
	return m.fillSynthMethods(res.value, methods)
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

func (m *Machine) attachPtrRecv(t *Type) error {
	specs := m.allSynthMethods(t, true)
	if len(specs) == 0 {
		return nil
	}
	methods := toSynthMethods(m, t, specs)
	res := lookupReservation(t)
	if res == nil && isGenericInstanceName(t.Name) {
		// A monomorphized generic instance materialized before its method table was filled.
		return nil
	}
	if res == nil || res.ptr == nil {
		return fmt.Errorf("synth: ptr-method type %s has no pointer reservation at attach", qualifiedTypeName(t))
	}
	return m.fillSynthMethods(res.ptr, methods)
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
	case !ptrIdent && isDirectIface(rtype):
		return recvWord
	default:
		return recvDeref
	}
}

// synthMethodSpec describes a single method picked for synth attachment.
type synthMethodSpec struct {
	name       string
	method     Method
	shape      stubs.Shape // matched signature shape
	wordKey    string      // non-empty => word-class path (shape ignored)
	swallowErr bool        // word path: swallow dispatch errors to zero results
	form       recvForm    //
	pkgName    string      // declaring package for an unexported method ("" if exported)
}

func (m *Machine) unexportedMethodPkg(t *Type, method Method, name string) string {
	if isExportedName(name) {
		return ""
	}
	cur := t
	for _, idx := range method.Path {
		next := embeddedTypeAt(cur, idx)
		if next == nil {
			break
		}
		cur = next
	}
	// Use the declaring package's full import path so an unexported method matches
	// reflect.Implements: it compares this embedded pkgPath against the interface
	// type's PkgPath, which rtypePkgPath also resolves to the import path.
	return rtypePkgPath(cur)
}

func embeddedTypeAt(t *Type, idx int) *Type {
	for _, e := range t.Embedded {
		if e.FieldIdx == idx {
			return e.Type
		}
	}
	return nil
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
		recordWordDrop(&wordDropDegraded, "erased typed fallback", precise)
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
	m *Machine, t *Type, specs []synthMethodSpec,
) []stubs.Method {
	out := make([]stubs.Method, len(specs))
	for i, s := range specs {
		if synthSharedPC {
			// wasm: shared-PC attach ignores shape/handler; only the method's
			// name/signature metadata is needed for reflect introspection.
			out[i] = stubs.Method{
				Name:     s.name,
				Exported: isExportedName(s.name),
				PkgPath:  s.pkgName,
				Sig:      s.method.Rtype,
			}
			continue
		}
		if s.wordKey != "" {
			out[i] = stubs.Method{
				Name:     s.name,
				Exported: isExportedName(s.name),
				PkgPath:  s.pkgName,
				Sig:      s.method.Rtype,
				WordKey:  s.wordKey,
				Core:     m.makeWordCore(t, s.method, s.name, s.form, s.swallowErr),
			}
			continue
		}
		var handler any
		switch s.shape {
		case stubs.ShapeS1:
			handler = makeHandlerS1(m, t, s.method, s.name, s.form)
		case stubs.ShapeS2:
			handler = makeHandlerS2(m, t, s.method, s.name, s.form)
		case stubs.ShapeS3:
			handler = makeHandlerS3(m, t, s.method, s.name, s.form)
		case stubs.ShapeS4:
			handler = makeHandlerS4(m, t, s.method, s.name, s.form)
		case stubs.ShapeS5:
			handler = makeHandlerS5(m, t, s.method, s.name, s.form)
		case stubs.ShapeS6:
			handler = makeHandlerS6(m, t, s.method, s.name, s.form)
		case stubs.ShapeS7:
			handler = makeHandlerS7(m, t, s.method, s.name, s.form)
		case stubs.ShapeS8:
			handler = makeHandlerS8(m, t, s.method, s.name, s.form)
		case stubs.ShapeS9:
			handler = makeHandlerS9(m, t, s.method, s.name, s.form)
		case stubs.ShapeS10:
			handler = makeHandlerS10(m, t, s.method, s.name, s.form)
		case stubs.ShapeS11:
			handler = makeHandlerS11(m, t, s.method, s.name, s.form)
		case stubs.ShapeS12:
			handler = makeHandlerS12(m, t, s.method, s.name, s.form)
		case stubs.ShapeS13:
			handler = makeHandlerS13(m, t, s.method, s.name, s.form)
		case stubs.ShapeS14:
			handler = makeHandlerS14(m, t, s.method, s.name, s.form)
		case stubs.ShapeS15:
			handler = makeHandlerS15(m, t, s.method, s.name, s.form)
		case stubs.ShapeS16:
			handler = makeHandlerS16(m, t, s.method, s.name, s.form)
		case stubs.ShapeS17:
			handler = makeHandlerS17(m, t, s.method, s.name, s.form)
		case stubs.ShapeS18:
			handler = makeHandlerS18(m, t, s.method, s.name, s.form)
		case stubs.ShapeS19:
			handler = makeHandlerS19(m, t, s.method, s.name, s.form)
		case stubs.ShapeS20:
			handler = makeHandlerS20(m, t, s.method, s.name, s.form)
		case stubs.ShapeS21:
			handler = makeHandlerS21(m, t, s.method, s.name, s.form)
		case stubs.ShapeS22:
			handler = makeHandlerS22(m, t, s.method, s.name, s.form)
		case stubs.ShapeS23:
			handler = makeHandlerS23(m, t, s.method, s.name, s.form)
		case stubs.ShapeS24:
			handler = makeHandlerS24(m, t, s.method, s.name, s.form)
		case stubs.ShapeS25:
			handler = makeHandlerS25(m, t, s.method, s.name, s.form)
		case stubs.ShapeS26:
			handler = makeHandlerS26(m, t, s.method, s.name, s.form)
		case stubs.ShapeS27:
			handler = makeHandlerS27(m, t, s.method, s.name, s.form)
		case stubs.ShapeS28:
			handler = makeHandlerS28(m, t, s.method, s.name, s.form)
		case stubs.ShapeS29:
			handler = makeHandlerS29(m, t, s.method, s.name, s.form)
		case stubs.ShapeS30:
			handler = makeHandlerS30(m, t, s.method, s.name, s.form)
		case stubs.ShapeS31:
			handler = makeHandlerS31(m, t, s.method, s.name, s.form)
		case stubs.ShapeS32:
			handler = makeHandlerS32(m, t, s.method, s.name, s.form)
		case stubs.ShapeS33:
			handler = makeHandlerS33(m, t, s.method, s.name, s.form)
		case stubs.ShapeS34:
			handler = makeHandlerS34(m, t, s.method, s.name, s.form)
		case stubs.ShapeS35:
			handler = makeHandlerS35(m, t, s.method, s.name, s.form)
		case stubs.ShapeS36:
			handler = makeHandlerS36(m, t, s.method, s.name, s.form)
		case stubs.ShapeS37:
			handler = makeHandlerS37(m, t, s.method, s.name, s.form)
		case stubs.ShapeS38:
			handler = makeHandlerS38(m, t, s.method, s.name, s.form)
		}
		out[i] = stubs.Method{
			Name:     s.name,
			Exported: isExportedName(s.name),
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
	t *Type, includePtr bool,
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
			pkgName: m.unexportedMethodPkg(t, method, m.MethodNames[i]),
		}
		// Typed-shape tables erase synth-iface params to any.
		if !spec.resolveDispatch(eraseSynthIfaceParams(method.Rtype), method.Rtype) {
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

func (m *Machine) promotedSynthMethods(t *Type, includePtr bool, seen map[string]bool) []synthMethodSpec {
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
		for meth := range setType.Methods() {
			if !meth.IsExported() || seen[meth.Name] {
				continue
			}
			sig := stripRecvType(meth.Type)
			spec := synthMethodSpec{
				name:   meth.Name,
				method: Method{Index: -1, Path: []int{emb.FieldIdx}, Rtype: sig, PtrRecv: includePtr},
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

func eraseSynthIfaceParams(sig reflect.Type) reflect.Type {
	if sig == nil || sig.Kind() != reflect.Func {
		return sig
	}
	changed := false
	conv := func(t reflect.Type) reflect.Type {
		if t.Kind() == reflect.Interface && t.NumMethod() > 0 && runtype.IsSynth(t) {
			changed = true
			return anyIface
		}
		return t
	}
	in := make([]reflect.Type, sig.NumIn())
	for i := range in {
		in[i] = conv(sig.In(i))
	}
	out := make([]reflect.Type, sig.NumOut())
	for i := range out {
		out[i] = conv(sig.Out(i))
	}
	if !changed {
		return sig
	}
	return reflect.FuncOf(in, out, sig.IsVariadic())
}

func isDirectIface(rt reflect.Type) bool {
	switch rt.Kind() {
	case reflect.Pointer, reflect.Chan, reflect.Map, reflect.Func, reflect.UnsafePointer:
		return true
	case reflect.Struct:
		return rt.NumField() == 1 && isDirectIface(rt.Field(0).Type)
	case reflect.Array:
		return rt.Len() == 1 && isDirectIface(rt.Elem())
	default:
		return false
	}
}

func stripRecvType(mt reflect.Type) reflect.Type {
	if mt.NumIn() == 0 {
		return mt
	}
	in := make([]reflect.Type, 0, mt.NumIn()-1)
	for i := 1; i < mt.NumIn(); i++ {
		in = append(in, mt.In(i))
	}
	out := make([]reflect.Type, mt.NumOut())
	for i := range out {
		out[i] = mt.Out(i)
	}
	return reflect.FuncOf(in, out, mt.IsVariadic())
}

// detectShape inspects a method signature and returns the matching stubs.Shape if any.
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
	case nin == 0 && nout == 3 && sig.Out(0).Kind() == reflect.Int32 &&
		sig.Out(1).Kind() == reflect.Int && isErrorType(sig.Out(2)):
		return stubs.ShapeS37, true
	case nin == 0 && nout == 0:
		return stubs.ShapeS38, true
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

	// io/fs cluster.
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.Int64:
		return stubs.ShapeS22, true
	case nin == 0 && nout == 1 && sig.Out(0) == fsFileModeType:
		return stubs.ShapeS23, true
	case nin == 0 && nout == 1 && sig.Out(0) == timeTimeType:
		return stubs.ShapeS24, true
	case nin == 0 && nout == 2 && sig.Out(0) == fsFileInfoIface && isErrorType(sig.Out(1)):
		return stubs.ShapeS25, true
	case nin == 1 && nout == 2 && sig.In(0).Kind() == reflect.String && isErrorType(sig.Out(1)):
		switch sig.Out(0) {
		case fsFileIface:
			return stubs.ShapeS26, true
		case fsFileInfoIface:
			return stubs.ShapeS27, true
		case fsFSIface:
			return stubs.ShapeS28, true
		case stringSliceType:
			return stubs.ShapeS29, true
		case dirEntrySliceType:
			return stubs.ShapeS30, true
		case byteSliceType:
			return stubs.ShapeS31, true
		}

	// log/slog cluster (slog.Handler).
	case nin == 2 && nout == 1 && sig.In(0) == contextIface &&
		sig.In(1) == slogLevelType && sig.Out(0).Kind() == reflect.Bool:
		return stubs.ShapeS32, true
	case nin == 2 && nout == 1 && sig.In(0) == contextIface &&
		sig.In(1) == slogRecordType && isErrorType(sig.Out(0)):
		return stubs.ShapeS33, true
	case nin == 1 && nout == 1 && sig.In(0) == slogAttrSliceType &&
		sig.Out(0) == slogHandlerIface:
		return stubs.ShapeS34, true
	case nin == 1 && nout == 1 && sig.In(0).Kind() == reflect.String &&
		sig.Out(0) == slogHandlerIface:
		return stubs.ShapeS35, true
	case nin == 0 && nout == 1 && sig.Out(0) == slogValueType:
		return stubs.ShapeS36, true
	}
	return 0, false
}

// Identity-based predicates: a named alias like `type MyBytes []byte` or
// `type Failure interface{ Error() string }` is structurally compatible but
// has a distinct reflect.Type identity.
var (
	errorIface        = reflect.TypeFor[error]()
	byteSliceType     = reflect.TypeFor[[]byte]()
	anyIface          = reflect.TypeFor[any]()
	errorSliceType    = reflect.TypeFor[[]error]()
	fmtStateIface     = reflect.TypeFor[fmt.State]()
	fmtScanStateIface = reflect.TypeFor[fmt.ScanState]()
	xmlEncoderPtr     = reflect.TypeFor[*xml.Encoder]()
	xmlDecoderPtr     = reflect.TypeFor[*xml.Decoder]()
	xmlStartElem      = reflect.TypeFor[xml.StartElement]()

	// io/fs cluster.
	fsFileModeType    = reflect.TypeFor[fs.FileMode]()
	timeTimeType      = reflect.TypeFor[time.Time]()
	fsFileInfoIface   = reflect.TypeFor[fs.FileInfo]()
	fsFileIface       = reflect.TypeFor[fs.File]()
	fsFSIface         = reflect.TypeFor[fs.FS]()
	stringSliceType   = reflect.TypeFor[[]string]()
	dirEntrySliceType = reflect.TypeFor[[]fs.DirEntry]()

	// log/slog cluster.
	contextIface      = reflect.TypeFor[context.Context]()
	slogLevelType     = reflect.TypeFor[slog.Level]()
	slogRecordType    = reflect.TypeFor[slog.Record]()
	slogAttrSliceType = reflect.TypeFor[[]slog.Attr]()
	slogHandlerIface  = reflect.TypeFor[slog.Handler]()
	slogValueType     = reflect.TypeFor[slog.Value]()
)

func isByteSlice(t reflect.Type) bool { return t == byteSliceType }

func isErrorType(t reflect.Type) bool { return t == errorIface }

func isAnyType(t reflect.Type) bool { return t == anyIface }

func isErrorSlice(t reflect.Type) bool { return t == errorSliceType }

func makeHandlerS1(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS1 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) string {
		rv := makeRecvValue(t.Rtype, recv, form)
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

func raiseMethodErr(err error) {
	var pe *PanicError
	if errors.As(err, &pe) {
		panic(reraisedPanic{pe})
	}
	panic(err)
}

func makeHandlerS2(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS2 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) ([]byte, error) {
		rv := makeRecvValue(t.Rtype, recv, form)
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

func makeHandlerS3(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS3 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, data []byte) error {
		rv := makeRecvValue(t.Rtype, recv, form)
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

func makeHandlerS4(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS4 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, target error) bool {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := []reflect.Value{reflect.ValueOf(&target).Elem()}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

func makeHandlerS5(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS5 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, target any) bool {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := []reflect.Value{reflect.ValueOf(&target).Elem()}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

func makeHandlerS6(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS6 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) error {
		rv := makeRecvValue(t.Rtype, recv, form)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return nil
		}
		return reflectToError(out[0])
	}
}

func makeHandlerS7(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS7 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) []error {
		rv := makeRecvValue(t.Rtype, recv, form)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return nil
		}
		return reflectToErrorSlice(out[0])
	}
}

func makeHandlerS8(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS8 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) int {
		rv := makeRecvValue(t.Rtype, recv, form)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return 0
		}
		return int(out[0].Int())
	}
}

func makeHandlerS9(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS9 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, i, j int) bool {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := []reflect.Value{reflect.ValueOf(i), reflect.ValueOf(j)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

func makeHandlerS10(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS10 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, i, j int) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := []reflect.Value{reflect.ValueOf(i), reflect.ValueOf(j)}
		_, _ = callMethod(m, t, name, rv, method, methodSig, argv)
	}
}

func makeHandlerS11(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS11 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, x any) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := []reflect.Value{reflect.ValueOf(&x).Elem()}
		_, _ = callMethod(m, t, name, rv, method, methodSig, argv)
	}
}

func makeHandlerS12(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS12 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) any {
		rv := makeRecvValue(t.Rtype, recv, form)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 || !out[0].IsValid() {
			return nil
		}
		return Exportable(out[0]).Interface()
	}
}

func makeHandlerS13(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS13 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, p []byte) (int, error) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := []reflect.Value{reflect.ValueOf(p)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			var pe *PanicError
			if errors.As(err, &pe) {
				panic(reraisedPanic{pe})
			}
			return 0, err
		}
		if len(out) != 2 {
			return 0, errors.New("synth: S13 dispatch produced wrong arity")
		}
		return int(out[0].Int()), reflectToError(out[1])
	}
}

func makeHandlerS14(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS14 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, st fmt.State, verb rune) {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := []reflect.Value{reflect.ValueOf(&st).Elem(), reflect.ValueOf(verb)}
		_, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil {
			raiseMethodErr(err)
		}
	}
}

func makeHandlerS15(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS15 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, e *xml.Encoder, start xml.StartElement) error {
		rv := makeRecvValue(t.Rtype, recv, form)
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

func makeHandlerS16(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS16 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, d *xml.Decoder, start xml.StartElement) error {
		rv := makeRecvValue(t.Rtype, recv, form)
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

func makeHandlerS17(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS17 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) (int, bool) {
		rv := makeRecvValue(t.Rtype, recv, form)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 2 {
			return 0, false
		}
		return int(out[0].Int()), out[1].Bool()
	}
}

func makeHandlerS18(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS18 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, c int) bool {
		rv := makeRecvValue(t.Rtype, recv, form)
		argv := []reflect.Value{reflect.ValueOf(c)}
		out, err := callMethod(m, t, name, rv, method, methodSig, argv)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

func makeHandlerS19(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS19 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, st fmt.ScanState, verb rune) error {
		rv := makeRecvValue(t.Rtype, recv, form)
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

func makeHandlerS20(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS20 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer, value string) error {
		rv := makeRecvValue(t.Rtype, recv, form)
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

func makeHandlerS21(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS21 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) bool {
		rv := makeRecvValue(t.Rtype, recv, form)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil || len(out) != 1 {
			return false
		}
		return out[0].Bool()
	}
}

func makeHandlerS37(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS37 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) (rune, int, error) {
		rv := makeRecvValue(t.Rtype, recv, form)
		out, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil {
			var pe *PanicError
			if errors.As(err, &pe) {
				panic(reraisedPanic{pe})
			}
			return 0, 0, err
		}
		if len(out) != 3 {
			return 0, 0, errors.New("synth: S37 dispatch produced wrong arity")
		}
		return rune(out[0].Int()), int(out[1].Int()), reflectToError(out[2])
	}
}

func makeHandlerS38(m *Machine, t *Type, method Method, name string, form recvForm) stubs.HandlerS38 {
	methodSig := method.Rtype
	return func(recv unsafe.Pointer) {
		rv := makeRecvValue(t.Rtype, recv, form)
		_, err := callMethod(m, t, name, rv, method, methodSig, nil)
		if err != nil {
			raiseMethodErr(err)
		}
	}
}

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
		cp.Set(Exportable(v))
		return cp
	}
}

func callMethod(
	m *Machine, ifcType *Type, name string, rv reflect.Value,
	method Method, methodSig reflect.Type, args []reflect.Value,
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
	rv = Exportable(rv)
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
	var st *Type
	if t := m.typeByRtype(cv.Type()); t != nil {
		if st = t; st.IsPtr() {
			st = st.ElemType
		}
	}
	return m.promotedNativeMethod(cv, st, name)
}

// promotedNativeMethod binds a method promoted from a NATIVE embedded field of a synth struct receiver.
func (m *Machine) promotedNativeMethod(rv reflect.Value, st *Type, name string) (reflect.Value, bool) {
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
		fv := Exportable(base.Field(idx))
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
	for id, n := range m.MethodNames {
		if n == name {
			return id
		}
	}
	return -1
}

func (m *Machine) callEmbedIface(ifc Iface, method Method, name string, methodSig reflect.Type, args []reflect.Value,
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
		// Embedded fields are often unexported, value carries RO flag. Clear it before dispatch.
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
