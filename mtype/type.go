// Package mtype holds mvm's symbolic type representation: the compile-time
// Type graph and the constructors/derivations over it.
package mtype

import (
	"encoding/binary"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unsafe"

	"github.com/mvm-sh/mvm/runtype"
)

// Method records a method's code location and receiver path for interface dispatch.
type Method struct {
	Index      int          // data index of code address (-1 if unset or EmbedIface)
	Path       []int        // field index path to embedded receiver (nil = direct, []int{} = deref only)
	EmbedIface bool         // Path leads to an embedded interface field; dispatch through it
	PtrRecv    bool         // true if the method has a pointer receiver
	Rtype      reflect.Type // bound method signature (no receiver)
	Sig        *Type        // symbolic bound method signature (no receiver)
}

// IsResolved reports whether this method slot has been populated with
// either a compiled code address or an embedded-interface dispatch entry.
func (m Method) IsResolved() bool { return m.Index >= 0 || m.EmbedIface }

// EmbeddedField records a mvm embedded field within a struct type.
type EmbeddedField struct {
	FieldIdx int   // index of this field in the parent struct
	Type     *Type // mvm type of the embedded field (shares identity with symbol table)
}

// Type is the representation of a mvm type.
type Type struct {
	PkgName      string          // package short name
	ImportPath   string          // full import path ("" for main/REPL/synthetic)
	Name         string          //
	Pos          int             // source offset of the type declaration (Sources-global); 0 if unknown
	Rtype        reflect.Type    //
	kind         reflect.Kind    // symbolic kind, set at construction; see Kind
	Placeholder  bool            // true for forward-declared struct/interface placeholders until finalized
	IfaceMethods []IfaceMethod   // non-nil for interface types: required method signatures
	TypeElems    []TypeElem      // non-nil for constraint interfaces: union members
	Comparable   bool            // constraint interface embeds the built-in `comparable`
	Methods      []Method        // concrete types: methods[methodID] = code location + receiver path
	Embedded     []EmbeddedField // mvm types of anonymous (embedded) fields, for promoted method lookup
	Params       []*Type         // mvm-level parameter types for func types
	Returns      []*Type         // mvm-level return types for func types
	Fields       []*Type         // mvm-level field types for struct types, parallel to reflect visible fields
	ElemType     *Type           // mvm-level element type for map/slice/array/pointer/chan types
	KeyType      *Type           // mvm-level key type for map types; nil for non-maps or native-built maps

	// Symbolic descriptors of what Rtype otherwise carries, so a type can be
	// materialized (Rtype built) from the symbolic graph alone.
	ArrayLen int             // array length (kind Array)
	ChanDir  reflect.ChanDir // channel direction (kind Chan)
	Variadic bool            // last param is variadic (kind Func)
	Tags     []string        // struct field tags, parallel to Fields (kind Struct)

	// Base is the source *Type a struct-field shallow copy derived from, so
	// methods registered on the source after the copy stay reachable.
	Base *Type

	// Defined marks a top-level `type X T` definition (set at parse), as opposed
	// to a struct-field shallow copy of a named type. A defined type owns its
	// identity and is never a field clone, even when its Base is a named type
	// (type Y X); the field-clone copy sites clear it.
	Defined bool
}

// Kind returns t's kind.
func (t *Type) Kind() reflect.Kind {
	if t.kind != reflect.Invalid {
		return t.kind
	}
	if t.Rtype != nil {
		return t.Rtype.Kind()
	}
	return reflect.Invalid
}

// IfaceMethod describes a method required by an interface type.
type IfaceMethod struct {
	Name  string
	ID    int          // global method ID; -1 = not yet assigned
	Rtype reflect.Type // method signature (no receiver, as declared in the interface body); nil if unknown
	Sig   *Type        // symbolic method signature, the materialize-time source of Rtype
}

// TypeElem describes one member of a constraint interface's type-element union.
type TypeElem struct {
	Approx bool
	Type   *Type
}

// AnyRtype is the reflect.Type for the empty interface (any).
var AnyRtype = reflect.TypeFor[any]()

// IsInterface reports whether t represents an interface type.
func (t *Type) IsInterface() bool {
	return t != nil && t.Kind() == reflect.Interface
}

// EnsureIfaceMethods populates IfaceMethods from the reflect method set if not already set.
func (t *Type) EnsureIfaceMethods() {
	if len(t.IfaceMethods) > 0 || t.Kind() != reflect.Interface {
		return
	}
	for m := range t.Rtype.Methods() {
		t.IfaceMethods = append(t.IfaceMethods, IfaceMethod{Name: m.Name, ID: -1, Rtype: m.Type})
	}
}

// SameAs reports whether t and u represent the same concrete type.
func (t *Type) SameAs(u *Type) bool {
	if t.Rtype != u.Rtype {
		return false
	}
	// Go has no named pointer types, so Rtype alone identifies them.
	if t.Kind() == reflect.Pointer {
		return true
	}
	return t.Name == u.Name
}

// Implements reports whether the concrete type t satisfies interface iface.
func (t *Type) Implements(iface *Type) bool {
	nativeIface := iface.Rtype.NumMethod() > 0
	isPtr := t.Kind() == reflect.Pointer
	for _, im := range iface.IfaceMethods {
		if mt := t.ResolveMethodType(im.ID); mt != nil && (isPtr || !mt.Methods[im.ID].PtrRecv) {
			m := mt.Methods[im.ID]
			if im.Sig != nil && m.Sig != nil {
				if !sigTypeCompatible(im.Sig, m.Sig) {
					return false
				}
			} else if !sigCompatible(im.Rtype, m.Rtype) {
				return false
			}
			continue
		}
		if nativeIface {
			return t.Rtype.Implements(iface.Rtype)
		}
		// Native concrete type with no mvm Methods: check reflect method set.
		return iface.NativeImplements(t.Rtype)
	}
	return true
}

