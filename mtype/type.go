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
)

// Method records a method's code location and receiver path for interface dispatch.
type Method struct {
	Index      int          // data index of code address (-1 if unset or EmbedIface)
	Path       []int        // field index path to embedded receiver (nil = direct, []int{} = deref only)
	EmbedIface bool         // Path leads to an embedded interface field; dispatch through it
	PtrRecv    bool         // true if the method has a pointer receiver (e.g. *T)
	Rtype      reflect.Type // bound method signature (no receiver); nil if unknown. Per-type, so it disambiguates same-named methods (e.g. Unwrap() error vs Unwrap() []error) that share a global method ID.
	Sig        *Type        // symbolic bound method signature (no receiver); the materialize-time source of Rtype. Preferred over Rtype for signature comparison when both methods carry it.
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
	PkgPath      string
	Name         string
	Rtype        reflect.Type
	kind         reflect.Kind    // symbolic kind, set at construction; see Kind
	Placeholder  bool            // true for forward-declared struct/interface placeholders until finalized (SetFields, or IfaceMethods assignment in parseTypeLine)
	IfaceMethods []IfaceMethod   // non-nil for interface types: required method signatures
	TypeElems    []TypeElem      // non-nil for constraint interfaces: union members (e.g. cmp.Ordered)
	Comparable   bool            // constraint interface embeds the built-in `comparable` (e.g. `interface { comparable; error }`)
	Methods      []Method        // concrete types: methods[methodID] = code location + receiver path
	Embedded     []EmbeddedField // mvm types of anonymous (embedded) fields, for promoted method lookup
	Params       []*Type         // mvm-level parameter types for func types (nil for non-func or if unknown)
	Returns      []*Type         // mvm-level return types for func types (nil for non-func or if unknown)
	Fields       []*Type         // mvm-level field types for struct types, parallel to reflect visible fields
	ElemType     *Type           // mvm-level element type for map/slice/array/pointer/chan types
	KeyType      *Type           // mvm-level key type for map types; nil for non-maps or native-built maps
	// Symbolic descriptors of what Rtype otherwise carries, so a type can be
	// materialized (Rtype built) from the symbolic graph alone (see comp materialize).
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

// Kind returns t's kind. It prefers the symbolic kind set at construction and
// falls back to the rtype, which lets parse-time dispatch move off Rtype while
// construction sites are migrated to populate kind. The goal is that, once
// every constructor sets kind, the rtype need not exist before comp.
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

// TypeElem describes one member of a constraint interface's type-element union,
// e.g. for "type Ordered interface { ~int | ~string }" the type elements are
// TypeElem{Approx: true, Type: intType}, TypeElem{Approx: true, Type: stringType}.
// Approx encodes the "~" prefix (any type whose underlying type is Type).
type TypeElem struct {
	Approx bool
	Type   *Type
}

// AnyRtype is the reflect.Type for the empty interface (any).
var AnyRtype = reflect.TypeOf((*any)(nil)).Elem()

// IsInterface reports whether t represents an interface type.
func (t *Type) IsInterface() bool {
	return t != nil && t.Kind() == reflect.Interface
}

// EnsureIfaceMethods populates IfaceMethods from the reflect method set
// if not already set. This covers native interface types (e.g. io.Reader)
// whose method sets were not explicitly enumerated at parse time.
func (t *Type) EnsureIfaceMethods() {
	if len(t.IfaceMethods) > 0 || t.Kind() != reflect.Interface {
		return
	}
	for i := range t.Rtype.NumMethod() {
		m := t.Rtype.Method(i)
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
// iface.IfaceMethods must have IDs populated (by the compiler) before calling this.
func (t *Type) Implements(iface *Type) bool {
	// Native interface types (e.g. io.Reader) have their method set in Rtype,
	// so reflect can check implementation.
	nativeIface := iface.Rtype.NumMethod() > 0
	// A ptr-receiver method (registered on value type T) only satisfies the
	// interface when t is itself a pointer, per Go's method-set rule.
	isPtr := t.Kind() == reflect.Pointer
	for _, im := range iface.IfaceMethods {
		if mt := t.ResolveMethodType(im.ID); mt != nil && (isPtr || !mt.Methods[im.ID].PtrRecv) {
			// Method IDs are global by name; require matching signatures so e.g.
			// Unwrap() []error does not satisfy interface{ Unwrap() error }.
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

// sigTypeCompatible reports whether two receiver-free signatures, expressed as
// symbolic func *Types, match. Lenient: a nil (unknown) side matches.
func sigTypeCompatible(want, have *Type) bool {
	if want == nil || have == nil || want == have {
		return true
	}
	if want.Kind() != reflect.Func || have.Kind() != reflect.Func {
		return true
	}
	return identicalTypes(want.Params, have.Params) &&
		identicalTypes(want.Returns, have.Returns) &&
		want.IsVariadic() == have.IsVariadic()
}

// sigCompatible reports whether two receiver-free method signatures match.
// Lenient: a nil (unknown) signature on either side matches.
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

// nativeSigCompatible reports whether a native method's signature mt satisfies
// the interface's receiver-free required signature want. mt carries the
// receiver as In(0) when hasRecv (rt is a concrete type, not an interface).
// Lenient: a nil want matches.
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
		if want.In(i) != mt.In(i+off) {
			return false
		}
	}
	for i := range want.NumOut() {
		if want.Out(i) != mt.Out(i) {
			return false
		}
	}
	return true
}

// IfaceMethodTypes returns the types carrying typ's method set: typ, its
// ElemType (pointers register methods on T not *T), and the same along the Base
// chain (so a struct-field copy reaches methods registered on its source).
// The [6] bound suffices: Base chains are collapsed to depth 1 at creation.
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

// ResolveMethodType returns the Type whose Methods[id] holds the resolved entry,
// scanning typ, its ElemType, and the Base chain (via IfaceMethodTypes).
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

// NativeImplements reports whether native reflect type rt has all the methods
// required by interface type t.
func (t *Type) NativeImplements(rt reflect.Type) bool {
	if !t.IsInterface() {
		return false
	}
	return t.MissingMethod(rt) == ""
}

// MissingMethod returns the name of the first method required by interface
// type t that native reflect type rt does not have. Returns "" if all methods
// are present or t has no IfaceMethods.
func (t *Type) MissingMethod(rt reflect.Type) string {
	t.EnsureIfaceMethods()
	hasRecv := rt.Kind() != reflect.Interface
	for _, im := range t.IfaceMethods {
		m, ok := rt.MethodByName(im.Name)
		if !ok {
			return im.Name
		}
		// Method present by name; if both signatures are known they must match
		// so e.g. Unwrap() []error does not satisfy interface{ Unwrap() error }.
		if !nativeSigCompatible(im.Rtype, m.Type, hasRecv) {
			return im.Name
		}
	}
	// Fallback: check methods declared on Rtype (for purely native interfaces).
	for i := range t.Rtype.NumMethod() {
		m := t.Rtype.Method(i)
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
		if t.PkgPath != "" {
			return t.PkgPath + "." + t.Name
		}
		// For native types without an explicit PkgPath, use the reflect
		// representation which includes the package qualifier (e.g. "http.Pusher").
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

// symbolicString renders an unnamed composite from the symbolic graph, for use
// before an rtype is materialized. Basic kinds fall back to the kind name.
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
			b.WriteString(f.String())
		}
		b.WriteString(" }")
		return b.String()
	}
	return t.Kind().String()
}

// Elem returns a type's element type, preserving mvm-level info (e.g. IfaceMethods).
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

// ReturnType returns the mvm-level i'th return type if known, else falls back
// to reflect. Returns nil when i is out of range.
func (t *Type) ReturnType(i int) *Type {
	if i < len(t.Returns) {
		return t.Returns[i]
	}
	if t.Rtype != nil && t.Rtype.Kind() == reflect.Func && i < t.Rtype.NumOut() {
		return t.Out(i)
	}
	return nil
}

// ParamType returns the mvm-level i'th parameter type if known, else falls
// back to reflect. Returns nil when i is out of range. Symmetric with
// ReturnType; used by generic inference to walk a func-typed parameter whose
// reflect-derived bridge form has an empty Params slice.
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

// NumOut returns a func type's number of results, from the symbolic Returns
// slice when populated, else from reflect. Symmetric with ReturnType.
func (t *Type) NumOut() int {
	if len(t.Returns) > 0 {
		return len(t.Returns)
	}
	if t.Rtype != nil && t.Rtype.Kind() == reflect.Func {
		return t.Rtype.NumOut()
	}
	return 0
}

// NumIn returns a func type's number of parameters, from the symbolic Params
// slice when populated, else from reflect. Symmetric with ParamType.
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

// CaptureKind records t's kind in the symbolic field so Kind() survives a
// caller nilling Rtype to defer materialization.
func (t *Type) CaptureKind() {
	if t.kind == reflect.Invalid && t.Rtype != nil {
		t.kind = t.Rtype.Kind()
	}
}

// SymPtr builds a symbolic *elem with Rtype unset for comp to materialize (see
// vm.MaterializeRtype); SymSlice/SymMap/SymArray/SymChan are the parse-time
// counterparts to vm's rtype-building PointerTo/SliceOf/... .
func SymPtr(elem *Type) *Type { return &Type{Name: elem.Name, kind: reflect.Pointer, ElemType: elem} }

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

// SymFunc builds a symbolic func type (Rtype unset); comp materializes it.
func SymFunc(arg, ret []*Type, variadic bool) *Type {
	return &Type{kind: reflect.Func, Params: arg, Returns: ret, Variadic: variadic}
}

// SymStruct builds a symbolic struct type (Rtype unset); comp materializes it.
func SymStruct(fields []*Type, embedded []EmbeddedField, tags []string) *Type {
	return &Type{kind: reflect.Struct, Fields: fields, Embedded: embedded, Tags: tags}
}

// funcTypes memoizes FuncOf by signature fingerprint; entries hold their input
// *Types so keys stay valid. Guarded by funcTypesMu (uncontended per Compiler).
var (
	funcTypesMu sync.Mutex
	funcTypes   = map[string]*Type{}
)

// FuncOf returns the canonical func type for the given args/results/variadic;
// repeated identical calls return the same *Type. Callers must not mutate the
// returned Params/Returns (they alias the cached slices).
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

// structTypes is the global registry for StructOf memoization.
// Keys are structural fingerprints (per-field {Name, PkgPath, Base-or-self
// pointer}, embedded list, tag strings).
// Persistence is process-lifetime; cached entries hold the input slices.
var (
	structTypesMu sync.Mutex
	structTypes   = map[string]*Type{}
)

// StructOf returns the canonical struct type for the given fields/embedded/tags,
// memoized on a structural key (Name+PkgPath+Base-or-self pointer per field) so
// equivalent shapes parsed separately converge despite per-call field clones.
func StructOf(fields []*Type, embedded []EmbeddedField, tags []string) *Type {
	key := structTypeKey(fields, embedded, tags)
	structTypesMu.Lock()
	defer structTypesMu.Unlock()
	if t, ok := structTypes[key]; ok {
		return t
	}
	rf := make([]reflect.StructField, len(fields))
	embSet := make(map[int]bool, len(embedded))
	for _, e := range embedded {
		embSet[e.FieldIdx] = true
	}
	// Find a consistent PkgPath for all unexported fields.
	// reflect.StructOf requires all unexported fields to share the same PkgPath.
	pkgPath := "builtin"
	for _, f := range fields {
		if f.PkgPath != "" {
			pkgPath = f.PkgPath
			break
		}
	}
	for i, f := range fields {
		rf[i].Name = f.Name
		rf[i].PkgPath = f.PkgPath
		if i < len(tags) {
			rf[i].Tag = reflect.StructTag(tags[i])
		}
		// Interface fields use interface{} so vm.Iface values can be stored via
		// reflect.Set. Exception: an embedded NATIVE non-empty interface (e.g.
		// struct{ io.Reader }) keeps its real rtype so the struct satisfies that
		// interface via method promotion at the native boundary.
		switch {
		case f.Kind() != reflect.Interface:
			rf[i].Type = f.Rtype
		case embSet[i] && f.Rtype != nil && f.Rtype.NumMethod() > 0:
			rf[i].Type = f.Rtype
		default:
			rf[i].Type = AnyRtype
		}
		// reflect.StructOf panics on an unexported anonymous field with empty
		// PkgPath, and on an anonymous method-bearing field in a multi-field
		// struct. Avoid Anonymous in those cases; mvm's Embedded tracking covers
		// promoted lookup.
		switch {
		case embSet[i] && len(f.Name) > 0 && !unicode.IsUpper(rune(f.Name[0])):
			if rf[i].PkgPath == "" {
				rf[i].PkgPath = pkgPath
			}
		case embSet[i] && len(rf) > 1 && rf[i].Type.NumMethod() > 0:
			// Cannot set Anonymous: reflect.StructOf would panic.
		default:
			rf[i].Anonymous = embSet[i]
		}
	}
	t := &Type{Rtype: reflect.StructOf(rf), kind: reflect.Struct, Embedded: embedded, Fields: fields, Tags: tags}
	structTypes[key] = t
	return t
}

func structTypeKey(fields []*Type, embedded []EmbeddedField, tags []string) string {
	var b strings.Builder
	writeUint32(&b, uint32(len(fields)))
	for _, f := range fields {
		writeString(&b, f.Name)
		writeString(&b, f.PkgPath)
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

// symVisibleFields walks t's symbolic Fields/Embedded graph (no Rtype) and
// returns visible fields with promotion: shallowest depth wins, names that are
// ambiguous at their shallowest depth are excluded (matching reflect.VisibleFields).
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

// symFieldLookup is the Rtype-free counterpart of FieldLookup.
func (t *Type) symFieldLookup(name string) ([]int, *Type) {
	// A defined type (type T1 T) clones its source's Fields, which may have been
	// empty when the source was still a forward-declared placeholder; delegate to
	// the now-finalized underlying via Base.
	if len(t.Fields) == 0 && t.Base != nil && t.Base != t {
		return t.Base.FieldLookup(name)
	}
	for _, sf := range t.symVisibleFields() {
		if sf.name != name {
			continue
		}
		if ft := t.resolveFieldByPath(sf.index); ft != nil {
			if ft.Base != nil && ft.Base.Name != "" {
				ft.Name = ft.Base.Name
			} else {
				ft.Name = ""
			}
			ft.PkgPath = sf.typ.PkgPath
			return sf.index, ft
		}
		return sf.index, &Type{Name: sf.typ.Name, PkgPath: sf.typ.PkgPath, Rtype: sf.typ.Rtype, kind: sf.typ.Kind()}
	}
	return nil, nil
}

// FieldTypeAtPath returns the type of the field reached by the reflect-style
// index path within struct t, computed from the symbolic Fields graph with a
// reflect fallback per segment. Pointers are dereferenced between segments.
// Returns nil if the path cannot be resolved.
func (t *Type) FieldTypeAtPath(path []int) *Type {
	cur := t
	for _, idx := range path {
		if cur == nil {
			return nil
		}
		if cur.Kind() == reflect.Pointer {
			cur = cur.Elem()
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
	if t.Rtype == nil {
		return t.symFieldLookup(name)
	}
	for _, f := range reflect.VisibleFields(t.Rtype) {
		if f.Name != name {
			continue
		}
		// Walk t.Fields/Embedded by f.Index (multi-segment for promoted fields) to
		// recover mvm-level info at the deepest field.
		if ft := t.resolveFieldByPath(f.Index); ft != nil {
			// Use the type name, not the StructOf field name, so method lookup
			// works. Prefer Base's name: for a defined basic type (type Frame
			// uintptr) reflect reports the underlying name and loses "Frame".
			if ft.Base != nil && ft.Base.Name != "" {
				ft.Name = ft.Base.Name
			} else {
				ft.Name = f.Type.Name()
			}
			ft.PkgPath = f.PkgPath
			return f.Index, ft
		}
		return f.Index, &Type{Name: f.Type.Name(), PkgPath: f.PkgPath, Rtype: f.Type, kind: f.Type.Kind()}
	}
	return t.embeddedFieldLookup(name)
}

// resolveFieldByPath walks t.Fields/Embedded recursively along the reflect-side
// field index path, returning a clone of the deepest mvm-level Type so that
// ElemType / Embedded / Fields propagate to the caller.
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
		return &clone
	}
	next := sub
	if next.IsPtr() && next.ElemType != nil {
		next = next.ElemType
	}
	return next.resolveFieldByPath(rest)
}

func (t *Type) embeddedFieldLookup(name string) ([]int, *Type) {
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
				if _, ft := emb.Type.FieldLookup(name); ft != nil {
					return idx, ft
				}
			}
			return idx, &Type{Name: sf.Type.Name(), PkgPath: sf.PkgPath, Rtype: sf.Type, kind: sf.Type.Kind()}
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

// Len returns an array type's length, from the symbolic graph when no rtype is
// materialized yet.
func (t *Type) Len() int {
	if t.Rtype != nil {
		return t.Rtype.Len()
	}
	return t.ArrayLen
}

// IsVariadic reports whether a func type's final parameter is variadic, from the
// symbolic graph when no rtype is materialized yet.
func (t *Type) IsVariadic() bool {
	if t.Rtype != nil {
		return t.Rtype.IsVariadic()
	}
	return t.Variadic
}

// IsComparable reports whether values of t may be compared with == / !=,
// computed from the symbolic graph (matching reflect.Type.Comparable). Slices,
// maps and funcs are not comparable; a struct is comparable iff every field is;
// an array iff its element is; interfaces are comparable (may panic at runtime).
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

// Identical reports whether t and u denote the same Go type. Materialized types
// compare by rtype identity; symbolic ones compare structurally (named types by
// name+package, composites recursively).
func (t *Type) Identical(u *Type) bool {
	if t == u {
		return true
	}
	if t == nil || u == nil {
		return false
	}
	if t.Rtype != nil && u.Rtype != nil {
		return t.Rtype == u.Rtype
	}
	if t.Kind() != u.Kind() {
		return false
	}
	if t.Name != "" || u.Name != "" {
		return t.Name == u.Name && t.PkgPath == u.PkgPath
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

// ptrSize is the machine word: the size and alignment of a pointer, int, and
// the header words of strings/slices/interfaces.
var ptrSize = unsafe.Sizeof(uintptr(0))

type layout struct{ size, align uintptr }

// Size returns the number of bytes a value of t occupies, computed from the
// symbolic graph so it is available before Rtype materializes. It matches
// reflect.Type.Size for every kind mvm constructs.
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

// structLayout lays fields out with Go's padding rules: each field aligns to
// its own alignment, the struct aligns to the widest field, and a struct that
// ends in a zero-sized field gets one byte of trailing padding so the address
// of that field cannot point past the allocation.
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

// FieldOffset returns the byte offset of the field reached by the reflect-style
// index path within struct t, computed symbolically (value-embed promotion;
// Go's unsafe.Offsetof does not cross pointer embeds).
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

// fieldOffsetAt returns the offset of field index i within struct t's own layout.
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
