package vm

import (
	"reflect"
	"sync"
)

// maxUnboxDepth bounds the deep-unbox walk on pathological data.
const maxUnboxDepth = 64

// unboxSeen identifies a pointer-like node already visited in one walk.
// Cyclic graphs (e.g. testing.T parent/sub links) would otherwise be
// re-walked at every depth up to maxUnboxDepth -- an exponential spin.
type unboxSeen struct {
	ptr uintptr
	t   reflect.Type
}

var ifaceBearingCache sync.Map // reflect.Type -> bool

// ifaceBearing reports whether values of type t can contain an interface slot.
func ifaceBearing(t reflect.Type) bool {
	if v, ok := ifaceBearingCache.Load(t); ok {
		return v.(bool)
	}
	r := ifaceBearingWalk(t, map[reflect.Type]bool{})
	ifaceBearingCache.Store(t, r)
	return r
}

func ifaceBearingWalk(t reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Interface:
		return true
	case reflect.Slice, reflect.Array, reflect.Pointer:
		return ifaceBearingWalk(t.Elem(), seen)
	case reflect.Map:
		return ifaceBearingWalk(t.Key(), seen) || ifaceBearingWalk(t.Elem(), seen)
	case reflect.Struct:
		for i := range t.NumField() {
			if ifaceBearingWalk(t.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}

// deepUnboxIface returns v with nested vm.Iface boxes replaced by their
// concrete values, so native reflect walks (DeepEqual, fmt) see real Go values.
// Composites are copied on change only; no box found returns v unchanged.
// Write-back through a copied composite is lost (already broken for boxed
// elements).
func (m *Machine) deepUnboxIface(v reflect.Value, depth int, seen map[unboxSeen]bool) (reflect.Value, bool) {
	if depth > maxUnboxDepth || !v.IsValid() {
		return v, false
	}
	t := v.Type()
	switch t.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v, false
		}
		el := Exportable(v.Elem())
		if el.Type() == ifaceRtype {
			ifc := el.Interface().(Iface)
			w := m.bridgeIface(ifc, t)
			if !w.IsValid() {
				return v, false
			}
			if dw, ch := m.deepUnboxIface(w, depth+1, seen); ch {
				w = dw
			}
			if !w.Type().AssignableTo(t) {
				return v, false
			}
			return w, true
		}
		if w, ch := m.deepUnboxIface(el, depth+1, seen); ch && w.Type().AssignableTo(t) {
			return w, true
		}
		return v, false
	case reflect.Slice:
		if v.IsNil() || !ifaceBearing(t) {
			return v, false
		}
		if seen[unboxSeen{v.Pointer(), t}] {
			return v, false
		}
		if seen == nil {
			seen = map[unboxSeen]bool{}
		}
		seen[unboxSeen{v.Pointer(), t}] = true
		n := v.Len()
		var out reflect.Value
		changed := false
		for i := range n {
			w, ch := m.deepUnboxIface(v.Index(i), depth+1, seen)
			if !ch {
				continue
			}
			if !changed {
				out = reflect.MakeSlice(t, n, n)
				reflect.Copy(out, Exportable(v))
				changed = true
			}
			out.Index(i).Set(Exportable(w))
		}
		if !changed {
			return v, false
		}
		return out, true
	case reflect.Array:
		if !ifaceBearing(t) {
			return v, false
		}
		var out reflect.Value
		changed := false
		for i := range t.Len() {
			w, ch := m.deepUnboxIface(v.Index(i), depth+1, seen)
			if !ch {
				continue
			}
			if !changed {
				out = reflect.New(t).Elem()
				out.Set(Exportable(v))
				changed = true
			}
			out.Index(i).Set(Exportable(w))
		}
		if !changed {
			return v, false
		}
		return out, true
	case reflect.Struct:
		if !ifaceBearing(t) {
			return v, false
		}
		var out reflect.Value
		changed := false
		for i := range t.NumField() {
			w, ch := m.deepUnboxIface(v.Field(i), depth+1, seen)
			if !ch {
				continue
			}
			if !changed {
				out = reflect.New(t).Elem()
				out.Set(Exportable(v))
				changed = true
			}
			Exportable(out.Field(i)).Set(Exportable(w))
		}
		if !changed {
			return v, false
		}
		return out, true
	case reflect.Pointer:
		if v.IsNil() || !ifaceBearing(t) {
			return v, false
		}
		if seen[unboxSeen{v.Pointer(), t}] {
			return v, false
		}
		if seen == nil {
			seen = map[unboxSeen]bool{}
		}
		seen[unboxSeen{v.Pointer(), t}] = true
		w, ch := m.deepUnboxIface(v.Elem(), depth+1, seen)
		if !ch {
			return v, false
		}
		np := reflect.New(t.Elem())
		np.Elem().Set(Exportable(w))
		if np.Type() != t {
			np = np.Convert(t)
		}
		return np, true
	case reflect.Map:
		if v.IsNil() || !ifaceBearing(t) {
			return v, false
		}
		if seen[unboxSeen{v.Pointer(), t}] {
			return v, false
		}
		if seen == nil {
			seen = map[unboxSeen]bool{}
		}
		seen[unboxSeen{v.Pointer(), t}] = true
		var out reflect.Value
		changed := false
		it := v.MapRange()
		for it.Next() {
			k, kc := m.deepUnboxIface(it.Key(), depth+1, seen)
			w, wc := m.deepUnboxIface(it.Value(), depth+1, seen)
			if !kc && !wc {
				continue
			}
			if !changed {
				out = reflect.MakeMapWithSize(t, v.Len())
				inner := v.MapRange()
				for inner.Next() {
					out.SetMapIndex(Exportable(inner.Key()), Exportable(inner.Value()))
				}
				changed = true
			}
			if kc {
				out.SetMapIndex(Exportable(it.Key()), reflect.Value{})
			}
			out.SetMapIndex(Exportable(k), Exportable(w))
		}
		if !changed {
			return v, false
		}
		return out, true
	}
	return v, false
}