func sigTypeCompatible(want, have *Type) bool {
	if want == nil || have == nil || want == have {
		return true
	}
	if want.Kind() != reflect.Func || have.Kind() != reflect.Func {
		return true
	}
	return compatibleTypeList(want.Params, have.Params) &&
		compatibleTypeList(want.Returns, have.Returns) &&
		want.IsVariadic() == have.IsVariadic()
}

func compatibleTypeList(a, b []*Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !compatibleType(a[i], b[i]) {
			return false
		}
	}
	return true
}

func compatibleType(a, b *Type) bool {
	if a == b || a == nil || b == nil || a.Identical(b) {
		return true
	}
	if isErasedIface(a) && b.IsInterface() || isErasedIface(b) && a.IsInterface() {
		return true
	}
	if a.Kind() != b.Kind() {
		return false
	}
	if a.Name != "" || b.Name != "" {
		if !a.SameNamedType(b) {
			return false
		}
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Chan:
		return compatibleType(a.ElemType, b.ElemType)
	case reflect.Array:
		return a.Len() == b.Len() && compatibleType(a.ElemType, b.ElemType)
	case reflect.Map:
		return compatibleType(a.KeyType, b.KeyType) && compatibleType(a.ElemType, b.ElemType)
	case reflect.Func:
		return compatibleTypeList(a.Params, b.Params) &&
			compatibleTypeList(a.Returns, b.Returns) &&
			a.IsVariadic() == b.IsVariadic()
	case reflect.Struct:
		return compatibleTypeList(a.Fields, b.Fields)
	}
	return true
}

func isErasedIface(t *Type) bool {
	return t != nil && t.Name == "" && t.IsInterface() && len(t.IfaceMethods) == 0
}

func sigCompatible(want, have reflect.Type) bool {
	if want == nil || have == nil || want == have {
		return true
	}
	if want.Kind() != reflect.Func || have.Kind() != reflect.Func {
		return true
	}
	if want.NumIn() != have.NumIn() || want.NumOut() != have.NumOut() || want.IsVariadic() != have.IsVariadic() {
		return false
	}
	for i := range want.NumIn() {
		if want.In(i) != have.In(i) {
			return false
		}
	}
	for i := range want.NumOut() {
		if want.Out(i) != have.Out(i) {
			return false
		}
	}
	return true
}

func nativeSigCompatible(want, mt reflect.Type, hasRecv bool) bool {
	if want == nil {
		return true
	}
	if want.Kind() != reflect.Func || mt.Kind() != reflect.Func {
		return true
	}
	off := 0
	if hasRecv {
		off = 1
	}
	if mt.NumIn()-off != want.NumIn() || mt.NumOut() != want.NumOut() || mt.IsVariadic() != want.IsVariadic() {
		return false
	}
	for i := range want.NumIn() {
		if !nativeTypeCompatible(want.In(i), mt.In(i+off)) {
			return false
		}
	}
	for i := range want.NumOut() {
		if !nativeTypeCompatible(want.Out(i), mt.Out(i)) {
			return false
		}
	}
	return true
}

func nativeSigCompatibleSym(sig *Type, mt reflect.Type, hasRecv bool) bool {
	if sig == nil || sig.Kind() != reflect.Func || mt.Kind() != reflect.Func {
		return true
	}
	off := 0
	if hasRecv {
		off = 1
	}
	if mt.NumIn()-off != len(sig.Params) || mt.NumOut() != len(sig.Returns) || mt.IsVariadic() != sig.IsVariadic() {
		return false
	}
	for i, p := range sig.Params {
		if !symTypeMatchesNative(p, mt.In(i+off)) {
			return false
		}
	}
	for i, r := range sig.Returns {
		if !symTypeMatchesNative(r, mt.Out(i)) {
			return false
		}
	}
	return true
}

func symTypeMatchesNative(a *Type, have reflect.Type) bool {
	if a == nil || have == nil {
		return true
	}
	ak, hk := a.Kind(), have.Kind()
	if ak == reflect.Interface && hk == reflect.Interface {
		return true
	}
	if ak != hk {
		return false
	}
	switch ak {
	case reflect.Pointer, reflect.Slice, reflect.Chan:
		return symTypeMatchesNative(a.ElemType, have.Elem())
	case reflect.Array:
		return a.Len() == have.Len() && symTypeMatchesNative(a.ElemType, have.Elem())
	case reflect.Map:
		return symTypeMatchesNative(a.KeyType, have.Key()) && symTypeMatchesNative(a.ElemType, have.Elem())
	}
	return true
}

func ifaceMethodMatchesNative(im IfaceMethod, mt reflect.Type, hasRecv bool) bool {
	if im.Rtype != nil {
		return nativeSigCompatible(im.Rtype, mt, hasRecv)
	}
	return nativeSigCompatibleSym(im.Sig, mt, hasRecv)
}

func nativeTypeCompatible(want, have reflect.Type) bool {
	if want == have {
		return true
	}
	if want == nil || have == nil ||
		want.Kind() != reflect.Interface || have.Kind() != reflect.Interface {
		return false
	}
	if want.NumMethod() == 0 || have.NumMethod() == 0 {
		return true
	}
	return want.Implements(have) && have.Implements(want)
}

// IfaceMethodTypes returns the types carrying typ's method set.
func IfaceMethodTypes(typ *Type) (types [6]*Type, n int) {
	push := func(t *Type) {
		if t != nil && n < len(types) {
			types[n] = t
			n++
		}
	}
	for cur := typ; cur != nil; cur = cur.Base {
		push(cur)
		if cur.Kind() == reflect.Pointer {
			push(cur.ElemType)
		}
	}
	return
}

