package derive

import (
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mvm-sh/mvm/mtype"
	"github.com/mvm-sh/mvm/runtype"
)

// SynthIfaceRtype returns t's method-bearing synthetic interface rtype, building
// and caching it on first use (nil if a method signature cannot be built; the
// any bridge stays and a later call retries). Clones of one named interface
// dedupe by synthIfaceNameKey to a single rtype identity.
//
// When some method signature is still unmaterialized, t's rtype is reserved and
// cached before the signatures are built, so a self- or mutually-recursive
// reference within them (type EnumType <-> Enum) resolves to this final pointer
// rather than erasing a cycle-breaking back-edge whose location -- and thus a
// later reflect.Implements -- would depend on materialization order. That path
// materializes signatures and so requires materializeMu; the only caller that
// may pass unmaterialized signatures (materializeFuncIO) holds it, while callers
// off the lock (bridgePtrToIface) pre-materialize first, taking the atomic path.
func SynthIfaceRtype(t *mtype.Type) reflect.Type {
	derivedMu.Lock()
	if st := synthIfaceCache[t]; st != nil {
		derivedMu.Unlock()
		return st
	}
	key := synthIfaceNameKey(t)
	if key != "" {
		if st := synthIfaceNamed[key]; st != nil {
			synthIfaceCache[t] = st
			derivedMu.Unlock()
			return st
		}
	}
	name := synthIfaceName(t)
	if name == "" {
		derivedMu.Unlock()
		return nil
	}
	if ims, ok := collectImethods(t, false); ok {
		// All signatures materialized: no cycle to break, build atomically.
		rt := runtype.InterfaceOf(name, RtypePkgPath(t), ims)
		cacheSynthIface(t, key, rt)
		derivedMu.Unlock()
		return rt
	}
	h := runtype.ReserveInterface(name, RtypePkgPath(t))
	rt := h.Type()
	cacheSynthIface(t, key, rt)
	derivedMu.Unlock()

	ims, ok := collectImethods(t, true)
	if !ok {
		derivedMu.Lock()
		uncacheSynthIface(t, key)
		derivedMu.Unlock()
		return nil
	}
	h.FillMethods(ims)
	return rt
}

// cacheSynthIface records rt as t's synth interface rtype and under its
// cross-clone dedupe key. Caller holds derivedMu.
func cacheSynthIface(t *mtype.Type, key string, rt reflect.Type) {
	synthIfaceCache[t] = rt
	if key != "" {
		synthIfaceNamed[key] = rt
	}
}

// uncacheSynthIface rolls back a reservation whose method sigs could not be
// built. Caller holds derivedMu.
func uncacheSynthIface(t *mtype.Type, key string) {
	delete(synthIfaceCache, t)
	if key != "" {
		delete(synthIfaceNamed, key)
	}
}

// synthIfaceName is t's interface name for the synth rtype, falling back to the
// reflect string of an already-bound rtype for an unnamed interface.
func synthIfaceName(t *mtype.Type) string {
	if name := QualifiedTypeName(t); name != "" {
		return name
	}
	if t.Rtype != nil {
		return t.Rtype.String()
	}
	return ""
}

// collectImethods builds the runtype.Imethod set from t's interface methods.
// With materializeSigs it fills an unset im.Rtype from im.Sig (needs materializeMu).
// ok is false if any method signature is still missing or non-func.
func collectImethods(t *mtype.Type, materializeSigs bool) (ims []runtype.Imethod, ok bool) {
	ims = make([]runtype.Imethod, 0, len(t.IfaceMethods))
	for i := range t.IfaceMethods {
		im := &t.IfaceMethods[i]
		if materializeSigs && im.Rtype == nil && im.Sig != nil {
			im.Rtype = materialize(im.Sig)
		}
		if im.Rtype == nil || im.Rtype.Kind() != reflect.Func {
			return nil, false
		}
		ims = append(ims, runtype.Imethod{
			Name:     im.Name,
			Exported: IsExportedName(im.Name),
			Sig:      im.Rtype,
		})
	}
	return ims, len(ims) > 0
}

// IsGenericInstanceName reports a generic instantiation, mangled with '#'.
func IsGenericInstanceName(name string) bool {
	return strings.IndexByte(name, '#') >= 0
}

// IsExportedName reports whether name is exported (leading upper-case rune).
func IsExportedName(name string) bool {
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// RtypePkgPath is the import path stamped into a synth rtype's uncommon section,
// surfaced by reflect.Type.PkgPath(). Native Go returns the full import path
// ("gorm.io/gorm"), not the short package name ("gorm"); prefer ImportPath and
// fall back to PkgName for main/REPL/synthetic types (ImportPath == "").
func RtypePkgPath(t *mtype.Type) string {
	if t.ImportPath != "" {
		return t.ImportPath
	}
	return t.PkgName
}

// UnexportedMethodPkg returns the declaring package's import path for an
// unexported method reached via method.Path from t, or "" if the method is
// exported. The path lets reflect.Implements match the embedded pkgPath against an
// interface's PkgPath (RtypePkgPath resolves both to the import path).
func UnexportedMethodPkg(t *mtype.Type, method mtype.Method, name string) string {
	if IsExportedName(name) {
		return ""
	}
	cur := t
	for _, idx := range method.Path {
		next := cur.EmbeddedTypeAt(idx)
		if next == nil {
			break
		}
		cur = next
	}
	return RtypePkgPath(cur)
}

// QualifiedTypeName is t's "pkg.Name" identity, or just Name when t has no package.
func QualifiedTypeName(t *mtype.Type) string {
	if t.PkgName == "" || t.Name == "" {
		return t.Name
	}
	base := t.PkgName
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base + "." + t.Name
}

// EraseSynthIfaceParams replaces synth non-empty interface params/results with any.
// The erased shape then matches a generic dispatch stub.
func EraseSynthIfaceParams(sig reflect.Type) reflect.Type {
	if sig == nil || sig.Kind() != reflect.Func {
		return sig
	}
	changed := false
	conv := func(t reflect.Type) reflect.Type {
		if t.Kind() == reflect.Interface && t.NumMethod() > 0 && runtype.IsSynth(t) {
			changed = true
			return mtype.AnyRtype
		}
		return t
	}
	in := make([]reflect.Type, sig.NumIn())
	for i := range in {
		in[i] = conv(sig.In(i))
	}
	out := make([]reflect.Type, sig.NumOut())
	for i := range out {
		out[i] = conv(sig.Out(i))
	}
	if !changed {
		return sig
	}
	return reflect.FuncOf(in, out, sig.IsVariadic())
}

// IsDirectIface reports whether rt is held directly in an interface word, not boxed.
func IsDirectIface(rt reflect.Type) bool {
	switch rt.Kind() {
	case reflect.Pointer, reflect.Chan, reflect.Map, reflect.Func, reflect.UnsafePointer:
		return true
	case reflect.Struct:
		return rt.NumField() == 1 && IsDirectIface(rt.Field(0).Type)
	case reflect.Array:
		return rt.Len() == 1 && IsDirectIface(rt.Elem())
	default:
		return false
	}
}

// StripRecvType drops the receiver (first param) from a bound method rtype.
func StripRecvType(mt reflect.Type) reflect.Type {
	if mt.NumIn() == 0 {
		return mt
	}
	in := make([]reflect.Type, 0, mt.NumIn()-1)
	for i := 1; i < mt.NumIn(); i++ {
		in = append(in, mt.In(i))
	}
	out := make([]reflect.Type, mt.NumOut())
	for i := range out {
		out[i] = mt.Out(i)
	}
	return reflect.FuncOf(in, out, mt.IsVariadic())
}
