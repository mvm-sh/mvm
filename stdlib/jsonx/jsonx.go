// Package jsonx is a mvm-aware replacement for the encoding/json
// functions that need to honour mvm-defined methods on struct
// types (MarshalJSON, UnmarshalJSON). It walks mvm *vm.Type
// metadata to traverse struct fields and dispatches mvm methods
// via Machine.CallFunc. Values whose mvm type is unknown are
// forwarded to the native encoding/json implementation.
//
// Dispatch is wired through vm.RegisterArgProxy / RegisterArgProxyMethod:
// mvm Iface arguments to json.Marshal / Unmarshal / MarshalIndent
// and to (*Encoder).Encode / (*Decoder).Decode are wrapped as
// marshalProxy / unmarshalProxy pointers whose MarshalJSON /
// UnmarshalJSON methods re-enter the walker. Native encoding/json
// reflection sees the proxies as ordinary json.Marshaler /
// json.Unmarshaler implementations.
package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/vm"
)

func init() {
	stdlib.RegisterPackagePatcher("encoding/json", patchEncodingJSON)
	vm.RegisterArgProxy(json.Marshal, 0, newMarshalProxy)
	vm.RegisterArgProxy(json.MarshalIndent, 0, newMarshalProxy)
	vm.RegisterArgProxy(json.Unmarshal, 1, newUnmarshalProxy)
	vm.RegisterArgProxyMethod((*Encoder)(nil), "Encode", 0, newMarshalProxy)
	vm.RegisterArgProxyMethod((*Decoder)(nil), "Decode", 0, newUnmarshalProxy)
}

// patchEncodingJSON overlays the jsonx Encoder/Decoder types into the
// encoding/json package's symbol map so interpreted code resolves
// json.Encoder / json.Decoder to the jsonx implementations.
func patchEncodingJSON(_ *vm.Machine, values map[string]vm.Value) {
	values["Encoder"] = encoderTypeV
	values["Decoder"] = decoderTypeV
	values["NewEncoder"] = newEncoderFuncV
	values["NewDecoder"] = newDecoderFuncV
}

// marshalProxy wraps a mvm Iface so native encoding/json reflection
// discovers a json.Marshaler whose MarshalJSON re-enters the jsonx
// walker with full Iface metadata. Installed at native-call boundaries
// by the vm.RegisterArgProxy* registrations above. unmarshalProxy is
// its decode-side counterpart.
type marshalProxy struct {
	m   *vm.Machine
	ifc vm.Iface
}

func (p *marshalProxy) MarshalJSON() ([]byte, error) {
	return marshalValue(p.m, vm.FromReflect(reflect.ValueOf(p.ifc)))
}

type unmarshalProxy struct {
	m   *vm.Machine
	ifc vm.Iface
}

func (p *unmarshalProxy) UnmarshalJSON(data []byte) error {
	return unmarshalValue(p.m, data, vm.FromReflect(reflect.ValueOf(p.ifc)))
}

func newMarshalProxy(m *vm.Machine, ifc vm.Iface) reflect.Value {
	return reflect.ValueOf(&marshalProxy{m: m, ifc: ifc})
}

func newUnmarshalProxy(m *vm.Machine, ifc vm.Iface) reflect.Value {
	return reflect.ValueOf(&unmarshalProxy{m: m, ifc: ifc})
}

// unmarshalValue decodes data into the destination described by dst.
// dst is expected to hold a pointer (boxed as Iface when mvm code
// passed it through `any`).
func unmarshalValue(m *vm.Machine, data []byte, dst vm.Value) error {
	if dst.IsIface() {
		ifc := dst.IfaceVal()
		return unmarshalIface(m, data, ifc)
	}
	rv := dst.Reflect()
	if !rv.IsValid() {
		return errors.New("json.Unmarshal: nil destination")
	}
	// Pure-native value: delegate.
	return json.Unmarshal(data, rv.Interface())
}