// ResolveMethodType returns the Type whose Methods[id] holds the resolved entry.
func (t *Type) ResolveMethodType(id int) *Type {
	if id < 0 {
		return nil
	}
	types, n := IfaceMethodTypes(t)
	for _, mt := range types[:n] {
		if id < len(mt.Methods) && mt.Methods[id].IsResolved() {
			return mt
		}
	}
	return nil
}

func hasUnexportedIfaceMethod(ms []IfaceMethod) bool {
	for _, m := range ms {
		if len(m.Name) > 0 && !unicode.IsUpper(rune(m.Name[0])) {
			return true
		}
	}
	return false
}

// NativeImplements reports whether rt has all the methods required by interface type t.
func (t *Type) NativeImplements(rt reflect.Type) bool {
	if !t.IsInterface() {
		return false
	}
	return t.MissingMethod(rt) == ""
}

// MissingMethod returns the name of the first missing method that rt does not have.
func (t *Type) MissingMethod(rt reflect.Type) string {
	t.EnsureIfaceMethods()
	// An interface with unexported methods can only be satisfied by native types from the method's own package,
	// and reflect cannot enumerate those methods, so the loops below always report them missing.
	if t.Rtype != nil && t.Rtype != AnyRtype && t.Rtype.Kind() == reflect.Interface &&
		hasUnexportedIfaceMethod(t.IfaceMethods) && rt.Implements(t.Rtype) {
		return ""
	}
	hasRecv := rt.Kind() != reflect.Interface
	for _, im := range t.IfaceMethods {
		m, ok := rt.MethodByName(im.Name)
		if !ok {
			return im.Name
		}
		// Method present by name; signatures must match when known.
		if !ifaceMethodMatchesNative(im, m.Type, hasRecv) {
			return im.Name
		}
	}
	// Fallback: check methods declared on Rtype (for purely native interfaces).
	for m := range t.Rtype.Methods() {
		if _, ok := rt.MethodByName(m.Name); !ok {
			return m.Name
		}
	}
	return ""
}

func (t *Type) String() string {
	if t == nil {
		return "<nil>"
	}
	if t.Name != "" {
		if t.PkgName != "" {
			return t.PkgName + "." + t.Name
		}
		// For native types without PkgPath, use the reflect representation.
		if t.Rtype != nil && t.Rtype.PkgPath() != "" {
			return t.Rtype.String()
		}
		return t.Name
	}
	if t.Rtype != nil {
		return t.Rtype.String()
	}
	return t.symbolicString()
}

func (t *Type) symbolicString() string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + t.ElemType.String()
	case reflect.Slice:
		return "[]" + t.ElemType.String()
	case reflect.Array:
		return "[" + strconv.Itoa(t.ArrayLen) + "]" + t.ElemType.String()
	case reflect.Map:
		return "map[" + t.KeyType.String() + "]" + t.ElemType.String()
	case reflect.Chan:
		switch t.ChanDir {
		case reflect.RecvDir:
			return "<-chan " + t.ElemType.String()
		case reflect.SendDir:
			return "chan<- " + t.ElemType.String()
		default:
			return "chan " + t.ElemType.String()
		}
	case reflect.Func:
		var b strings.Builder
		b.WriteString("func(")
		for i, p := range t.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(p.String())
		}
		b.WriteByte(')')
		switch len(t.Returns) {
		case 0:
		case 1:
			b.WriteByte(' ')
			b.WriteString(t.Returns[0].String())
		default:
			b.WriteString(" (")
			for i, r := range t.Returns {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(r.String())
			}
			b.WriteByte(')')
		}
		return b.String()
	case reflect.Struct:
		var b strings.Builder
		b.WriteString("struct {")
		for i, f := range t.Fields {
			if i > 0 {
				b.WriteByte(';')
			}
			b.WriteByte(' ')
			if f.Name != "" {
				b.WriteString(f.Name)
				b.WriteByte(' ')
			}
			// A field clone carries the field name in Name; render its source
			// type (Base) so distinct anon structs don't share a string key.
			if !f.Defined && f.Base != nil {
				b.WriteString(f.Base.String())
			} else {
				b.WriteString(f.String())
			}
		}
		b.WriteString(" }")
		return b.String()
	}
	return t.Kind().String()
}

// Elem returns a type's element type, preserving mvm-level info.
func (t *Type) Elem() *Type {
	if t.ElemType != nil {
		return t.ElemType
	}
	e := t.Rtype.Elem()
	return &Type{Name: e.Name(), Rtype: e, kind: e.Kind()}
}

// Key returns a map type's key type.
func (t *Type) Key() *Type {
	if t.KeyType != nil {
		return t.KeyType
	}
	k := t.Rtype.Key()
	return &Type{Name: k.Name(), Rtype: k, kind: k.Kind()}
}

// Out returns the type's i'th output parameter.
func (t *Type) Out(i int) *Type {
	o := t.Rtype.Out(i)
	return &Type{Name: o.Name(), Rtype: o, kind: o.Kind()}
}

// ReturnType returns the mvm-level i'th return type if known, else falls back to reflect.
func (t *Type) ReturnType(i int) *Type {
	if i < len(t.Returns) {
		return t.Returns[i]
	}
	if t.Rtype != nil && t.Rtype.Kind() == reflect.Func && i < t.Rtype.NumOut() {
		return t.Out(i)
	}
	return nil
}

// ParamType returns the mvm-level i'th parameter type if known, else falls back to reflect.
func (t *Type) ParamType(i int) *Type {
	if i < len(t.Params) {
		return t.Params[i]
	}
	if t.Rtype != nil && t.Rtype.Kind() == reflect.Func && i < t.Rtype.NumIn() {
		in := t.Rtype.In(i)
		return &Type{Name: in.Name(), Rtype: in, kind: in.Kind()}
	}
	return nil
}

// NumOut returns a func type's number of results.
func (t *Type) NumOut() int {
	if len(t.Returns) > 0 {
		return len(t.Returns)
	}
	if t.Rtype != nil && t.Rtype.Kind() == reflect.Func {
		return t.Rtype.NumOut()
	}
	return 0
}

