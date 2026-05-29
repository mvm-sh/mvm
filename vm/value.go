package vm

import (
	"fmt"
	"iter"
	"math"
	"reflect"
	"unsafe"
)

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
