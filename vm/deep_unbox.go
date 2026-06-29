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

// seenBefore records k in seen (allocating it on first use), reporting
// whether k was already visited.
func seenBefore(seen *map[unboxSeen]bool, k unboxSeen) bool {
	if (*seen)[k] {
		return true
	}
	if *seen == nil {
		*seen = map[unboxSeen]bool{}
	}
	(*seen)[k] = true
	return false
}

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
	if t == synthErrShimType || t == synthStrShimType ||
		t == synthReaderShimType || t == synthWriterShimType {
		return false // opaque native shim; never walk its *Machine field
	}
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
		for field := range t.Fields() {
			if ifaceBearingWalk(field.Type, seen) {
				return true
			}
		}
	}
	return false
}

// maxMapHops gates map iteration by heap-indirection depth.
// A fresh composite arg holds its boxes within 1 hop; a map reached deeper
// belongs to the broader object graph (a live serverConn) where iterating it
// finds no boxes and races a concurrent writer (a "concurrent map iteration and
// map write" fatal).
const maxMapHops = 1

// deepUnboxIface returns v with nested vm.Iface boxes replaced by their
// concrete values, so native reflect walks (DeepEqual, fmt) see real Go values.
// Value types (struct/array/interface) are copied on change only; reference
// types (pointer/slice/map) are unboxed IN PLACE so a callee mutating through
// them still reaches the caller's data (e.g. zerolog's hook.Run(e) appends to
// e.buf; a rebuilt *Event would detach the write).
// hops counts heap indirections from the arg root (pointer, slice, map edges;
// not by-value struct/array), gating map iteration (see maxMapHops).
// The bool reports a REPLACED value (value types only); false from a
// reference type may still mean its contents were unboxed in place.
func (m *Machine) deepUnboxIface(v reflect.Value, depth, hops int, seen map[unboxSeen]bool) (reflect.Value, bool) {
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
		if !el.IsValid() {
			// A non-nil interface whose Elem is unreadable: a native unexported
			// field (e.g. log.Logger.out) can reflect with IsNil false yet a zero
			// Elem. Nothing to unbox; leave it for the native call unchanged.
			return v, false
		}
		if el.Type() == ifaceRtype {
			ifc := el.Interface().(Iface)
			w := m.bridgeIface(ifc, t)
			if !w.IsValid() {
				return v, false
			}
			if dw, ch := m.deepUnboxIface(w, depth+1, hops, seen); ch {
				w = dw
			}
			if !w.Type().AssignableTo(t) {
				return v, false
			}
			return w, true
		}
		if w, ch := m.deepUnboxIface(el, depth+1, hops, seen); ch && w.Type().AssignableTo(t) {
			return w, true
		}
		return v, false
	case reflect.Slice:
		if v.IsNil() || !ifaceBearing(t) {
			return v, false
		}
		if seenBefore(&seen, unboxSeen{v.Pointer(), t}) {
			return v, false
		}
		// In place: elements are settable through the data pointer, and the
		// backing array may be aliased elsewhere.
		for i := range v.Len() {
			el := v.Index(i)
			if w, ch := m.deepUnboxIface(el, depth+1, hops+1, seen); ch {
				Exportable(el).Set(Exportable(w))
			}
		}
		return v, false
	case reflect.Array:
		if !ifaceBearing(t) {
			return v, false
		}
		var out reflect.Value
		changed := false
		for i := range t.Len() {
			w, ch := m.deepUnboxIface(v.Index(i), depth+1, hops, seen)
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
			w, ch := m.deepUnboxIface(v.Field(i), depth+1, hops, seen)
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
		if seenBefore(&seen, unboxSeen{v.Pointer(), t}) {
			return v, false
		}
		// In place: the callee must see the SAME pointer, or its writes
		// through it land in a detached copy.
		el := v.Elem()
		if w, ch := m.deepUnboxIface(el, depth+1, hops+1, seen); ch {
			Exportable(el).Set(Exportable(w))
		}
		return v, false
	case reflect.Map:
		if v.IsNil() || !ifaceBearing(t) || hops > maxMapHops {
			return v, false
		}
		if seenBefore(&seen, unboxSeen{v.Pointer(), t}) {
			return v, false
		}
		// In place (the map may be aliased elsewhere): collect changed
		// entries first, then apply, to not mutate during iteration.
		type entry struct {
			oldKey, newKey, val reflect.Value
			keyChanged          bool
		}
		var changes []entry
		it := v.MapRange()
		for it.Next() {
			k, kc := m.deepUnboxIface(it.Key(), depth+1, hops+1, seen)
			w, wc := m.deepUnboxIface(it.Value(), depth+1, hops+1, seen)
			if !kc && !wc {
				continue
			}
			changes = append(changes, entry{it.Key(), k, w, kc})
		}
		mv := Exportable(v)
		for _, c := range changes {
			if c.keyChanged {
				mv.SetMapIndex(Exportable(c.oldKey), reflect.Value{})
			}
			mv.SetMapIndex(Exportable(c.newKey), Exportable(c.val))
		}
		return v, false
	}
	return v, false
}
