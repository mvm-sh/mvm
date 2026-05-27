package vm

import (
	"errors"
	"fmt"
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/vm/synth"
)

// AttachSynthMethods installs t's interpreted methods on a fresh synthesized
// rtype via vm/synth and replaces t.Rtype.
// Native code that asserts the new rtype to an interface (fmt.Stringer,
// error, json.Marshaler, json.Unmarshaler, etc.) then dispatches the
// method directly, with no bridge proxy.
//
// Phase 2c: shape catalog S1 (func() string) / S2 (func() ([]byte, error)) /
// S3 (func([]byte) error) on any supported kind, plus pointer-receiver
// variants on *T via attachPtrType.
// At most ONE method per receiver kind per type per call; multi-method
// support lands in Phase 2d.
//
// Re-allocation of existing values is out of scope: global slots populated
// before this call keep their old rtype.
// New values allocated via vm.NewValue against t.Rtype after this call see
// the synth rtype.
func (m *Machine) AttachSynthMethods(t *Type) error {
	if !synth.Enabled() || t == nil || t.Rtype == nil {
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

func (m *Machine) attachValueRecv(t *Type) (bool, error) {
	spec, ok := m.firstSynthMethod(t, false)
	if !ok {
		return false, nil
	}
	newRT, err := synth.AttachMethods(t.Rtype, t.Name, t.PkgPath,
		spec.toSynthMethod(m, t, false))
	if err != nil {
		return false, err
	}
	t.Rtype = newRT
	return true, nil
}

// attachPtrRecv installs a ptr-recv method on *T.
// elemReady reports whether t.Rtype is already a fresh synth elem we own.
// If not, clone the original layout first so attachPtrType writes
// PtrToThis into our own rtype rather than the layout shared with reflect's
// caches (structLookupCache for struct, the canonical native rtype for
// primitives/slices/arrays/maps).
func (m *Machine) attachPtrRecv(t *Type, elemReady bool) error {
	spec, ok := m.firstSynthMethod(t, true)
	if !ok {
		return nil
	}
	if !elemReady {
		clone, err := synth.Clone(t.Rtype, t.PkgPath)
		if err != nil {
			return err
		}
		t.Rtype = clone
	}
	_, err := synth.AttachPtrMethods(t.Rtype, "*"+t.Name, t.PkgPath,
		spec.toSynthMethod(m, t, true))
	return err
}

// synthMethodSpec describes a single method picked for synth attachment.
// shape is the matched signature shape; name comes from MethodNames.
type synthMethodSpec struct {
	name   string
	method Method
	shape  synth.Shape
}

// toSynthMethod builds the synth.Method (with the right shape-typed handler
// closure) for installation against t.
// ptrRecv selects how recv is interpreted when the stub fires.
func (s synthMethodSpec) toSynthMethod(m *Machine, t *Type, ptrRecv bool) synth.Method {
	var handler any
	switch s.shape {
	case synth.ShapeS1:
		handler = makeHandlerS1(m, t, s.method, ptrRecv)
	case synth.ShapeS2:
		handler = makeHandlerS2(m, t, s.method, ptrRecv)
	case synth.ShapeS3:
		handler = makeHandlerS3(m, t, s.method, ptrRecv)
	}
	return synth.Method{
		Name:     s.name,
		Exported: true,
		Sig:      s.method.Rtype,
		Shape:    s.shape,
		Handler:  handler,
	}
}

// firstSynthMethod returns the highest-priority resolved method on t whose
// signature matches a supported shape and whose PtrRecv matches wantPtr.
//
// Phase 2c attaches at most one method per receiver kind per type.
// When a type defines methods of multiple shapes (e.g. both String() and
// MarshalJSON() as value-recv), priority is fixed at S1 > S2 > S3:
//   - S1 (Stringer/Error) is the broadest interop hook; losing it degrades
//     fmt printing AND error interop simultaneously.
//   - S2/S3 (Marshaler/Unmarshaler) callers will fall through to the
//     legacy bridge until Phase 2d's multi-method containers land.
//
// Name filtering is intentionally absent: which method names matter is a
// stdlib-layer concern, not a vm concern.
func (m *Machine) firstSynthMethod(
	t *Type, wantPtr bool,
) (synthMethodSpec, bool) {
	var best synthMethodSpec
	bestRank := -1
	for i, method := range t.Methods {
		if !method.IsResolved() || i >= len(m.MethodNames) {
			continue
		}
		if method.PtrRecv != wantPtr {
			continue
		}
		shape, ok := detectShape(method.Rtype)
		if !ok {
			continue
		}
		rank := shapeRank(shape)
		if bestRank == -1 || rank < bestRank {
			best = synthMethodSpec{
				name:   m.MethodNames[i],
				method: method,
				shape:  shape,
			}
			bestRank = rank
			if rank == 0 {
				break // S1 is highest priority; no need to keep scanning
			}
		}
	}
	return best, bestRank != -1
}

// shapeRank returns the priority for a shape; lower is higher priority.
func shapeRank(s synth.Shape) int {
	switch s {
	case synth.ShapeS1:
		return 0
	case synth.ShapeS2:
		return 1
	case synth.ShapeS3:
		return 2
	}
	return 99
}

// detectShape inspects a method signature (receiver elided) and returns the
// matching synth.Shape if any.
// Recognized shapes:
//
//	S1: func() string
//	S2: func() ([]byte, error)
//	S3: func([]byte) error
func detectShape(sig reflect.Type) (synth.Shape, bool) {
	if sig == nil || sig.Kind() != reflect.Func {
		return 0, false
	}
	nin, nout := sig.NumIn(), sig.NumOut()
	switch {
	case nin == 0 && nout == 1 && sig.Out(0).Kind() == reflect.String:
		return synth.ShapeS1, true
	case nin == 0 && nout == 2 &&
		isByteSlice(sig.Out(0)) && isErrorType(sig.Out(1)):
		return synth.ShapeS2, true
	case nin == 1 && nout == 1 &&
		isByteSlice(sig.In(0)) && isErrorType(sig.Out(0)):
		return synth.ShapeS3, true
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
	errorIface    = reflect.TypeOf((*error)(nil)).Elem()
	byteSliceType = reflect.TypeOf([]byte(nil))
)

func isByteSlice(t reflect.Type) bool { return t == byteSliceType }

func isErrorType(t reflect.Type) bool { return t == errorIface }

// makeHandlerS1 builds the per-method bridge closure for shape S1.
// For ptrRecv methods, recv from the stub IS the *T pointer (direct-iface);
// the receiver Value is reflect.NewAt(t.Rtype, recv).
// For value-recv methods, recv points at boxed T storage; the receiver Value
// is reflect.NewAt(t.Rtype, recv).Elem().
func makeHandlerS1(m *Machine, t *Type, method Method, ptrRecv bool) synth.HandlerS1 {
	rtype, ifcType, methodSig := t.Rtype, t, method.Rtype
	return func(recv unsafe.Pointer) string {
		rv := makeRecvValue(rtype, recv, ptrRecv)
		out, err := callMethod(m, ifcType, rv, method, methodSig, nil)
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
	rtype, ifcType, methodSig := t.Rtype, t, method.Rtype
	return func(recv unsafe.Pointer) ([]byte, error) {
		rv := makeRecvValue(rtype, recv, ptrRecv)
		out, err := callMethod(m, ifcType, rv, method, methodSig, nil)
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
	rtype, ifcType, methodSig := t.Rtype, t, method.Rtype
	return func(recv unsafe.Pointer, data []byte) error {
		rv := makeRecvValue(rtype, recv, ptrRecv)
		argv := []reflect.Value{reflect.ValueOf(data)}
		out, err := callMethod(m, ifcType, rv, method, methodSig, argv)
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
