package vm

import (
	"encoding/binary"
	"fmt"
	"iter"
	"math"
	"reflect"
	"strings"
	"sync"
	"unicode"
	"unsafe"

	"github.com/mvm-sh/mvm/vm/synth"
)

// Runtime type and value representations (based on reflect).

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

// Type is the representation of a runtime type.
type Type struct {
	PkgPath      string
	Name         string
	Rtype        reflect.Type
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
	// Base points at the source *Type that struct-field shallow copies
	// were derived from. Methods registered on the source after the copy
	// was taken (normal for mvm, which parses struct types before
	// method declarations) remain reachable through Base.
	Base *Type

	// derived caches canonical derived types built from this Type
	// (PointerTo, SliceOf, ArrayOf, ChanOf, MapOf).
	// Memoization keys on *Type pointer identity so the cache survives a
	// later swap of an inner *Type's Rtype (e.g. by vm/synth).
	// Lazily allocated on first derivation; nil for primitives never derived from.
	// Not safe for concurrent derivation of the same base Type -- callers must
	// serialize, which mvm's single-threaded compile pipeline already does.
	derived *derivedTypes

	// synthIface caches a method-bearing synth interface rtype built from
	// IfaceMethods for native-boundary satisfaction; Rtype stays AnyRtype for
	// in-interpreter storage. Guarded by derivedMu.
	synthIface reflect.Type

	// priorRtype is the original Rtype before the first synth swap (see
	// RefreshRtype). Closure func types and other compiler-captured rtypes may
	// still reference it, so typeByRtype indexes it alongside the current Rtype.
	// Guarded by derivedMu.
	priorRtype reflect.Type
}

type derivedTypes struct {
	ptr   *Type
	slice *Type
	array map[int]*Type
	chans map[reflect.ChanDir]*Type
	maps  map[*Type]*Type
}

// derivedMu serializes all reads/writes of any Type.derived field and the
// Rtype field of derived entries during RefreshRtype propagation.
// Single global mutex because contention is rare (only during concurrent
// compilation of tests that share a *Type, e.g. via the std module's
// pre-loaded type symbols).
// Within a single Compiler, derivation is single-threaded and the lock is
// uncontended.
var derivedMu sync.Mutex