// NumIn returns a func type's number of parameters.
func (t *Type) NumIn() int {
	if len(t.Params) > 0 {
		return len(t.Params)
	}
	if t.Rtype != nil && t.Rtype.Kind() == reflect.Func {
		return t.Rtype.NumIn()
	}
	return 0
}

// TypeOf returns the mvm type of v.
func TypeOf(v any) *Type {
	t := reflect.TypeOf(v)
	return &Type{Name: t.Name(), Rtype: t, kind: t.Kind()}
}

// SymBasic builds a symbolic type of a basic kind with Rtype unset.
func SymBasic(k reflect.Kind) *Type { return &Type{kind: k} }

// CaptureKind records t's kind in the symbolic field so Kind() survives a caller nilling Rtype to defer materialization.
func (t *Type) CaptureKind() {
	if t.kind == reflect.Invalid && t.Rtype != nil {
		t.kind = t.Rtype.Kind()
	}
}

// SymPtr builds a symbolic *elem with Rtype unset for comp to materialize.
func SymPtr(elem *Type) *Type { return &Type{kind: reflect.Pointer, ElemType: elem} }

// SymSlice builds a symbolic []elem.
func SymSlice(elem *Type) *Type { return &Type{kind: reflect.Slice, ElemType: elem} }

// SymMap builds a symbolic map[key]elem.
func SymMap(key, elem *Type) *Type { return &Type{kind: reflect.Map, KeyType: key, ElemType: elem} }

// SymArray builds a symbolic [n]elem.
func SymArray(n int, elem *Type) *Type {
	return &Type{kind: reflect.Array, ArrayLen: n, ElemType: elem}
}

// SymChan builds a symbolic chan-elem with direction dir.
func SymChan(dir reflect.ChanDir, elem *Type) *Type {
	return &Type{kind: reflect.Chan, ChanDir: dir, ElemType: elem}
}

// SymFunc builds a symbolic func type (Rtype unset).
func SymFunc(arg, ret []*Type, variadic bool) *Type {
	return &Type{kind: reflect.Func, Params: arg, Returns: ret, Variadic: variadic}
}

// SymStruct builds a symbolic struct type (Rtype unset); comp materializes it.
func SymStruct(fields []*Type, embedded []EmbeddedField, tags []string) *Type {
	return &Type{kind: reflect.Struct, Fields: fields, Embedded: embedded, Tags: tags}
}

var (
	funcTypesMu sync.Mutex
	funcTypes   = map[string]*Type{} // memoizes FuncOf by signature fingerprint
)

// FuncOf returns the canonical func type for the given args/results/variadic.
func FuncOf(arg, ret []*Type, variadic bool) *Type {
	key := funcTypeKey(arg, ret, variadic)
	funcTypesMu.Lock()
	defer funcTypesMu.Unlock()
	if t, ok := funcTypes[key]; ok {
		return t
	}
	a := make([]reflect.Type, len(arg))
	for i, e := range arg {
		a[i] = e.Rtype
	}
	r := make([]reflect.Type, len(ret))
	for i, e := range ret {
		r[i] = e.Rtype
	}
	t := &Type{Rtype: reflect.FuncOf(a, r, variadic), kind: reflect.Func, Variadic: variadic, Params: arg, Returns: ret}
	funcTypes[key] = t
	return t
}

func funcTypeKey(arg, ret []*Type, variadic bool) string {
	var b strings.Builder
	b.Grow(int(unsafe.Sizeof(uintptr(0)))*(len(arg)+len(ret)) + 9)
	writeUint32(&b, uint32(len(arg)))
	for _, t := range arg {
		writePtr(&b, t)
	}
	writeUint32(&b, uint32(len(ret)))
	for _, t := range ret {
		writePtr(&b, t)
	}
	if variadic {
		b.WriteByte(1)
	} else {
		b.WriteByte(0)
	}
	return b.String()
}

func writeUint32(b *strings.Builder, v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	b.Write(buf[:])
}

func writePtr(b *strings.Builder, p *Type) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(uintptr(unsafe.Pointer(p))))
	b.Write(buf[:])
}

var (
	structTypesMu sync.Mutex
	structTypes   = map[string]*Type{} // memoization of StructOf. Process lifetime
)

func methodlessLayout(rt reflect.Type) reflect.Type { return methodlessLayoutAt(rt, 0) }

func methodlessLayoutAt(rt reflect.Type, depth int) reflect.Type {
	if depth > 32 {
		return rt // self-referential embed; bail and let the demote-all fallback handle it
	}
	switch rt.Kind() {
	case reflect.Pointer:
		if elem := methodlessStruct(rt.Elem(), depth+1); elem != nil {
			return reflect.PointerTo(elem)
		}
		return rt
	case reflect.Struct:
		if out := methodlessStruct(rt, depth+1); out != nil {
			return out
		}
		return rt
	default:
		return rt
	}
}

func methodlessStruct(rt reflect.Type, depth int) reflect.Type {
	if rt.Kind() != reflect.Struct {
		return nil
	}
	rf := make([]reflect.StructField, rt.NumField())
	for i := range rf {
		rf[i] = rt.Field(i)
		if rf[i].Anonymous {
			rf[i].Type = methodlessLayoutAt(rf[i].Type, depth)
		}
	}
	if out, ok := tryStructOf(rf); ok {
		return out
	}
	return nil
}

func embedFieldHasMethods(f *Type, rt reflect.Type) bool {
	if rt != nil && rt.NumMethod() > 0 {
		return true
	}
	e := f
	if e != nil && e.IsPtr() && e.ElemType != nil {
		e = e.ElemType
	}
	for ; e != nil; e = e.Base {
		if len(e.Methods) > 0 {
			return true
		}
	}
	return false
}