// unmarshalIface decodes data into ifc (a pointer boxed as Iface).
func unmarshalIface(m *vm.Machine, data []byte, ifc vm.Iface) error {
	if ifc.Typ == nil {
		rv := ifc.Val.Reflect()
		if !rv.IsValid() {
			return errors.New("json.Unmarshal: invalid destination")
		}
		return json.Unmarshal(data, rv.Interface())
	}
	// mvm UnmarshalJSON on the pointer type (or the pointee).
	if ok, err := callUnmarshalJSON(m, data, ifc); ok {
		return err
	}
	rv := ifc.Val.Reflect()
	if !rv.IsValid() {
		return errors.New("json.Unmarshal: invalid destination")
	}
	if ifc.Typ.Rtype.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return errors.New("json.Unmarshal: nil pointer")
		}
		elem := rv.Elem()
		elemTyp := ifc.Typ.ElemType
		return decodeInto(m, data, elem, elemTyp)
	}
	return json.Unmarshal(data, rv.Interface())
}

// decodeInto decodes data into the addressable reflect.Value dst
// (typed via mvm type typ).
func decodeInto(m *vm.Machine, data []byte, dst reflect.Value, typ *vm.Type) error {
	if typ == nil {
		if !dst.CanAddr() {
			return errors.New("json.Unmarshal: destination is not addressable")
		}
		return json.Unmarshal(data, dst.Addr().Interface())
	}
	// If the pointee type has UnmarshalJSON, build a pointer Iface and dispatch.
	// When typ is already a pointer, dst IS the pointer (taking another Addr
	// would pass **T where the receiver expects *T). Allocate if nil.
	if method, ok := m.MethodByName(typ, "UnmarshalJSON"); ok {
		var recv reflect.Value
		switch {
		case typ.Rtype.Kind() == reflect.Pointer:
			if dst.IsNil() {
				if !dst.CanSet() {
					return errors.New("json.Unmarshal: non-settable pointer destination")
				}
				dst.Set(reflect.New(typ.Rtype.Elem()))
			}
			recv = dst
		case dst.CanAddr():
			recv = dst.Addr()
		}
		if recv.IsValid() {
			ptrIfc := vm.Iface{Typ: typ, Val: vm.FromReflect(recv)}
			return invokeUnmarshalJSON(m, data, ptrIfc, method)
		}
	}
	switch typ.Rtype.Kind() {
	case reflect.Struct:
		return decodeStruct(m, data, dst, typ)
	case reflect.Pointer:
		if dst.IsNil() {
			dst.Set(reflect.New(typ.Rtype.Elem()))
		}
		return decodeInto(m, data, dst.Elem(), typ.ElemType)
	case reflect.Slice:
		return decodeSlice(m, data, dst, typ)
	case reflect.Map:
		return decodeMap(m, data, dst, typ)
	default:
		if !dst.CanAddr() {
			return fmt.Errorf("json.Unmarshal: non-addressable destination for kind %v", typ.Rtype.Kind())
		}
		return json.Unmarshal(data, dst.Addr().Interface())
	}
}

// decodeStruct decodes a JSON object into an addressable struct value.
func decodeStruct(m *vm.Machine, data []byte, dst reflect.Value, typ *vm.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	rtype := typ.Rtype
	for name, piece := range raw {
		idx, fieldTyp := lookupField(rtype, typ, name)
		if idx < 0 {
			continue
		}
		fv := dst.Field(idx)
		if !fv.CanSet() {
			continue
		}
		if err := decodeInto(m, piece, fv, fieldTyp); err != nil {
			return err
		}
	}
	return nil
}

// lookupField finds the struct field matching name (via json tag or
// field name). Returns field index + mvm type (or -1 / nil).
func lookupField(rtype reflect.Type, typ *vm.Type, name string) (int, *vm.Type) {
	// First pass: match explicit json tag names.
	for i := range rtype.NumField() {
		sf := rtype.Field(i)
		if !sf.IsExported() {
			continue
		}
		tagName, _ := parseJSONTag(sf.Tag.Get("json"))
		if tagName == "-" {
			continue
		}
		if tagName != "" && tagName == name {
			return i, fieldTypeAt(typ, i)
		}
	}
	// Second pass: case-exact name, then case-insensitive.
	exact, ci := -1, -1
	for i := range rtype.NumField() {
		sf := rtype.Field(i)
		if !sf.IsExported() {
			continue
		}
		tagName, _ := parseJSONTag(sf.Tag.Get("json"))
		if tagName == "-" || tagName != "" {
			continue
		}
		if sf.Name == name {
			exact = i
			break
		}
		if ci == -1 && strings.EqualFold(sf.Name, name) {
			ci = i
		}
	}
	if exact >= 0 {
		return exact, fieldTypeAt(typ, exact)
	}
	if ci >= 0 {
		return ci, fieldTypeAt(typ, ci)
	}
	return -1, nil
}