func (t *Type) ensureDerivedLocked() *derivedTypes {
	if t.derived == nil {
		t.derived = &derivedTypes{}
	}
	return t.derived
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

// Iface represents a boxed interface value at runtime.
// It preserves the concrete mvm type identity for dynamic method dispatch.
type Iface struct {
	Typ *Type // concrete mvm type (carries Name for method lookup)
	Val Value // the concrete value
}

// Format routes fmt verbs to the concrete value so an Iface boxed in an
// interface{} slot renders like the raw Go value.
func (i Iface) Format(s fmt.State, verb rune) {
	if !i.Val.IsValid() {
		_, _ = fmt.Fprint(s, "<nil>")
		return
	}
	_, _ = fmt.Fprintf(s, fmt.FormatString(s, verb), Exportable(i.Val.ref).Interface())
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

// boundHookCall is the sentinel Value that IfaceCall places on the stack
// when a native method has a registered NativeMethodHook (e.g., the
// testing.T.Log/Error/Fatal intercepts in stdlib/testing_virt.go).
// The Call opcode detects this struct, uses Fn as the bound method, and
// consults RecvType+Method to look up the hook (which receives Recv).
type boundHookCall struct {
	Fn       reflect.Value
	RecvType reflect.Type
	Method   string
	Recv     reflect.Value
}

var boundHookCallRtype = reflect.TypeOf(boundHookCall{})

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
	// Go method-set rule: a value type T does not include *T's pointer-receiver
	// methods. mvm registers pointer-receiver methods on the value type T (so *T's
	// Methods slice may be empty), so a resolved PtrRecv method only counts toward
	// satisfaction when t is itself a pointer type. Mirrors ifaceProvidedMethods.
	isPtr := t.Rtype != nil && t.Rtype.Kind() == reflect.Pointer
	for _, im := range iface.IfaceMethods {
		if mt := t.ResolveMethodType(im.ID); mt != nil && (isPtr || !mt.Methods[im.ID].PtrRecv) {
			// Method IDs are global by name, so a same-named method of a
			// different signature would otherwise satisfy the interface (e.g.
			// Unwrap() []error counting for interface{ Unwrap() error }). When
			// both signatures are known, require them to match.
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

// sigCompatible reports whether a concrete method signature satisfies the
// interface's required one. Both are receiver-free func types. It is lenient:
// a nil (unknown) signature on either side matches, preserving the prior
// name-only behavior wherever signatures were never recorded.
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

// ResolveMethodType returns the Type whose Methods[id] holds the resolved entry.
// It scans typ, its ElemType, and the Base chain (via ifaceMethodTypes) so a
// struct-field shallow copy resolves methods reachable only through Base -- the
// same coverage as MethodByName, keeping interface satisfaction (Implements,
// type assertions) consistent with method dispatch.
func (t *Type) ResolveMethodType(id int) *Type {
	if id < 0 {
		return nil
	}
	types, n := ifaceMethodTypes(t)
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
		return &Type{Name: in.Name(), Rtype: in}
	}
	return nil
}

// Value is the VM runtime value.
// Numeric types (bool, int*, uint*, float*) store their value inline in num.
// ref carries reflect.Zero(t) for type metadata on numeric types.
// Composite types (string, slice, map, struct, ptr, func, interface) use ref.
type Value struct {
	num uint64        // inline storage for numeric types (bool, int*, uint*, float*)
	ref reflect.Value // composite data OR reflect.Zero(t) for numeric type metadata
}

// ValueSize is the in-memory footprint of a Value on the current platform.
// Used by stat reporters to convert slot counts to bytes.
const ValueSize = int(unsafe.Sizeof(Value{}))

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
		return uint64(rv.Int())
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
func (v Value) Int() int64 { return int64(v.num) }

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
		r.SetInt(int64(v.num))
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

// Exportable returns rv with its read-only flag cleared so that .Interface()
// and .Call() do not panic on values obtained from unexported struct fields.
func Exportable(rv reflect.Value) reflect.Value {
	if !rv.IsValid() || rv.CanInterface() {
		return rv
	}
	if rv.CanAddr() {
		return reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem()
	}
	// Non-addressable read-only value (typical: a method value taken off
	// an unexported field, or the result of an unexported func-typed field
	// access). Clear flagStickyRO and flagEmbedRO directly. The reflect.Value
	// layout has been stable across recent Go releases; if the bit positions
	// ever change, this will fail loudly via panic on the next .Interface()
	// rather than silently misbehave.
	// rvHeader mirrors reflect.Value's internal layout (typ, ptr, flag).
	type rvHeader struct {
		typ  unsafe.Pointer
		ptr  unsafe.Pointer
		flag uintptr
	}
	const flagRO = (1 << 5) | (1 << 6) // flagStickyRO | flagEmbedRO
	out := rv
	(*rvHeader)(unsafe.Pointer(&out)).flag &^= flagRO
	return out
}

// IfaceVal extracts the Iface from a boxed interface value.
func (v Value) IfaceVal() Iface {
	rv := Exportable(v.ref)
	if rv.Kind() == reflect.Interface {
		return rv.Elem().Interface().(Iface)
	}
	return rv.Interface().(Iface)
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

// isNativeIface reports whether v is a non-nil native interface{} holding a
// concrete value (i.e. NOT mvm's own Iface wrapper). Used by Equal to unwrap
// such interfaces before comparison.
func isNativeIface(v Value) bool {
	if !v.ref.IsValid() || v.ref.Kind() != reflect.Interface || v.ref.IsNil() {
		return false
	}
	return !v.IsIface()
}

// Equal reports whether v is equal to u.
func (v Value) Equal(u Value) bool {
	// Native interface{} holding a concrete (e.g. flag.Getter.Get() returns
	// any-bool from native code): unwrap to the dynamic value so a
	// `value == literal` test matches Go's interface-to-concrete comparison
	// rather than reflect.Value.Equal's stricter Kind-must-match rule. mvm's
	// own Iface wrapper is handled below; this only unwraps plain native ifaces.
	if isNativeIface(v) {
		v = FromReflect(v.ref.Elem())
	}
	if isNativeIface(u) {
		u = FromReflect(u.ref.Elem())
	}
	if v.IsIface() {
		if !u.IsValid() {
			return false // non-nil interface != nil
		}
		if u.IsIface() {
			return v.IfaceVal().Val.Equal(u.IfaceVal().Val)
		}
		return v.IfaceVal().Val.Equal(u)
	}
	if u.IsIface() {
		// v is a concrete value, u is still boxed as Iface; compare
		// against the boxed value.
		return u.IfaceVal().Val.Equal(v)
	}
	if isNum(v.ref.Kind()) && isNum(u.ref.Kind()) {
		// Floats are stored as Float64 bits; compare them as floats so the IEEE
		// rules hold (NaN != NaN, +0.0 == -0.0) instead of raw bit equality.
		if isFloat(v.ref.Kind()) || isFloat(u.ref.Kind()) {
			return v.Float() == u.Float()
		}
		return v.num == u.num
	}
	// Untyped nil is stored as an invalid ref.
	if !u.ref.IsValid() {
		return nilEqual(v)
	}
	if !v.ref.IsValid() {
		return nilEqual(u)
	}
	// For structs, walk fields and recurse so that interface-typed fields
	// (stored as mvm Iface inside an `any` slot) get mvm's iface-aware
	// comparison instead of reflect.Value.Equal's structural compare. The
	// latter would recurse into the embedded reflect.Value of Iface.Val and
	// compare ptr fields, which differ across instances of the same logical
	// value.
	if v.ref.Kind() == reflect.Struct && u.ref.Kind() == reflect.Struct && v.ref.Type() == u.ref.Type() {
		nf := v.ref.NumField()
		for i := 0; i < nf; i++ {
			fv := FromReflect(Exportable(v.ref.Field(i)))
			fu := FromReflect(Exportable(u.ref.Field(i)))
			if !fv.Equal(fu) {
				return false
			}
		}
		return true
	}
	return v.ref.Equal(u.ref)
}

// PointerTo returns the canonical pointer type with element t.
// Repeated calls with the same t return the same *Type.
func PointerTo(t *Type) *Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := t.ensureDerivedLocked()
	if d.ptr != nil {
		return d.ptr
	}
	d.ptr = &Type{Name: t.Name, Rtype: derivePointerTo(t.Rtype), ElemType: t}
	return d.ptr
}

// ArrayOf returns the canonical array type with the given length and element type.
func ArrayOf(length int, t *Type) *Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := t.ensureDerivedLocked()
	if d.array == nil {
		d.array = map[int]*Type{}
	} else if a := d.array[length]; a != nil {
		return a
	}
	a := &Type{Rtype: deriveArrayOf(length, t.Rtype), ElemType: t}
	d.array[length] = a
	return a
}

// SliceOf returns the canonical slice type with the given element type.
// Repeated calls with the same t return the same *Type.
func SliceOf(t *Type) *Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := t.ensureDerivedLocked()
	if d.slice != nil {
		return d.slice
	}
	d.slice = &Type{Rtype: deriveSliceOf(t.Rtype), ElemType: t}
	return d.slice
}

// MapOf returns the canonical map type with the given key and element types.
// Memoized on the key type, indexed by element type.
func MapOf(k, e *Type) *Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := k.ensureDerivedLocked()
	if d.maps == nil {
		d.maps = map[*Type]*Type{}
	} else if m := d.maps[e]; m != nil {
		return m
	}
	m := &Type{Rtype: deriveMapOf(k.Rtype, e.Rtype), ElemType: e, KeyType: k}
	d.maps[e] = m
	return m
}