// StructOf returns the canonical struct type for the given fields/embedded/tags.
func StructOf(fields []*Type, embedded []EmbeddedField, tags []string) *Type {
	key := structTypeKey(fields, embedded, tags)
	structTypesMu.Lock()
	defer structTypesMu.Unlock()
	if t, ok := structTypes[key]; ok {
		return t
	}
	t := &Type{Rtype: buildStructRtype(fields, embedded, tags, false), kind: reflect.Struct, Embedded: embedded, Fields: fields, Tags: tags}
	structTypes[key] = t
	return t
}

// ReserveStruct returns the memoized Type for an anonymous struct shape.
func ReserveStruct(fields []*Type, embedded []EmbeddedField, tags []string) (t *Type, fresh bool) {
	key := structTypeKey(fields, embedded, tags)
	structTypesMu.Lock()
	defer structTypesMu.Unlock()
	if t, ok := structTypes[key]; ok {
		return t, false
	}
	t = &Type{Rtype: NewPlaceholderRtype(""), kind: reflect.Struct, Embedded: embedded, Fields: fields, Tags: tags}
	structTypes[key] = t
	return t, true
}

// FinalizeStruct patches a reserved struct's placeholder rtype in place with the real layout from its field rtypes.
func FinalizeStruct(t *Type) reflect.Type {
	layout := buildStructRtype(t.Fields, t.Embedded, t.Tags, false)
	patchRtype(t.Rtype, layout)
	return layout
}

// BuildStructRtype builds the reflect struct rtype for the given fields.
func BuildStructRtype(fields []*Type, embedded []EmbeddedField, tags []string) reflect.Type {
	return buildStructRtype(fields, embedded, tags, false)
}

// BuildStructRtypeKeepIface is BuildStructRtype but keeps a non-embedded native non-empty interface field as iface.
func BuildStructRtypeKeepIface(fields []*Type, embedded []EmbeddedField, tags []string) reflect.Type {
	return buildStructRtype(fields, embedded, tags, true)
}

// UnreserveStruct drops a reservation whose fields could not be materialized.
func UnreserveStruct(fields []*Type, embedded []EmbeddedField, tags []string) {
	key := structTypeKey(fields, embedded, tags)
	structTypesMu.Lock()
	delete(structTypes, key)
	structTypesMu.Unlock()
}

func buildStructRtype(fields []*Type, embedded []EmbeddedField, tags []string, keepIface bool) reflect.Type {
	rf := make([]reflect.StructField, len(fields))
	embSet := make(map[int]bool, len(embedded))
	for _, e := range embedded {
		embSet[e.FieldIdx] = true
	}
	// Embeds demoted to a methodless layout below (to satisfy reflect.StructOf);
	// after building we restore the canonical named type so reflect identity holds.
	var restore map[int]reflect.Type
	// Find a consistent PkgPath for all unexported fields.
	// reflect.StructOf requires all unexported fields to share the same PkgPath.
	pkgPath := "builtin"
	for _, f := range fields {
		if f.PkgName != "" {
			pkgPath = f.PkgName
			break
		}
	}
	for i, f := range fields {
		// Backstop for the blank-field marker: a synthesized blank field name
		// carries '~' (goparser blankName), which reflect.StructOf rejects.
		// goparser strips it when it builds the *Type, so the name is usually
		// already clean; sanitize here too since this is the single point every
		// struct rtype passes through, whatever path produced the *Type.
		name := f.Name
		if strings.IndexByte(name, '~') >= 0 {
			name = strings.ReplaceAll(name, "~", "")
		}
		rf[i].Name = name
		rf[i].PkgPath = f.PkgName
		if i < len(tags) {
			rf[i].Tag = reflect.StructTag(tags[i])
		}
		// Interface fields use interface{} so vm.Iface values can be stored via reflect.Set.
		// Exception: an embedded NATIVE non-empty interface keeps its real rtype so the struct
		// satisfies that interface via method promotion at the native boundary.
		switch {
		case f.Kind() != reflect.Interface:
			rf[i].Type = f.Rtype
			// mvm stores interfaces as eface, so &errField is *interface{}; a *error field must match.
			if ft := f.Rtype; ft != nil && ft.Kind() == reflect.Pointer &&
				ft.Elem().Kind() == reflect.Interface && ft.Elem() != AnyRtype {
				rf[i].Type = reflect.PointerTo(AnyRtype)
			}
		case embSet[i] && f.Rtype != nil && f.Rtype.NumMethod() > 0:
			rf[i].Type = f.Rtype
		case keepIface && f.Rtype != nil && f.Rtype.Kind() == reflect.Interface && f.Rtype.NumMethod() > 0:
			rf[i].Type = f.Rtype // native non-empty interface kept as iface, not erased
		default:
			rf[i].Type = AnyRtype
		}

		switch {
		case embSet[i] && len(f.Name) > 0 && !unicode.IsUpper(rune(f.Name[0])):
			// An unexported embed needs a PkgPath, but reflect.StructOf rejects an
			// anonymous field with PkgPath set, so it cannot stay Anonymous: its
			// promoted fields are unreachable via reflect (a StructOf limitation).
			if rf[i].PkgPath == "" {
				rf[i].PkgPath = pkgPath
			}
		case embSet[i] && rf[i].Type != nil && rf[i].Type.Kind() != reflect.Interface &&
			(i > 0 || rf[i].Type.Kind() == reflect.Pointer) &&
			(embedFieldHasMethods(f, rf[i].Type) || runtype.EmbedTripsStructOf(rf[i].Type)):
			// Interfaces are excluded: StructOf promotes them at any position, and the
			// dance's clone would strip that method set.
			// reflect.StructOf only promotes a method-bearing VALUE embed at field 0; a pointer
			// embed (e.g. gorm IndexOption's *Field) panics there too, and any embed past 0 panics.
			// Give it a methodless layout-equivalent so the field stays Anonymous (json/fmt flatten it); mvm promotes the methods itself.
			// That layout is unnamed, so reflect would report this field's type -- and box a
			// direct field read into an interface -- as *struct{...} not the canonical named
			// type, diverging from native Go and breaking reflect.DeepEqual/==. Record the
			// canonical type to repoint the built field at after StructOf (see CloneStructLayoutWithFields).
			if rf[i].Type != nil {
				if restore == nil {
					restore = map[int]reflect.Type{}
				}
				restore[i] = rf[i].Type
			}
			rf[i].Type = methodlessLayout(rf[i].Type)
			rf[i].Anonymous = true
		default:
			rf[i].Anonymous = embSet[i]
		}
	}
	if rt, ok := tryStructOf(rf); ok {
		if len(restore) > 0 {
			rt = runtype.CloneStructLayoutWithFields(rt, restore)
		}
		return rt
	}
	// mvm promotes embedded methods itself, so retry with the embeds demoted to named fields.
	// A non-anonymous field never trips StructOf's promotion check, so the demoted embeds
	// keep their canonical method-bearing type (restoring reflect identity) directly.
	for i := range rf {
		rf[i].Anonymous = false
		if rt, ok := restore[i]; ok {
			rf[i].Type = rt
		}
	}
	return reflect.StructOf(rf)
}