func fieldTypeAt(typ *vm.Type, i int) *vm.Type {
	if i < len(typ.Fields) {
		return typ.Fields[i]
	}
	return nil
}

// decodeSlice decodes a JSON array into an addressable slice value.
func decodeSlice(m *vm.Machine, data []byte, dst reflect.Value, typ *vm.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		dst.Set(reflect.Zero(typ.Rtype))
		return nil
	}
	// []byte is encoded as base64 string - delegate.
	if typ.Rtype.Elem().Kind() == reflect.Uint8 {
		var b []byte
		if err := json.Unmarshal(data, &b); err != nil {
			return err
		}
		dst.SetBytes(b)
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	sl := reflect.MakeSlice(typ.Rtype, len(raw), len(raw))
	for i, piece := range raw {
		if err := decodeInto(m, piece, sl.Index(i), typ.ElemType); err != nil {
			return err
		}
	}
	dst.Set(sl)
	return nil
}

// decodeMap decodes a JSON object into an addressable map value.
// Existing entries are preserved (encoding/json semantics): keys
// present in data overwrite, others remain untouched.
func decodeMap(m *vm.Machine, data []byte, dst reflect.Value, typ *vm.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		dst.Set(reflect.Zero(typ.Rtype))
		return nil
	}
	if typ.Rtype.Key().Kind() != reflect.String {
		if !dst.CanAddr() {
			return errors.New("json.Unmarshal: non-addressable map destination")
		}
		return json.Unmarshal(data, dst.Addr().Interface())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if dst.IsNil() {
		dst.Set(reflect.MakeMapWithSize(typ.Rtype, len(raw)))
	}
	for k, piece := range raw {
		elem := reflect.New(typ.Rtype.Elem()).Elem()
		if err := decodeInto(m, piece, elem, typ.ElemType); err != nil {
			return err
		}
		dst.SetMapIndex(reflect.ValueOf(k).Convert(typ.Rtype.Key()), elem)
	}
	return nil
}

// callUnmarshalJSON checks whether ifc (a pointer-boxed mvm value)
// has a registered UnmarshalJSON method and dispatches it. ok=false
// means no such method.
func callUnmarshalJSON(m *vm.Machine, data []byte, ifc vm.Iface) (bool, error) {
	method, found := m.MethodByName(ifc.Typ, "UnmarshalJSON")
	if !found {
		return false, nil
	}
	return true, invokeUnmarshalJSON(m, data, ifc, method)
}

// invokeUnmarshalJSON dispatches the mvm UnmarshalJSON method via
// the VM with the given bytes.
func invokeUnmarshalJSON(m *vm.Machine, data []byte, ifc vm.Iface, method vm.Method) error {
	fval := m.MakeMethodCallable(ifc, method)
	fnType := reflect.TypeOf((func([]byte) error)(nil))
	in := []reflect.Value{reflect.ValueOf(data)}
	out, err := m.CallFunc(fval, fnType, in)
	if err != nil {
		return err
	}
	if len(out) != 1 {
		return fmt.Errorf("UnmarshalJSON: expected 1 return, got %d", len(out))
	}
	if out[0].IsValid() && !out[0].IsNil() {
		if e, ok := out[0].Interface().(error); ok {
			return e
		}
	}
	return nil
}

// marshalValue encodes a single mvm Value as JSON bytes. It is the
// top-level entry point; recursion writes directly into the shared
// buffer via encodeTo to avoid per-level []byte allocations.
func marshalValue(m *vm.Machine, val vm.Value) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeTo(&buf, m, val); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeTo writes the JSON encoding of val into buf.
func encodeTo(buf *bytes.Buffer, m *vm.Machine, val vm.Value) error {
	if !val.IsValid() {
		buf.WriteString("null")
		return nil
	}
	if val.IsIface() {
		return encodeIfaceTo(buf, m, val.IfaceVal())
	}
	return nativeMarshal(buf, val.Reflect().Interface())
}

