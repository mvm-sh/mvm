package stdlib

import (
	"fmt"
	"io"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// compositeFmtArg wraps an mvm slice whose element type defines a display
// method (String/Error/Format/GoString) so native fmt renders each element
// through that interpreted method. Native fmt sees only the reflect element
// type, which carries no Go-level methods for an interpreted type, so without
// this it falls back to default formatting -- e.g. printing `{Bob 31}` for a
// []Person instead of the Stringer's `Bob: 31`. For `%#v` it also recovers the
// user-level slice type name that the reflect type alone loses for a defined
// type over a basic kind (e.g. `type Frame uintptr` -> `[]uintptr`).
type compositeFmtArg struct {
	m        *vm.Machine
	slice    reflect.Value
	elemTyp  *vm.Type
	prefix   string // "[]<pkg>.<Name>", for %#v
	goSyntax bool   // element has Format/GoString: honor it under %#v
}

func (w *compositeFmtArg) Format(s fmt.State, verb rune) {
	n := 0
	if w.slice.IsValid() && !w.slice.IsNil() {
		n = w.slice.Len()
	}
	// %#v keeps the Go-syntax form with the user-level slice type name.
	if verb == 'v' && s.Flag('#') {
		switch {
		case !w.slice.IsValid() || w.slice.IsNil():
			_, _ = io.WriteString(s, w.prefix+"(nil)")
		case n == 0:
			_, _ = io.WriteString(s, w.prefix+"{}")
		default:
			_, _ = io.WriteString(s, w.prefix+"{")
			for i := 0; i < n; i++ {
				if i > 0 {
					_, _ = io.WriteString(s, ", ")
				}
				// %#v honors Format/GoString but never String/Error, so only
				// route through the display bridge when the element provides a
				// Go-syntax method; otherwise print the raw element.
				if w.goSyntax {
					_, _ = fmt.Fprintf(s, "%#v", w.elem(i))
				} else {
					_, _ = fmt.Fprintf(s, "%#v", w.slice.Index(i).Interface())
				}
			}
			_, _ = io.WriteString(s, "}")
		}
		return
	}
	// Every other verb: hand native fmt a []any of per-element display
	// wrappers, so it formats the slice exactly as it would a native one and
	// each element's method drives its own rendering.
	wrapped := make([]any, n)
	for i := 0; i < n; i++ {
		wrapped[i] = w.elem(i)
	}
	_, _ = fmt.Fprintf(s, fmt.FormatString(s, verb), wrapped)
}

// elem returns the i-th element wrapped as a display bridge -- the same
// wrapping a standalone value receives flowing into an interface{} arg.
func (w *compositeFmtArg) elem(i int) any {
	ev := reflect.ValueOf(w.slice.Index(i).Interface())
	if rv := w.m.BridgeForAny(vm.Iface{Typ: w.elemTyp, Val: vm.FromReflect(ev)}); rv.IsValid() && rv.CanInterface() {
		return rv.Interface()
	}
	return ev.Interface()
}

func init() { vm.IfaceFallbackHook = ifaceFallbackHook }

// ifaceFallbackHook substitutes an mvm slice flowing into `any` with a fmt
// wrapper when the element type defines a display method. Gating on a display
// method keeps plain slices ([]int, []string, ...) passing through as real
// reflect slices, so reflect-based callers (sort.Slice, json.Marshal, ...)
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
	if elemTyp == nil || !hasDisplayMethod(m, elemTyp) {
		return reflect.Value{}
	}
	return reflect.ValueOf(&compositeFmtArg{
		m:        m,
		slice:    rv,
		elemTyp:  elemTyp,
		prefix:   "[]" + elemTyp.String(),
		goSyntax: hasMethod(m, elemTyp, "Format") || hasMethod(m, elemTyp, "GoString"),
	})
}

func hasMethod(m *vm.Machine, elem *vm.Type, name string) bool {
	_, ok := m.MethodByName(elem, name)
	return ok
}

// hasDisplayMethod reports whether elem defines a method fmt dispatches on
// (String/Error/Format/GoString) -- the same set used to pick a display bridge
// for a standalone value.
func hasDisplayMethod(m *vm.Machine, elem *vm.Type) bool {
	for name := range vm.DisplayBridges {
		if hasMethod(m, elem, name) {
			return true
		}
	}
	return false
}