func tryStructOf(rf []reflect.StructField) (rt reflect.Type, ok bool) {
	defer func() {
		if recover() != nil {
			rt, ok = nil, false
		}
	}()
	return reflect.StructOf(rf), true
}

func structTypeKey(fields []*Type, embedded []EmbeddedField, tags []string) string {
	var b strings.Builder
	writeUint32(&b, uint32(len(fields)))
	for _, f := range fields {
		writeString(&b, f.Name)
		writeString(&b, f.PkgName)
		base := f.Base
		if base == nil {
			base = f
		}
		writePtr(&b, base)
	}
	writeUint32(&b, uint32(len(embedded)))
	for _, e := range embedded {
		writeUint32(&b, uint32(e.FieldIdx))
		writePtr(&b, e.Type)
	}
	writeUint32(&b, uint32(len(tags)))
	for _, s := range tags {
		writeString(&b, s)
	}
	return b.String()
}

func writeString(b *strings.Builder, s string) {
	writeUint32(b, uint32(len(s)))
	b.WriteString(s)
}

// FieldIndex returns the index of struct field name.
func (t *Type) FieldIndex(name string) []int {
	if t.Rtype == nil {
		idx, _ := t.symFieldLookup(name)
		return idx
	}
	for _, f := range reflect.VisibleFields(t.Rtype) {
		if f.Name == name {
			return f.Index
		}
	}
	idx, _ := t.embeddedFieldLookup(name)
	return idx
}

// symField is one entry of a symbolic visible-field walk: a field's name, its
// reflect-style index path, and its mvm field type.
type symField struct {
	name  string
	index []int
	typ   *Type
}

func (t *Type) structView() *Type {
	for t != nil && len(t.Fields) == 0 && t.Base != nil && t.Base != t {
		t = t.Base
	}
	return t
}

func (t *Type) symVisibleFields() []symField {
	type item struct {
		t     *Type
		index []int
	}
	var out []symField
	resolved := map[string]bool{}
	seen := map[*Type]bool{t: true}
	current := []item{{t, nil}}
	for len(current) > 0 {
		var next []item
		var level []symField
		count := map[string]int{}
		for _, it := range current {
			st := it.t
			if st != nil && st.Kind() == reflect.Pointer {
				st = st.ElemType
			}
			if st == nil || st.Kind() != reflect.Struct {
				continue
			}
			embSet := make(map[int]bool, len(st.Embedded))
			for _, e := range st.Embedded {
				embSet[e.FieldIdx] = true
			}
			for i, f := range st.Fields {
				idx := append(append([]int{}, it.index...), i)
				level = append(level, symField{name: f.Name, index: idx, typ: f})
				count[f.Name]++
				if embSet[i] {
					ft := f
					if ft != nil && ft.Kind() == reflect.Pointer {
						ft = ft.ElemType
					}
					ft = ft.structView()
					if ft != nil && !seen[ft] {
						seen[ft] = true
						next = append(next, item{ft, idx})
					}
				}
			}
		}
		for _, sf := range level {
			if !resolved[sf.name] && count[sf.name] == 1 {
				out = append(out, sf)
			}
		}
		for name := range count {
			resolved[name] = true
		}
		current = next
	}
	return out
}

func (t *Type) symFieldLookup(name string) ([]int, *Type) {
	if len(t.Fields) == 0 && t.Base != nil && t.Base != t {
		return t.Base.FieldLookup(name)
	}
	for _, sf := range t.symVisibleFields() {
		if sf.name != name {
			continue
		}
		if ft := t.resolveFieldByPath(sf.index); ft != nil {
			if ft.Base != nil && ft.Base.Name != "" {
				// The clone's own Name/PkgName are the field's, not the type's;
				// recover the field TYPE's identity from Base.
				ft.Name = ft.Base.Name
				ft.PkgName = ft.Base.PkgName
			} else {
				ft.Name = ""
				ft.PkgName = sf.typ.PkgName
			}
			return sf.index, ft
		}
		return sf.index, &Type{Name: sf.typ.Name, PkgName: sf.typ.PkgName, Rtype: sf.typ.Rtype, kind: sf.typ.Kind()}
	}
	return nil, nil
}