// ChanOf returns the canonical channel type with the given direction and element type.
func ChanOf(dir reflect.ChanDir, elem *Type) *Type {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	d := elem.ensureDerivedLocked()
	if d.chans == nil {
		d.chans = map[reflect.ChanDir]*Type{}
	} else if c := d.chans[dir]; c != nil {
		return c
	}
	c := &Type{Rtype: deriveChanOf(dir, elem.Rtype), ElemType: elem}
	d.chans[dir] = c
	return c
}

// derivePointerTo / SliceOf / ArrayOf / ChanOf / MapOf route between
// reflect.* and synth.* per elem (and key) origin.
// reflect.*Of preserves native rtype identity (so reflect.PointerTo(int) and
// vm.PointerTo's *int Rtype stay the canonical *int); on a synth elem
// reflect.*Of crashes via resolveNameOff, so synth.* takes over.
// Exception for PointerTo: if a synth elem has its PtrToThis wired by
// AttachPtrMethods, reflect.PointerTo follows that wiring to the canonical
// *T-with-methods WITHOUT entering the resolveNameOff path -- using
// reflect.PointerTo there unifies vm-side and reflect-side *T identity.
func derivePointerTo(elem reflect.Type) reflect.Type {
	if synth.IsSynth(elem) && !synth.HasPtrToThis(elem) {
		return synth.PointerTo(elem)
	}
	return reflect.PointerTo(elem)
}

