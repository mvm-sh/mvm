package vm

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/mvm-sh/mvm/vm/synth"
)

// AttachSynthMethods installs t's interpreted methods on a fresh synthesized
// rtype via vm/synth and replaces t.Rtype.
// Native code that asserts the new rtype to an interface (fmt.Stringer,
// error, etc.) then dispatches the method directly, with no bridge proxy.
//
// Phase 2a: shape S1 (func() string) on struct kinds, plus pointer-receiver
// variants installed on *T via attachPtrType.
// No-op for other shapes, other kinds, or when synth.Enabled() is false.
// Installs at most one value-recv method on T and one ptr-recv method on *T
// per call; multi-method support lands in Phase 2d.
//
// Re-allocation of existing values is out of scope: global slots populated
// before this call keep their old rtype.
// New values allocated via vm.NewValue against t.Rtype after this call see
// the synth rtype.
func (m *Machine) AttachSynthMethods(t *Type) error {
	if !synth.Enabled() || t == nil || t.Rtype == nil {
		return nil
	}
	if t.Rtype.Kind() != reflect.Struct {
		return nil
	}

	valueAttached, err := m.attachValueRecvS1(t)
	if err != nil {
		return err
	}
	return m.attachPtrRecvS1(t, valueAttached)
}

func (m *Machine) attachValueRecvS1(t *Type) (bool, error) {
	name, method, ok := m.firstS1Method(t, false)
	if !ok {
		return false, nil
	}
	handler := m.makeS1Handler(t, method, false)
	newRT, err := synth.AttachStructMethods(t.Rtype, t.PkgPath, synth.Method{
		Name:     name,
		Exported: true,
		Sig:      method.Rtype,
		Handler:  handler,
	})
	if err != nil {
		return false, err
	}
	t.Rtype = newRT
	return true, nil
}

// attachPtrRecvS1 installs a ptr-recv shape-S1 method on *T.
// elemReady reports whether t.Rtype is already a fresh synth elem we own.
// If not, we clone the original struct layout first so attachPtrType writes
// PtrToThis into our own rtype rather than the layout shared with reflect's
// structLookupCache.
func (m *Machine) attachPtrRecvS1(t *Type, elemReady bool) error {
	name, method, ok := m.firstS1Method(t, true)
	if !ok {
		return nil
	}
	if !elemReady {
		clone, err := synth.CloneStruct(t.Rtype, t.PkgPath)
		if err != nil {
			return err
		}
		t.Rtype = clone
	}
	handler := m.makeS1Handler(t, method, true)
	_, err := synth.AttachPtrMethods(t.Rtype, "*"+t.Name, t.PkgPath, synth.Method{
		Name:     name,
		Exported: true,
		Sig:      method.Rtype,
		Handler:  handler,
	})
	return err
}

// makeS1Handler builds the per-method bridge closure.
// For ptrRecv methods, recv from the stub IS the *T pointer (direct-iface);
// the receiver Value is reflect.NewAt(t.Rtype, recv), i.e. a *T.
// For value-recv methods, recv points at boxed T storage; the receiver Value
// is reflect.NewAt(t.Rtype, recv).Elem(), i.e. a T.
func (m *Machine) makeS1Handler(
	t *Type, method Method, ptrRecv bool,
) synth.HandlerS1 {
	rtype := t.Rtype
	ifcType := t
	methodSig := method.Rtype

	return func(recv unsafe.Pointer) string {
		var rv reflect.Value
		if ptrRecv {
			rv = reflect.NewAt(rtype, recv)
		} else {
			rv = reflect.NewAt(rtype, recv).Elem()
		}
		ifc := Iface{Typ: ifcType, Val: FromReflect(rv)}
		fval := m.MakeMethodCallable(ifc, method)
		out, err := m.CallFunc(fval, methodSig, nil)
		if err != nil {
			return fmt.Sprintf("<synth dispatch error: %v>", err)
		}
		if len(out) != 1 {
			return ""
		}
		return out[0].String()
	}
}

// firstS1Method returns the first resolved method on t whose signature
// matches shape S1 (no args beyond receiver, single string return) and
// whose PtrRecv matches the wantPtr filter.
// Name filtering is intentionally absent: which method names matter is a
// stdlib-layer concern, not a vm concern.
func (m *Machine) firstS1Method(
	t *Type, wantPtr bool,
) (name string, mh Method, ok bool) {
	for i, method := range t.Methods {
		if !method.IsResolved() || i >= len(m.MethodNames) {
			continue
		}
		if method.PtrRecv != wantPtr {
			continue
		}
		if method.Rtype == nil ||
			method.Rtype.NumIn() != 0 ||
			method.Rtype.NumOut() != 1 ||
			method.Rtype.Out(0).Kind() != reflect.String {
			continue
		}
		return m.MethodNames[i], method, true
	}
	return "", Method{}, false
}