// encodeIfaceTo writes the JSON encoding of ifc into buf.
func encodeIfaceTo(buf *bytes.Buffer, m *vm.Machine, ifc vm.Iface) error {
	if ifc.Typ == nil {
		rv := ifc.Val.Reflect()
		if !rv.IsValid() {
			buf.WriteString("null")
			return nil
		}
		return nativeMarshal(buf, rv.Interface())
	}
	if data, ok, err := callMarshalJSON(m, ifc); ok {
		if err != nil {
			return err
		}
		buf.Write(data)
		return nil
	}
	rv := ifc.Val.Reflect()
	if !rv.IsValid() {
		buf.WriteString("null")
		return nil
	}
	switch ifc.Typ.Rtype.Kind() {
	case reflect.Struct:
		return encodeStructTo(buf, m, rv, ifc.Typ)
	case reflect.Pointer:
		if rv.IsNil() {
			buf.WriteString("null")
			return nil
		}
		return encodeIfaceTo(buf, m, vm.Iface{Typ: ifc.Typ.ElemType, Val: vm.FromReflect(rv.Elem())})
	case reflect.Slice, reflect.Array:
		return encodeSliceTo(buf, m, rv, ifc.Typ)
	case reflect.Map:
		return encodeMapTo(buf, m, rv, ifc.Typ)
	case reflect.Interface:
		if rv.IsNil() {
			buf.WriteString("null")
			return nil
		}
		inner := rv.Elem()
		if inner.Type() == ifaceRtype {
			return encodeIfaceTo(buf, m, inner.Interface().(vm.Iface))
		}
		return nativeMarshal(buf, inner.Interface())
	default:
		return nativeMarshal(buf, rv.Interface())
	}
}

// nativeMarshal calls stdlib json.Marshal on v and appends the result
// to buf.
func nativeMarshal(buf *bytes.Buffer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf.Write(data)
	return nil
}

// callMarshalJSON dispatches a mvm MarshalJSON method on ifc, if
// one is registered. ok=false means no such method; err propagates the
// method's own error return.
func callMarshalJSON(m *vm.Machine, ifc vm.Iface) (data []byte, ok bool, err error) {
	method, found := m.MethodByName(ifc.Typ, "MarshalJSON")
	if !found {
		return nil, false, nil
	}
	fval := m.MakeMethodCallable(ifc, method)
	fnType := reflect.TypeOf((func() ([]byte, error))(nil))
	out, err := m.CallFunc(fval, fnType, nil)
	if err != nil {
		return nil, true, err
	}
	if len(out) != 2 {
		return nil, true, fmt.Errorf("MarshalJSON: expected 2 returns, got %d", len(out))
	}
	if out[0].IsValid() && !out[0].IsZero() {
		data = out[0].Bytes()
	}
	if out[1].IsValid() && !out[1].IsNil() {
		if e, eok := out[1].Interface().(error); eok {
			err = e
		}
	}
	return data, true, err
}

// encodeStructTo writes a struct as a JSON object into buf, honouring
// json:"name,opts" tags and promoting anonymous struct fields per
// encoding/json semantics.
func encodeStructTo(buf *bytes.Buffer, m *vm.Machine, rv reflect.Value, t *vm.Type) error {
	buf.WriteByte('{')
	first := true
	if err := writeStructFields(buf, &first, m, rv, t); err != nil {
		return err
	}
	buf.WriteByte('}')
	return nil
}