// FieldTypeAtPath returns the type of the field reached by the index path within struct t.
func (t *Type) FieldTypeAtPath(path []int) *Type {
	cur := t
	for _, idx := range path {
		if cur == nil {
			return nil
		}
		if cur.Kind() == reflect.Pointer {
			cur = cur.Elem()
		}
		// A field-access clone can drop Fields/Rtype; resolve via its canonical (Base).
		for cur != nil && cur.Rtype == nil && (idx >= len(cur.Fields) || cur.Fields[idx] == nil) && cur.Base != nil {
			cur = cur.Base
			if cur != nil && cur.Kind() == reflect.Pointer {
				cur = cur.Elem()
			}
		}
		if cur == nil {
			return nil
		}
		switch {
		case idx < len(cur.Fields) && cur.Fields[idx] != nil:
			cur = cur.Fields[idx]
		case cur.Rtype != nil && cur.Kind() == reflect.Struct && idx < cur.Rtype.NumField():
			f := cur.Rtype.Field(idx)
			cur = &Type{Name: f.Type.Name(), Rtype: f.Type, kind: f.Type.Kind()}
		default:
			return nil
		}
	}
	return cur
}

// FieldType returns the type of struct field name, using mvm-level info when available.
func (t *Type) FieldType(name string) *Type {
	_, ft := t.FieldLookup(name)
	return ft
}

// FieldLookup returns the index path and type of struct field name in a single pass.
func (t *Type) FieldLookup(name string) ([]int, *Type) {
	return t.fieldLookupSeen(name, nil)
}

// fieldLookupSeen is FieldLookup threading a visited set through the symbolic
// embedded-field recursion, so mutually-embedding types (legal Go: `type A
// struct{ *B }; type B struct{ *A }`) cannot loop forever.
func (t *Type) fieldLookupSeen(name string, seen map[*Type]bool) ([]int, *Type) {
	if t.Rtype == nil {
		return t.symFieldLookup(name)
	}
	for _, f := range reflect.VisibleFields(t.Rtype) {
		if f.Name != name {
			continue
		}
		// Walk t.Fields/Embedded by f.Index to recover mvm-level info at the deepest field.
		if ft := t.resolveFieldByPath(f.Index); ft != nil {
			// Use the type's name+pkgpath, so method lookup works.
			if ft.Base != nil && ft.Base.Name != "" {
				ft.Name = ft.Base.Name
				ft.PkgName = ft.Base.PkgName
			} else {
				ft.Name = f.Type.Name()
				ft.PkgName = f.Type.PkgPath()
			}
			return f.Index, ft
		}
		// No mvm info: identify by the field's TYPE (name+package), like above.
		return f.Index, &Type{Name: f.Type.Name(), PkgName: f.Type.PkgPath(), Rtype: f.Type, kind: f.Type.Kind()}
	}
	return t.embeddedFieldLookupSeen(name, seen)
}

func (t *Type) resolveFieldByPath(path []int) *Type {
	if t == nil || len(path) == 0 {
		return nil
	}
	head := path[0]
	rest := path[1:]
	var sub *Type
	if head < len(t.Fields) {
		sub = t.Fields[head]
	}
	if sub == nil {
		return nil
	}
	if len(rest) == 0 {
		clone := *sub
		// A field type cloned at parse time can be an empty forward placeholder.
		// Its Base now holds the materialized type, so adopt Base's structure.
		if clone.Kind() == reflect.Invalid && clone.Base != nil && clone.Base != sub && clone.Base.Kind() != reflect.Invalid {
			adopted := *clone.Base
			adopted.Base = clone.Base
			return &adopted
		}
		return &clone
	}
	next := sub
	if next.IsPtr() && next.ElemType != nil {
		next = next.ElemType
	}
	next = next.structView()
	return next.resolveFieldByPath(rest)
}

func (t *Type) embeddedFieldLookup(name string) ([]int, *Type) {
	return t.embeddedFieldLookupSeen(name, nil)
}

func (t *Type) embeddedFieldLookupSeen(name string, seen map[*Type]bool) ([]int, *Type) {
	if seen[t] {
		return nil, nil
	}
	if seen == nil {
		seen = map[*Type]bool{}
	}
	seen[t] = true
	for _, emb := range t.Embedded {
		rt := t.Rtype.Field(emb.FieldIdx).Type
		if rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			continue
		}
		if sf, ok := rt.FieldByName(name); ok {
			idx := append([]int{emb.FieldIdx}, sf.Index...)
			if emb.Type != nil {
				if _, ft := emb.Type.fieldLookupSeen(name, seen); ft != nil {
					return idx, ft
				}
			}
			// Identify by the field's TYPE (name+package), like FieldLookup.
			return idx, &Type{Name: sf.Type.Name(), PkgName: sf.Type.PkgPath(), Rtype: sf.Type, kind: sf.Type.Kind()}
		}
		// reflect.FieldByName cannot promote past a deeper embedded pointer whose
		// synth rtype dropped the Anonymous flag (a cross-package chain such as gorm
		// Statement -> *DB -> *Config). Recurse symbolically through emb.Type's own
		// embedding chain, which carries the field info reflect lost.
		if emb.Type != nil {
			if sub, ft := emb.Type.fieldLookupSeen(name, seen); ft != nil {
				return append([]int{emb.FieldIdx}, sub...), ft
			}
		}
	}
	return nil, nil
}

// IsPtr returns true if type t is of pointer kind.
func (t *Type) IsPtr() bool { return t.Kind() == reflect.Pointer }

// IsStruct returns true if type t is of struct kind.
func (t *Type) IsStruct() bool { return t != nil && t.Kind() == reflect.Struct }

// IsSlice returns true if type t is of slice kind.
func (t *Type) IsSlice() bool { return t != nil && t.Kind() == reflect.Slice }

// IsFunc returns true if type t is of func kind.
func (t *Type) IsFunc() bool { return t != nil && t.Kind() == reflect.Func }

// Len returns an array type's length, from the symbolic graph when no rtype is materialized yet.
func (t *Type) Len() int {
	if t.Rtype != nil {
		return t.Rtype.Len()
	}
	return t.ArrayLen
}