func deriveSliceOf(elem reflect.Type) reflect.Type {
	if synth.IsSynth(elem) {
		return synth.SliceOf(elem)
	}
	return reflect.SliceOf(elem)
}

func deriveArrayOf(n int, elem reflect.Type) reflect.Type {
	if synth.IsSynth(elem) {
		return synth.ArrayOf(n, elem)
	}
	return reflect.ArrayOf(n, elem)
}

func deriveChanOf(dir reflect.ChanDir, elem reflect.Type) reflect.Type {
	if synth.IsSynth(elem) {
		return synth.ChanOf(dir, elem)
	}
	return reflect.ChanOf(dir, elem)
}

func deriveMapOf(key, elem reflect.Type) reflect.Type {
	if synth.IsSynth(key) || synth.IsSynth(elem) {
		return synth.MapOf(key, elem)
	}
	return reflect.MapOf(key, elem)
}

// RefreshRtype updates t.Rtype to newRT and cascades the change through every
// derived *Type cached on t (and recursively on those derivatives).
// Called by AttachSynthMethods after vm/synth swaps the layout-identity of
// a user type to a synth-built rtype carrying interpreted methods; derived
// rtypes (*T, []T, [N]T, chan T, map[T]E) captured by the compiler before
// the swap would otherwise reference the pre-synth layout.
// Uses synth.* (not reflect.*Of) for the rebuild because reflect.*Of crashes
// via resolveNameOff on rtypes that live outside any registered moduledata.
// Maps cascade only the t-as-key direction (t.derived.maps): maps with t as
// element live under the key's derived cache and are not reachable from t.
func (t *Type) RefreshRtype(newRT reflect.Type) {
	derivedMu.Lock()
	defer derivedMu.Unlock()
	t.refreshLocked(newRT)
}

func (t *Type) refreshLocked(newRT reflect.Type) {
	if newRT == nil || newRT == t.Rtype {
		return
	}
	if t.priorRtype == nil {
		t.priorRtype = t.Rtype
	}
	t.Rtype = newRT
	if t.derived == nil {
		return
	}
	d := t.derived
	if d.ptr != nil {
		d.ptr.refreshLocked(synth.PointerTo(newRT))
	}
	if d.slice != nil {
		d.slice.refreshLocked(synth.SliceOf(newRT))
	}
	for length, a := range d.array {
		a.refreshLocked(synth.ArrayOf(length, newRT))
	}
	for dir, c := range d.chans {
		c.refreshLocked(synth.ChanOf(dir, newRT))
	}
	for e, mt := range d.maps {
		mt.refreshLocked(synth.MapOf(newRT, e.Rtype))
	}
}

const canonicalTypeMaxDepth = 1024

// CanonicalType walks the Base chain to recover the source *Type that a
// struct-field shallow copy was derived from.
// Returns t itself if Base is nil.
// Depth-capped at canonicalTypeMaxDepth so a malformed Type graph with
// cyclic Base pointers can't hang the compiler; returns t unchanged when
// the cap is hit.
// Callers that hold a clone (Base != nil) but want to observe live state
// updated by the synth cascade (which only touches canonical *Types) must
// route through this helper rather than reading clone fields directly.
func CanonicalType(t *Type) *Type {
	start := t
	for i := 0; i < canonicalTypeMaxDepth && t != nil && t.Base != nil; i++ {
		t = t.Base
	}
	if t != nil && t.Base != nil {
		return start
	}
	return t
}