// writeStructFields emits the key-value pairs of a struct into buf
// without surrounding braces, recursing into anonymous struct fields
// whose mvm type has no MarshalJSON of its own.
func writeStructFields(buf *bytes.Buffer, first *bool, m *vm.Machine, rv reflect.Value, t *vm.Type) error {
	rtype := t.Rtype
	embSet := embeddedFieldSet(t)
	for i := range rtype.NumField() {
		sf := rtype.Field(i)
		tag := sf.Tag.Get("json")
		name, opts := parseJSONTag(tag)
		if name == "-" {
			continue
		}
		fv := rv.Field(i)
		var fieldTyp *vm.Type
		if i < len(t.Fields) {
			fieldTyp = t.Fields[i]
		}
		if tag == "" && (sf.Anonymous || embSet[i]) {
			inner, innerTyp, ok := embeddedStructValue(fv, fieldTyp)
			if ok {
				if _, hasMarshal := m.MethodByName(innerTyp, "MarshalJSON"); !hasMarshal {
					if err := writeStructFields(buf, first, m, inner, innerTyp); err != nil {
						return err
					}
					continue
				}
			}
		}
		if !sf.IsExported() {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		if opts.omitempty && isEmptyValue(fv) {
			continue
		}
		if !*first {
			buf.WriteByte(',')
		}
		*first = false
		nameJSON, _ := json.Marshal(name)
		buf.Write(nameJSON)
		buf.WriteByte(':')
		if err := encodeTo(buf, m, fieldValueForMarshal(fv, fieldTyp)); err != nil {
			return err
		}
	}
	return nil
}

// embeddedFieldSet returns the set of field indices marked embedded
// by mvm. Needed because reflect.StructField.Anonymous may be
// false for some embeds (e.g. when the embedded type has methods -
// see vm.StructOf comments).
func embeddedFieldSet(t *vm.Type) map[int]bool {
	if len(t.Embedded) == 0 {
		return nil
	}
	s := make(map[int]bool, len(t.Embedded))
	for _, e := range t.Embedded {
		s[e.FieldIdx] = true
	}
	return s
}

// embeddedStructValue follows a single pointer indirection (like
// encoding/json) and returns the struct value + mvm type when the
// embedded field is a struct (possibly through a pointer).
func embeddedStructValue(fv reflect.Value, typ *vm.Type) (reflect.Value, *vm.Type, bool) {
	if typ == nil {
		return fv, typ, false
	}
	t := typ
	if t.Rtype.Kind() == reflect.Pointer {
		if !fv.IsValid() || fv.IsNil() {
			return fv, t, false
		}
		fv = fv.Elem()
		t = t.ElemType
		if t == nil {
			return fv, nil, false
		}
	}
	if t.Rtype.Kind() != reflect.Struct {
		return fv, t, false
	}
	return fv, t, true
}

// encodeSliceTo writes a slice or array into buf.
func encodeSliceTo(buf *bytes.Buffer, m *vm.Machine, rv reflect.Value, t *vm.Type) error {
	// []byte is encoded as base64 string per encoding/json semantics.
	if t.Rtype.Elem().Kind() == reflect.Uint8 {
		return nativeMarshal(buf, rv.Bytes())
	}
	if rv.Kind() == reflect.Slice && rv.IsNil() {
		buf.WriteString("null")
		return nil
	}
	buf.WriteByte('[')
	n := rv.Len()
	for i := range n {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := encodeTo(buf, m, fieldValueForMarshal(rv.Index(i), t.ElemType)); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

// encodeMapTo writes a map into buf. Only string-keyed maps are handled
// directly; other key kinds defer to the native encoder (which goes
// through encoding.TextMarshaler).
func encodeMapTo(buf *bytes.Buffer, m *vm.Machine, rv reflect.Value, t *vm.Type) error {
	if rv.IsNil() {
		buf.WriteString("null")
		return nil
	}
	if t.Rtype.Key().Kind() != reflect.String {
		return nativeMarshal(buf, rv.Interface())
	}
	keys := rv.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		nameJSON, _ := json.Marshal(k.String())
		buf.Write(nameJSON)
		buf.WriteByte(':')
		if err := encodeTo(buf, m, fieldValueForMarshal(rv.MapIndex(k), t.ElemType)); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// fieldValueForMarshal wraps a reflect.Value into a mvm Value,
// attaching the mvm type info when available so recursive
// marshalValue can dispatch methods on the field's type.
func fieldValueForMarshal(rv reflect.Value, typ *vm.Type) vm.Value {
	if typ == nil {
		return vm.FromReflect(rv)
	}
	// Iface-typed slot already holds an Iface - pass through.
	if rv.IsValid() && rv.Type() == ifaceRtype {
		return vm.FromReflect(rv)
	}
	// Re-box the field with its known mvm type so downstream
	// method dispatch works even for StructOf-built types.
	return vm.FromReflect(reflect.ValueOf(vm.Iface{Typ: typ, Val: vm.FromReflect(rv)}))
}

// --- helpers ---

type jsonOpts struct {
	omitempty bool
	asString  bool
}

func parseJSONTag(tag string) (name string, opts jsonOpts) {
	if tag == "" {
		return "", opts
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, p := range parts[1:] {
		switch p {
		case "omitempty":
			opts.omitempty = true
		case "string":
			opts.asString = true
		}
	}
	return name, opts
}

func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

var (
	ifaceRtype = reflect.TypeOf(vm.Iface{})

	encoderTypeV    = vm.FromReflect(reflect.ValueOf((*Encoder)(nil)))
	decoderTypeV    = vm.FromReflect(reflect.ValueOf((*Decoder)(nil)))
	newEncoderFuncV = vm.FromReflect(reflect.ValueOf(NewEncoder))
	newDecoderFuncV = vm.FromReflect(reflect.ValueOf(NewDecoder))
)

// Encoder is the mvm-aware replacement for *encoding/json.Encoder.
// Mvm resolves json.Encoder to this type via patchEncodingJSON, so
// user code that spells *json.Encoder actually gets *jsonx.Encoder.
// When mvm calls Encode with an Iface argument, vm.bridgeArgs wraps
// it as a *marshalProxy whose MarshalJSON re-enters the jsonx walker.
type Encoder struct {
	w      io.Writer
	prefix string
	indent string
}

// NewEncoder returns a new jsonx.Encoder that writes to w, matching the
// signature of encoding/json.NewEncoder.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// SetIndent mirrors (*json.Encoder).SetIndent.
func (e *Encoder) SetIndent(prefix, indent string) { e.prefix, e.indent = prefix, indent }

// SetEscapeHTML matches the (*json.Encoder) method set for API
// compatibility. The flag is accepted but unused.
func (e *Encoder) SetEscapeHTML(bool) {}

// Encode serialises v as JSON and writes a newline-terminated line.
// Mvm callers receive v as a *marshalProxy (installed by
// vm.RegisterArgProxyMethod); native json.Marshal finds its
// MarshalJSON and recurses back into the walker.
func (e *Encoder) Encode(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return e.writeEncoded(data)
}

// writeEncoded applies the configured indent and writes data followed
// by a newline. data must be a freshly-owned slice.
func (e *Encoder) writeEncoded(data []byte) error {
	if e.indent != "" || e.prefix != "" {
		var buf bytes.Buffer
		if err := json.Indent(&buf, data, e.prefix, e.indent); err != nil {
			return err
		}
		data = buf.Bytes()
	}
	_, err := e.w.Write(append(data, '\n'))
	return err
}

// Decoder is the mvm-aware replacement for *encoding/json.Decoder.
// It wraps a native *json.Decoder; when mvm calls Decode, the
// argument arrives as a *unmarshalProxy that satisfies json.Unmarshaler,
// so native decoding drives UnmarshalJSON back into the walker.
type Decoder struct {
	dec *json.Decoder
}

// NewDecoder returns a new jsonx.Decoder that reads from r.
func NewDecoder(r io.Reader) *Decoder { return &Decoder{dec: json.NewDecoder(r)} }

// UseNumber forwards to (*json.Decoder).UseNumber.
func (d *Decoder) UseNumber() { d.dec.UseNumber() }

// DisallowUnknownFields forwards to (*json.Decoder).DisallowUnknownFields.
func (d *Decoder) DisallowUnknownFields() { d.dec.DisallowUnknownFields() }

// More forwards to (*json.Decoder).More.
func (d *Decoder) More() bool { return d.dec.More() }

// Buffered forwards to (*json.Decoder).Buffered.
func (d *Decoder) Buffered() io.Reader { return d.dec.Buffered() }

// InputOffset forwards to (*json.Decoder).InputOffset.
func (d *Decoder) InputOffset() int64 { return d.dec.InputOffset() }

// Token forwards to (*json.Decoder).Token.
func (d *Decoder) Token() (json.Token, error) { return d.dec.Token() }

// Decode reads the next JSON value from the stream into v.
func (d *Decoder) Decode(v any) error { return d.dec.Decode(v) }
