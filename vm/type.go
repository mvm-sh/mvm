package vm

import (
	"iter"
	"math"
	"reflect"
	"unicode"
)

// Runtime type and value representations (based on reflect).

// Method records a method's code location and receiver path for interface dispatch.
type Method struct {
	Index      int   // data index of code address (-1 if unset or EmbedIface)
	Path       []int // field index path to embedded receiver (nil = direct, []int{} = deref only)
	EmbedIface bool  // Path leads to an embedded interface field; dispatch through it
	PtrRecv    bool  // true if the method has a pointer receiver (e.g. *T)
}

// IsResolved reports whether this method slot has been populated with
// either a compiled code address or an embedded-interface dispatch entry.
func (m Method) IsResolved() bool { return m.Index >= 0 || m.EmbedIface }

// EmbeddedField records a mvm embedded field within a struct type.
type EmbeddedField struct {
	FieldIdx int   // index of this field in the parent struct
	Type     *Type // mvm type of the embedded field (shares identity with symbol table)
}

// Type is the representation of a runtime type.
type Type struct {
	PkgPath      string
	Name         string
	Rtype        reflect.Type
	Placeholder  bool            // true for forward-declared struct placeholders until SetFields is called
	IfaceMethods []IfaceMethod   // non-nil for interface types: required method signatures
	TypeElems    []TypeElem      // non-nil for constraint interfaces: union members (e.g. cmp.Ordered)
	Methods      []Method        // concrete types: methods[methodID] = code location + receiver path
	Embedded     []EmbeddedField // mvm types of anonymous (embedded) fields, for promoted method lookup
	Params       []*Type         // mvm-level parameter types for func types (nil for non-func or if unknown)
	Returns      []*Type         // mvm-level return types for func types (nil for non-func or if unknown)
	Fields       []*Type         // mvm-level field types for struct types, parallel to reflect visible fields
	ElemType     *Type           // mvm-level element type for map/slice/array/pointer/chan types
	KeyType      *Type           // mvm-level key type for map types; nil for non-maps or native-built maps
	// Base points at the source *Type that struct-field shallow copies
	// were derived from. Methods registered on the source after the copy
	// was taken (normal for mvm, which parses struct types before
	// method declarations) remain reachable through Base.
	Base *Type
}

// IfaceMethod describes a method required by an interface type.
type IfaceMethod struct {
	Name  string
	ID    int          // global method ID; -1 = not yet assigned
	Rtype reflect.Type // method signature (with receiver as 1st param); nil if unknown
}

// TypeElem describes one member of a constraint interface's type-element union,
// e.g. for "type Ordered interface { ~int | ~string }" the type elements are
// TypeElem{Approx: true, Type: intType}, TypeElem{Approx: true, Type: stringType}.
// Approx encodes the "~" prefix (any type whose underlying type is Type).
type TypeElem struct {
	Approx bool
	Type   *Type
}

// Iface represents a boxed interface value at runtime.
// It preserves the concrete mvm type identity for dynamic method dispatch.
type Iface struct {
	Typ *Type // concrete mvm type (carries Name for method lookup)
	Val Value // the concrete value
}

// AnyRtype is the reflect.Type for the empty interface (any).
var AnyRtype = reflect.TypeOf((*any)(nil)).Elem()

// Opaque stands in for an external type which could not be resolved at parse time.
type Opaque struct{}

// OpaqueRtype is the reflect.Type for Opaque.
var OpaqueRtype = reflect.TypeFor[Opaque]()

var ifaceRtype = reflect.TypeOf(Iface{})

// MvmFunc bundles a mvm func value with its native Go reflect.MakeFunc wrapper.
// Stored when a mvm func is assigned to a struct field of func type:
// GF is callable from native Go (HTTP handlers, callbacks, etc.);
// Val is the original mvm func dispatched directly by the VM.
type MvmFunc struct {
	Val Value         // mvm func (int code addr or Closure)
	GF  reflect.Value // reflect.MakeFunc wrapper for native Go callbacks
}

// boundProxyCall is the sentinel Value that IfaceCall places on the
// stack when a native method has registered arg proxies. The Call
// opcode detects this struct, uses Fn as the bound method to invoke,
// and threads RecvType+Method to bridgeArgs for per-arg proxy lookup.
type boundProxyCall struct {
	Fn       reflect.Value
	RecvType reflect.Type
	Method   string
}