// LiveFieldRtype returns the current rtype to use when rebuilding a struct
// containing f as a field.
// Struct-field clones (Base != nil) and pointer/slice/array/chan/map *Types
// whose ElemType is a clone don't get refreshed by the in-Type cascade
// (which walks t.derived from canonical roots only).  This helper follows
// Base chains and re-derives via derive* so the returned rtype reflects the
// post-cascade state of every referenced canonical type.
func LiveFieldRtype(f *Type) reflect.Type {
	if f == nil {
		return nil
	}
	canonical := CanonicalType(f)
	if canonical == nil || canonical.Rtype == nil {
		return nil
	}
	// Derived-type field whose ElemType (and/or KeyType for map) is itself
	// a clone: re-derive from the canonical inner so we observe cascade
	// updates that only landed on the canonical's derived chain.
	if canonical.ElemType != nil && canonical.ElemType.Base != nil {
		elemC := CanonicalType(canonical.ElemType)
		switch canonical.Rtype.Kind() {
		case reflect.Pointer:
			return derivePointerTo(elemC.Rtype)
		case reflect.Slice:
			return deriveSliceOf(elemC.Rtype)
		case reflect.Array:
			return deriveArrayOf(canonical.Rtype.Len(), elemC.Rtype)
		case reflect.Chan:
			return deriveChanOf(canonical.Rtype.ChanDir(), elemC.Rtype)
		case reflect.Map:
			keyC := canonical.KeyType
			if keyC != nil {
				keyC = CanonicalType(keyC)
			}
			keyRT := canonical.Rtype.Key()
			if keyC != nil && keyC.Rtype != nil {
				keyRT = keyC.Rtype
			}
			return deriveMapOf(keyRT, elemC.Rtype)
		}
	}
	return canonical.Rtype
}

// funcTypes is the global registry for FuncOf memoization.
// Keys are signature fingerprints (packed pointer bytes of params/returns +
// variadic byte); values are the canonical func *Type per signature.
// Persistence is process-lifetime: cached entries hold the input *Type values
// (via Params/Returns), so the inputs stay reachable and the key stays valid.
// Guarded by funcTypesMu; contention is effectively zero because mvm's compile
// pipeline is single-threaded per Compiler.
var (
	funcTypesMu sync.Mutex
	funcTypes   = map[string]*Type{}
)

// FuncOf returns the canonical function type with the given argument and
// result types and variadic flag.
// Repeated calls with the same (arg pointers, ret pointers, variadic) return
// the same *Type.
// Callers must NOT mutate the returned *Type's Params/Returns slices; they
// alias the first caller's slices held in the cache.
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
	t := &Type{Rtype: reflect.FuncOf(a, r, variadic), Params: arg, Returns: ret}
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

// StructOf returns the canonical struct type with the given field types,
// embedded field info, and tags.
// Memoized on a structural key: callers that parse the same source shape
// (e.g. two `struct{X int}` literals) receive the same *Type even when
// caller-side parseStructType clones field *Type instances per call.
// The key uses each field's Name + PkgPath + Base pointer (or self pointer
// if Base is nil) so cloned-but-equivalent fields converge.
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
	t := &Type{Rtype: reflect.StructOf(rf), Embedded: embedded, Fields: fields}
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
		// Walk t.Fields/Embedded along f.Index to recover mvm-level info (ElemType,
		// Fields, Embedded) at the deepest field. VisibleFields' iteration index
		// does not align with t.Fields when promoted fields are present, so we
		// index strictly by f.Index -- single-segment for top-level fields,
		// multi-segment for fields promoted through embedded structs.
		if ft := t.resolveFieldByPath(f.Index); ft != nil {
			// Return a shallow copy with the type name (not the field name that
			// Fields[i].Name holds for StructOf), so that method lookup works.
			// Prefer the back-linked Base type's name: for defined types whose
			// underlying is a basic kind (e.g. `type Frame uintptr`), reflect's
			// f.Type.Name() returns the underlying name ("uintptr") and loses
			// the user-level name needed to find methods on Frame.
			if ft.Base != nil && ft.Base.Name != "" {
				ft.Name = ft.Base.Name
			} else {
				ft.Name = f.Type.Name()
			}
			ft.PkgPath = f.PkgPath
			return f.Index, ft
		}
		return f.Index, &Type{Name: f.Type.Name(), PkgPath: f.PkgPath, Rtype: f.Type}
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
			return idx, &Type{Name: sf.Type.Name(), PkgPath: sf.PkgPath, Rtype: sf.Type}
		}
	}
	return nil, nil
}

// IsPtr returns true if type t is of pointer kind.
func (t *Type) IsPtr() bool { return t.Rtype.Kind() == reflect.Pointer }

// IsStruct returns true if type t is of struct kind.
func (t *Type) IsStruct() bool { return t != nil && t.Rtype != nil && t.Rtype.Kind() == reflect.Struct }

// IsSlice returns true if type t is of slice kind.
func (t *Type) IsSlice() bool { return t != nil && t.Rtype != nil && t.Rtype.Kind() == reflect.Slice }

// IsFunc returns true if type t is of func kind.
func (t *Type) IsFunc() bool { return t != nil && t.Rtype != nil && t.Rtype.Kind() == reflect.Func }
