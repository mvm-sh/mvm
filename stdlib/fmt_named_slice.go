package stdlib

import (
	"fmt"
	"io"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// namedSliceArg wraps a slice whose mvm element type carries a user-level
// name that the underlying reflect.Type alone cannot recover (e.g. `type
// Frame uintptr` collapses to `uintptr` at the reflect layer). Implementing
// fmt.Formatter lets the wrapper render `%#v` as `[]<pkg>.<Name>{...}` and
// dispatch each element through the user Format method, while delegating
// other verbs to native fmt.
type namedSliceArg struct {
	slice  reflect.Value
	prefix string
	elemFn func(any, fmt.State, rune)
}

func (w *namedSliceArg) Format(s fmt.State, verb rune) {
	if verb != 'v' || !s.Flag('#') {
		_, _ = fmt.Fprintf(s, fmt.FormatString(s, verb), w.slice.Interface())
		return
	}
	switch {
	case !w.slice.IsValid() || w.slice.IsNil():
		_, _ = io.WriteString(s, w.prefix+"(nil)")
	case w.slice.Len() == 0:
		_, _ = io.WriteString(s, w.prefix+"{}")
	default:
		_, _ = io.WriteString(s, w.prefix+"{")
		for i, n := 0, w.slice.Len(); i < n; i++ {
			if i > 0 {
				_, _ = io.WriteString(s, ", ")
			}
			elem := w.slice.Index(i).Interface()
			if w.elemFn != nil {
				w.elemFn(elem, s, 'v')
			} else {
				_, _ = fmt.Fprintf(s, "%#v", elem)
			}
		}
		_, _ = io.WriteString(s, "}")
	}
}

func (w *namedSliceArg) String() string { return fmt.Sprintf("%v", w.slice.Interface()) }

func init() { vm.IfaceFallbackHook = ifaceFallbackHook }

// ifaceFallbackHook substitutes an mvm slice flowing into `any` with a
// fmt.Formatter wrapper when the element type defines a Format method.
// Gating on Format keeps plain slices like []int passing through as real
// reflect slices so reflect-based callers (sort.Slice, json.Marshal, ...)
// keep working.
func ifaceFallbackHook(m *vm.Machine, ifc vm.Iface, targetType reflect.Type) reflect.Value {
	if targetType.Kind() != reflect.Interface || targetType.NumMethod() != 0 || ifc.Typ == nil {
		return reflect.Value{}
	}
	rv := ifc.Val.Reflect()
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return reflect.Value{}
	}
	elemTyp := ifc.Typ.ElemType
	if elemTyp == nil {
		return reflect.Value{}
	}
	elemFn := makeElemFormatFn(m, elemTyp)
	if elemFn == nil {
		return reflect.Value{}
	}
	return reflect.ValueOf(&namedSliceArg{
		slice:  rv,
		prefix: "[]" + elemTyp.String(),
		elemFn: elemFn,
	})
}

// formatFnType is the reflect type of an interpreted Format method body
// after the receiver is bound (the receiver becomes the closure's first
// heap cell): func(fmt.State, rune).
var formatFnType = reflect.TypeOf(func(fmt.State, rune) {})

// makeElemFormatFn returns a per-element formatter that dispatches to the
// element type's interpreted Format method, or nil if none is registered.
func makeElemFormatFn(m *vm.Machine, elem *vm.Type) func(any, fmt.State, rune) {
	if m == nil || elem == nil {
		return nil
	}
	method, ok := m.MethodByName(elem, "Format")
	if !ok {
		return nil
	}
	return func(v any, s fmt.State, verb rune) {
		ifc := vm.Iface{Typ: elem, Val: vm.FromReflect(reflect.ValueOf(v))}
		fval := m.MakeMethodCallable(ifc, method)
		if !fval.Reflect().IsValid() {
			_, _ = fmt.Fprintf(s, "%v", v)
			return
		}
		if _, err := m.CallFunc(fval, formatFnType, []reflect.Value{reflect.ValueOf(s), reflect.ValueOf(verb)}); err != nil {
			_, _ = fmt.Fprintf(s, "%v", v)
		}
	}
}