var boundProxyCallRtype = reflect.TypeOf(boundProxyCall{})

// IsInterface reports whether t represents an interface type.
func (t *Type) IsInterface() bool {
	return t != nil && t.Rtype.Kind() == reflect.Interface
}

// EnsureIfaceMethods populates IfaceMethods from the reflect method set
// if not already set. This covers native interface types (e.g. io.Reader)
// whose method sets were not explicitly enumerated at parse time.
func (t *Type) EnsureIfaceMethods() {
	if len(t.IfaceMethods) > 0 || t.Rtype.Kind() != reflect.Interface {
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
	if t.Rtype.Kind() == reflect.Pointer {
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
	for _, im := range iface.IfaceMethods {
		if im.ID < 0 || im.ID >= len(t.Methods) || !t.Methods[im.ID].IsResolved() {
			if nativeIface {
				return t.Rtype.Implements(iface.Rtype)
			}
			// Native concrete type with no mvm Methods: check reflect method set.
			return iface.NativeImplements(t.Rtype)
		}
	}
	return true
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
	for _, im := range t.IfaceMethods {
		if _, ok := rt.MethodByName(im.Name); !ok {
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
	return &Type{Name: e.Name(), Rtype: e}
}

// Key returns a map type's key type.
func (t *Type) Key() *Type {
	if t.KeyType != nil {
		return t.KeyType
	}
	k := t.Rtype.Key()
	return &Type{Name: k.Name(), Rtype: k}
}

// Out returns the type's i'th output parameter.
func (t *Type) Out(i int) *Type {
	o := t.Rtype.Out(i)
	return &Type{Name: o.Name(), Rtype: o}
}

// ReturnType returns the mvm-level i'th return type if known, else falls back to Out(i).
func (t *Type) ReturnType(i int) *Type {
	if i < len(t.Returns) {
		return t.Returns[i]
	}
	return t.Out(i)
}

// Value is the VM runtime value.
// Numeric types (bool, int*, uint*, float*) store their value inline in num.
// ref carries reflect.Zero(t) for type metadata on numeric types.
// Composite types (string, slice, map, struct, ptr, func, interface) use ref.
type Value struct {
	num uint64        // inline storage for numeric types (bool, int*, uint*, float*)
	ref reflect.Value // composite data OR reflect.Zero(t) for numeric type metadata
}

// NumKindOffset maps a reflect.Kind to a 0-based offset into per-type opcode blocks.
// Returns -1 for non-numeric kinds.
var NumKindOffset [reflect.Float64 + 1]int

func init() {
	for i := range NumKindOffset {
		NumKindOffset[i] = -1
	}
	NumKindOffset[reflect.Int] = 0
	NumKindOffset[reflect.Int8] = 1
	NumKindOffset[reflect.Int16] = 2
	NumKindOffset[reflect.Int32] = 3
	NumKindOffset[reflect.Int64] = 4
	NumKindOffset[reflect.Uint] = 5
	NumKindOffset[reflect.Uint8] = 6
	NumKindOffset[reflect.Uint16] = 7
	NumKindOffset[reflect.Uint32] = 8
	NumKindOffset[reflect.Uint64] = 9
	NumKindOffset[reflect.Uintptr] = 5 // same opcode block as Uint
	NumKindOffset[reflect.Float32] = 10
	NumKindOffset[reflect.Float64] = 11
}

// Pre-computed zero reflect.Values for all numeric types (zero allocation).
var (
	zbool    = reflect.Zero(reflect.TypeOf(false))
	zint     = reflect.Zero(reflect.TypeOf(int(0)))
	zint8    = reflect.Zero(reflect.TypeOf(int8(0)))
	zint16   = reflect.Zero(reflect.TypeOf(int16(0)))
	zint32   = reflect.Zero(reflect.TypeOf(int32(0)))
	zint64   = reflect.Zero(reflect.TypeOf(int64(0)))
	zuint    = reflect.Zero(reflect.TypeOf(uint(0)))
	zuint8   = reflect.Zero(reflect.TypeOf(uint8(0)))
	zuint16  = reflect.Zero(reflect.TypeOf(uint16(0)))
	zuint32  = reflect.Zero(reflect.TypeOf(uint32(0)))
	zuint64  = reflect.Zero(reflect.TypeOf(uint64(0)))
	zuintptr = reflect.Zero(reflect.TypeOf(uintptr(0)))
	zfloat32 = reflect.Zero(reflect.TypeOf(float32(0)))
	zfloat64 = reflect.Zero(reflect.TypeOf(float64(0)))
)

func isNum(k reflect.Kind) bool { return k >= reflect.Bool && k <= reflect.Float64 }

func uintOrUintptr(ref reflect.Value) reflect.Value {
	if ref.Kind() == reflect.Uintptr {
		return zuintptr
	}
	return zuint
}

func isFloat(k reflect.Kind) bool { return k == reflect.Float32 || k == reflect.Float64 }

func numBits(rv reflect.Value) uint64 {
	switch rv.Kind() {
	case reflect.Bool:
		if rv.Bool() {
			return 1
		}
		return 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(rv.Int()) //nolint:gosec
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return math.Float64bits(rv.Float())
	}
	return 0
}

// TypeValue returns a zero value for use as a type descriptor in the data table.
// Preserves the exact reflect.Type for all kinds so opcodes like MkChan can
// recover it via ref.Type().
func TypeValue(typ reflect.Type) Value {
	return Value{ref: reflect.New(typ).Elem()}
}

// NewValue returns a zero value for the specified reflect.Type.
func NewValue(typ reflect.Type, arg ...int) Value {
	if isNum(typ.Kind()) {
		return Value{ref: reflect.New(typ).Elem()}
	}
	switch typ.Kind() {
	case reflect.Slice:
		if len(arg) == 1 {
			v := reflect.New(typ).Elem()
			v.Set(reflect.MakeSlice(typ, arg[0], arg[0]))
			return Value{ref: v}
		}
	case reflect.Map:
		if len(arg) == 1 {
			v := reflect.New(typ).Elem()
			v.Set(reflect.MakeMapWithSize(typ, arg[0]))
			return Value{ref: v}
		}
	case reflect.Func, reflect.Interface:
		// Func/interface variables hold heterogeneous values (int, Closure, Iface).
		// Use interface{} so reflect.Set can accept any of them.
		return Value{ref: reflect.New(AnyRtype).Elem()}
	}
	return Value{ref: reflect.New(typ).Elem()}
}

// TypeOf returns the runtime type of v.
func TypeOf(v any) *Type {
	t := reflect.TypeOf(v)
	return &Type{Name: t.Name(), Rtype: t}
}

// ValueOf returns the runtime value of v.
func ValueOf(v any) Value {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return Value{}
	}
	if isNum(rv.Kind()) {
		return Value{num: numBits(rv), ref: reflect.Zero(rv.Type())}
	}
	return Value{ref: rv}
}

func boolVal(b bool) Value {
	v := Value{ref: zbool}
	if b {
		v.num = 1
	}
	return v
}

// Kind returns the reflect.Kind of the value.
func (v Value) Kind() reflect.Kind { return v.ref.Kind() }

// Type returns the reflect.Type of the value.
func (v Value) Type() reflect.Type { return v.ref.Type() }

// UnwrapType checks if v encodes a stdlib type as (*T)(nil).
// If so, it returns the underlying reflect.Type and true.
func (v Value) UnwrapType() (reflect.Type, bool) {
	if v.Kind() == reflect.Pointer && v.Reflect().IsNil() {
		return v.Type().Elem(), true
	}
	return nil, false
}

// IsValid reports whether v represents a value (ref is set).
func (v Value) IsValid() bool { return v.ref.IsValid() }

// Int returns v's value as int64.
func (v Value) Int() int64 { return int64(v.num) } //nolint:gosec

// Uint returns v's value as uint64.
func (v Value) Uint() uint64 { return v.num }

// Float returns v's value as float64.
func (v Value) Float() float64 { return math.Float64frombits(v.num) }

// Bool returns v's value as bool.
func (v Value) Bool() bool { return v.num != 0 }

// Interface returns v's value as interface{}.
func (v Value) Interface() any {
	if v.IsIface() {
		return v.IfaceVal().Val.Interface()
	}
	return v.Reflect().Interface()
}

// CanInt reports whether Int can be called without panicking.
func (v Value) CanInt() bool {
	k := v.ref.Kind()
	return k >= reflect.Int && k <= reflect.Int64
}

// CanAddr reports whether the value is addressable.
func (v Value) CanAddr() bool { return v.ref.CanAddr() }

// Reflect reconstructs a reflect.Value from an inline numeric Value.
// For composite types, returns ref directly.
// This may allocate for numeric types; use only at reflect boundaries.
func (v Value) Reflect() reflect.Value {
	if !v.ref.IsValid() || !isNum(v.ref.Kind()) || v.ref.CanAddr() {
		return v.ref
	}
	// Non addressable numeric value: allocate, set and return a new reflect value.
	r := reflect.New(v.ref.Type()).Elem()
	switch v.ref.Kind() {
	case reflect.Bool:
		r.SetBool(v.num != 0)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		r.SetInt(int64(v.num)) //nolint:gosec
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		r.SetUint(v.num)
	case reflect.Float32, reflect.Float64:
		r.SetFloat(math.Float64frombits(v.num))
	}
	return r
}

// Addr returns a pointer value representing the address of v.
func (v Value) Addr() reflect.Value { return v.ref.Addr() }

// Elem returns the value that the interface v contains or the pointer v points to.
func (v Value) Elem() reflect.Value { return v.ref.Elem() }

// Len returns v's length.
func (v Value) Len() int { return v.ref.Len() }

// Index returns v's i'th element.
func (v Value) Index(i int) reflect.Value { return v.ref.Index(i) }

// Field returns v's i'th field.
func (v Value) Field(i int) reflect.Value { return v.ref.Field(i) }

// FieldByIndex returns the nested field corresponding to index.
func (v Value) FieldByIndex(index []int) reflect.Value { return v.ref.FieldByIndex(index) }

// MapIndex returns the value associated with key in the map v.
func (v Value) MapIndex(key reflect.Value) reflect.Value { return v.ref.MapIndex(key) }

// SetMapIndex sets the element associated with key in the map v.
func (v Value) SetMapIndex(key, elem reflect.Value) { v.ref.SetMapIndex(key, elem) }

// Set assigns x to the value v.
func (v Value) Set(x reflect.Value) { v.ref.Set(x) }

// Slice returns v[i:j].
func (v Value) Slice(i, j int) reflect.Value { return v.ref.Slice(i, j) }

// Slice3 returns v[i:j:k].
func (v Value) Slice3(i, j, k int) reflect.Value { return v.ref.Slice3(i, j, k) }

// Seq returns a range-over iterator for the value v.
func (v Value) Seq() iter.Seq[reflect.Value] { return v.Reflect().Seq() }

// Seq2 returns a range-over-2 iterator for the value v.
func (v Value) Seq2() iter.Seq2[reflect.Value, reflect.Value] { return v.ref.Seq2() }

// CopyArray returns a Value holding a copy of the array in v, so that
// range iterates over a snapshot (Go spec: range over array uses a copy).
func (v Value) CopyArray() Value {
	cp := reflect.New(v.ref.Type()).Elem()
	cp.Set(v.ref)
	return Value{ref: cp}
}

// FromReflect wraps a reflect.Value into a Value.
func FromReflect(rv reflect.Value) Value {
	if isNum(rv.Kind()) {
		return Value{num: numBits(rv), ref: reflect.Zero(rv.Type())}
	}
	return Value{ref: rv}
}

// resetNumRef ensures ref is non-addressable after an arithmetic operation.
func resetNumRef(v *Value) {
	if v.ref.CanAddr() {
		v.ref = reflect.Zero(v.ref.Type())
	}
}

// IsIface reports whether v holds a boxed interface value.
func (v Value) IsIface() bool {
	if !v.ref.IsValid() {
		return false
	}
	if v.ref.Type() == ifaceRtype {
		return true
	}
	// Check inside interface{} slots: v.ref is an any holding an Iface.
	if v.ref.Kind() == reflect.Interface && v.ref.Elem().IsValid() && v.ref.Elem().Type() == ifaceRtype {
		return true
	}
	return false
}

// IfaceVal extracts the Iface from a boxed interface value.
func (v Value) IfaceVal() Iface {
	if v.ref.Kind() == reflect.Interface {
		return v.ref.Elem().Interface().(Iface)
	}
	return v.ref.Interface().(Iface)
}

func isNilable(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Func, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Interface, reflect.UnsafePointer:
		return true
	}
	return false
}

func nilEqual(v Value) bool {
	if isNilable(v.ref) {
		return v.ref.IsNil()
	}
	return !v.ref.IsValid()
}

// Equal reports whether v is equal to u.
func (v Value) Equal(u Value) bool {
	if v.IsIface() {
		if !u.IsValid() {
			return false // non-nil interface != nil
		}
		if u.IsIface() {
			return v.IfaceVal().Val.Equal(u.IfaceVal().Val)
		}
		return v.IfaceVal().Val.Equal(u)
	}
	if isNum(v.ref.Kind()) && isNum(u.ref.Kind()) {
		return v.num == u.num
	}
	// Untyped nil is stored as an invalid ref.
	if !u.ref.IsValid() {
		return nilEqual(v)
	}
	if !v.ref.IsValid() {
		return nilEqual(u)
	}
	return v.ref.Equal(u.ref)
}

// PointerTo returns the pointer type with element t.
func PointerTo(t *Type) *Type {
	return &Type{Name: t.Name, Rtype: reflect.PointerTo(t.Rtype), ElemType: t}
}

// ArrayOf returns the array type with the given length and element type.
func ArrayOf(length int, t *Type) *Type {
	return &Type{Rtype: reflect.ArrayOf(length, t.Rtype), ElemType: t}
}

// SliceOf returns the slice type with the given element type.
func SliceOf(t *Type) *Type {
	return &Type{Rtype: reflect.SliceOf(t.Rtype), ElemType: t}
}

// MapOf returns the map type with the given key and element types.
func MapOf(k, e *Type) *Type {
	return &Type{Rtype: reflect.MapOf(k.Rtype, e.Rtype), ElemType: e, KeyType: k}
}

// ChanOf returns the channel type with the given direction and element type.
func ChanOf(dir reflect.ChanDir, elem *Type) *Type {
	return &Type{Rtype: reflect.ChanOf(dir, elem.Rtype), ElemType: elem}
}

// FuncOf returns the function type with the given argument and result types.
func FuncOf(arg, ret []*Type, variadic bool) *Type {
	a := make([]reflect.Type, len(arg))
	for i, e := range arg {
		a[i] = e.Rtype
	}
	r := make([]reflect.Type, len(ret))
	for i, e := range ret {
		r[i] = e.Rtype
	}
	return &Type{Rtype: reflect.FuncOf(a, r, variadic), Params: arg, Returns: ret}
}

// StructOf returns the struct type with the given field types, embedded field info, and tags.
func StructOf(fields []*Type, embedded []EmbeddedField, tags []string) *Type {
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
		if f.Rtype.Kind() == reflect.Interface {
			rf[i].Type = AnyRtype
		} else {
			rf[i].Type = f.Rtype
		}
		// reflect.StructOf panics if Anonymous=true and PkgPath is non-empty, or if
		// Anonymous=false and the name is unexported with empty PkgPath. For embedded
		// built-in types (e.g. bool, int) the name is lowercase with no PkgPath; we
		// must not set Anonymous and must set a non-empty PkgPath so reflect treats
		// the field as unexported. Mvm tracks embedded info via EmbeddedField.
		//
		// reflect.StructOf also panics if an Anonymous field's type has methods and the
		// struct has more than one field. In that case, skip Anonymous and let mvm's
		// Embedded tracking handle promoted field/method lookup.
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
	return &Type{Rtype: reflect.StructOf(rf), Embedded: embedded, Fields: fields}
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
	for i, f := range reflect.VisibleFields(t.Rtype) {
		if f.Name != name {
			continue
		}
		if i < len(t.Fields) {
			// Return a shallow copy with the type name (not the field name that
			// Fields[i].Name holds for StructOf), so that method lookup works.
			ft := *t.Fields[i]
			ft.Name = f.Type.Name()
			ft.PkgPath = f.PkgPath
			return f.Index, &ft
		}
		return f.Index, &Type{Name: f.Type.Name(), PkgPath: f.PkgPath, Rtype: f.Type}
	}
	return t.embeddedFieldLookup(name)
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
			return idx, &Type{Name: sf.Type.Name(), PkgPath: sf.PkgPath, Rtype: sf.Type}
		}
	}
	return nil, nil
}

// IsPtr returns true if type t is of pointer kind.
func (t *Type) IsPtr() bool { return t.Rtype.Kind() == reflect.Pointer }