// IsVariadic reports whether a func type's final parameter is variadic.
func (t *Type) IsVariadic() bool {
	if t.Rtype != nil {
		return t.Rtype.IsVariadic()
	}
	return t.Variadic
}

// IsComparable reports whether values of t may be compared with == / !=.
func (t *Type) IsComparable() bool {
	if t.Rtype != nil {
		return t.Rtype.Comparable()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Map, reflect.Func:
		return false
	case reflect.Array:
		return t.ElemType == nil || t.ElemType.IsComparable()
	case reflect.Struct:
		for _, f := range t.Fields {
			if !f.IsComparable() {
				return false
			}
		}
		return true
	}
	return true
}

// SameNamedType reports whether t and u are the same named Go type.
func (t *Type) SameNamedType(u *Type) bool {
	if t.Name != u.Name {
		return false
	}
	if t.ImportPath != "" && u.ImportPath != "" {
		return t.ImportPath == u.ImportPath
	}
	return t.PkgName == u.PkgName
}

// Identical reports whether t and u denote the same Go type.
func (t *Type) Identical(u *Type) bool {
	if t == u {
		return true
	}
	if t == nil || u == nil {
		return false
	}
	// Unequal rtypes are not conclusive: one symbolic shape can materialize
	// twice with different precision, so an unnamed pair falls through to structural identity.
	// A named pair must stem from one declaration (common Base root);
	// distinct declarations sharing Name+PkgName stay distinct.
	if t.Rtype != nil && u.Rtype != nil {
		if t.Rtype == u.Rtype {
			return true
		}
		if t.Name != "" || u.Name != "" {
			return baseRoot(t) == baseRoot(u)
		}
	}
	if t.Kind() != u.Kind() {
		return false
	}
	if t.Name != "" || u.Name != "" {
		return t.SameNamedType(u)
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Chan:
		return t.ElemType.Identical(u.ElemType)
	case reflect.Array:
		return t.Len() == u.Len() && t.ElemType.Identical(u.ElemType)
	case reflect.Map:
		return t.KeyType.Identical(u.KeyType) && t.ElemType.Identical(u.ElemType)
	case reflect.Func:
		return identicalTypes(t.Params, u.Params) && identicalTypes(t.Returns, u.Returns) && t.IsVariadic() == u.IsVariadic()
	case reflect.Struct:
		return identicalTypes(t.Fields, u.Fields)
	}
	return true // identical basic kinds
}

func baseRoot(t *Type) *Type {
	for i := 0; i < 1024 && t != nil && t.Base != nil; i++ {
		t = t.Base
	}
	return t
}

func identicalTypes(a, b []*Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Identical(b[i]) {
			return false
		}
	}
	return true
}

var ptrSize = unsafe.Sizeof(uintptr(0)) // machine word size.

type layout struct{ size, align uintptr }

// Size returns the number of bytes a value of t occupies.
func (t *Type) Size() uintptr {
	if t.Rtype != nil {
		return t.Rtype.Size()
	}
	return t.layout().size
}

// Align returns t's required alignment in bytes (see Size).
func (t *Type) Align() int {
	if t.Rtype != nil {
		return t.Rtype.Align()
	}
	return int(t.layout().align)
}

func (t *Type) layout() layout {
	switch t.Kind() {
	case reflect.Bool, reflect.Int8, reflect.Uint8:
		return layout{1, 1}
	case reflect.Int16, reflect.Uint16:
		return layout{2, 2}
	case reflect.Int32, reflect.Uint32, reflect.Float32:
		return layout{4, 4}
	case reflect.Int64, reflect.Uint64, reflect.Float64:
		return layout{8, 8}
	case reflect.Complex64:
		return layout{8, 4}
	case reflect.Complex128:
		return layout{16, 8}
	case reflect.Int, reflect.Uint, reflect.Uintptr,
		reflect.Pointer, reflect.UnsafePointer, reflect.Chan, reflect.Map, reflect.Func:
		return layout{ptrSize, ptrSize}
	case reflect.String, reflect.Interface:
		return layout{2 * ptrSize, ptrSize}
	case reflect.Slice:
		return layout{3 * ptrSize, ptrSize}
	case reflect.Array:
		el := t.ElemType.layout()
		return layout{el.size * uintptr(t.ArrayLen), el.align}
	case reflect.Struct:
		return structLayout(t.Fields)
	}
	return layout{}
}

func structLayout(fields []*Type) layout {
	off, maxAlign := uintptr(0), uintptr(1)
	lastZero := false
	for _, f := range fields {
		fl := f.layout()
		if fl.align == 0 {
			fl.align = 1
		}
		off = alignUp(off, fl.align) + fl.size
		if fl.align > maxAlign {
			maxAlign = fl.align
		}
		lastZero = fl.size == 0
	}
	if lastZero && off > 0 {
		off++
	}
	return layout{alignUp(off, maxAlign), maxAlign}
}

// FieldOffset returns the byte offset of the field reached by the index path within struct t.
func (t *Type) FieldOffset(path []int) uintptr {
	var off uintptr
	cur := t
	for _, i := range path {
		if cur != nil && cur.Kind() == reflect.Pointer {
			cur = cur.ElemType
		}
		if cur == nil || i >= len(cur.Fields) {
			break
		}
		off += cur.fieldOffsetAt(i)
		cur = cur.Fields[i]
	}
	return off
}

func (t *Type) fieldOffsetAt(i int) uintptr {
	off := uintptr(0)
	for j := 0; j < len(t.Fields); j++ {
		fl := t.Fields[j].layout()
		if fl.align == 0 {
			fl.align = 1
		}
		off = alignUp(off, fl.align)
		if j == i {
			return off
		}
		off += fl.size
	}
	return off
}

func alignUp(n, a uintptr) uintptr {
	if a == 0 {
		return n
	}
	return (n + a - 1) &^ (a - 1)
}
