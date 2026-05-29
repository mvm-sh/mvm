// Package mtype holds mvm's symbolic type representation: the compile-time
// Type graph and the constructors/derivations over it.
package mtype

import (
	"encoding/binary"
	"reflect"
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
			if !sigCompatible(im.Rtype, mt.Methods[im.ID].Rtype) {
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
	if t.Name != "" {
		if t.PkgPath != "" {
			return t.PkgPath + "." + t.Name
		}
		// For native types without an explicit PkgPath, use the reflect
		// representation which includes the package qualifier (e.g. "http.Pusher").
		if t.Rtype.PkgPath() != "" {
			return t.Rtype.String()
		}
		return t.Name
	}
	return t.Rtype.String()
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

// TypeOf returns the mvm type of v.
func TypeOf(v any) *Type {
	t := reflect.TypeOf(v)
	return &Type{Name: t.Name(), Rtype: t, kind: t.Kind()}
}

// SymPtr builds a symbolic *elem with Rtype unset for comp to materialize (see
// vm.MaterializeRtype); SymSlice/SymMap/SymArray/SymChan are the parse-time
// counterparts to vm's rtype-building PointerTo/SliceOf/... .
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

// WithStructTypesLock runs fn holding the StructOf memoization lock, so a
// caller patching a cached struct rtype's fields in place is serialized against
// reflect.StructOf reads on the shared rtype.
func WithStructTypesLock(fn func()) {
	structTypesMu.Lock()
	defer structTypesMu.Unlock()
	fn()
}

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
		// Interface fields use interface{} so vm.Iface values can be stored via reflect.Set.
		if f.Kind() == reflect.Interface {
			rf[i].Type = AnyRtype
		} else {
			rf[i].Type = f.Rtype
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
	for _, f := range reflect.VisibleFields(t.Rtype) {
		if f.Name == name {
			return f.Index
		}
	}
	idx, _ := t.embeddedFieldLookup(name)
	return idx
}

// FieldType returns the type of struct field name, using mvm-level info when available.
func (t *Type) FieldType(name string) *Type {
	_, ft := t.FieldLookup(name)
	return ft
}

// FieldLookup returns the index path and type of struct field name in a single pass.
func (t *Type) FieldLookup(name string) ([]int, *Type) {
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
