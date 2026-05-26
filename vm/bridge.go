package vm

import "reflect"

// ValBridgeTypes is the set of bridge pointer types that carry a Val field
// holding the original concrete value. Populated at init time by stdlib.
var ValBridgeTypes = map[reflect.Type]bool{}

// BridgeForAny wraps ifc the way a value flowing into an interface{} parameter
// is wrapped: a display bridge (String/Error/Format/GoString) when the value's
// type defines one, otherwise the concrete value. Exported so stdlib display
// helpers can wrap composite elements the same way a standalone value is
// wrapped at the native-call boundary.
func (m *Machine) BridgeForAny(ifc Iface) reflect.Value {
	return m.bridgeIface(ifc, AnyRtype)
}

func unbridgeValue(rv reflect.Value) reflect.Value { return UnbridgeValue(rv) }

// asBridge returns the bridge struct's elem (settable) if rv is a known
// bridge wrapper with a non-nil pointer; otherwise an invalid Value.
func asBridge(rv reflect.Value) reflect.Value {
	rv = unwrapIface(rv)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || !ValBridgeTypes[rv.Type()] {
		return reflect.Value{}
	}
	return rv.Elem()
}

// UnbridgeValue extracts the underlying interpreted value from a bridge
// wrapper (e.g. *BridgeError, *BridgeErrorUnwrap). Returns an invalid
// reflect.Value when rv is not a known bridge with a populated Val
// field. Exported for stdlib intercept packages such as errorsx.
func UnbridgeValue(rv reflect.Value) reflect.Value {
	elem := asBridge(rv)
	if !elem.IsValid() {
		return reflect.Value{}
	}
	valField := elem.FieldByName("Val")
	if !valField.IsValid() || valField.IsNil() {
		return reflect.Value{}
	}
	return reflect.ValueOf(valField.Interface())
}

// UnbridgeIface returns the original mvm Iface stored in a bridge
// wrapper (in the Ifc field), if any. Used at the native->mvm return
// boundary to restore the interpreted Iface so subsequent operations
// (reflect.DeepEqual proxy, equality, etc.) see the same value the
// caller originally passed in.
func UnbridgeIface(rv reflect.Value) (Iface, bool) {
	elem := asBridge(rv)
	if !elem.IsValid() {
		return Iface{}, false
	}
	fld := elem.FieldByName("Ifc")
	if !fld.IsValid() {
		return Iface{}, false
	}
	ifc, ok := fld.Interface().(Iface)
	if !ok || ifc.Typ == nil {
		return Iface{}, false
	}
	return ifc, true
}

// Bridges maps interface method names to their bridge pointer types.
// Each bridge type is a struct with a Fn field and a pointer-receiver method
// that delegates to Fn. Populated at init time by stdlib (or any compiled
// package binding). The VM uses these to build wrapper types that make
// interpreted values satisfy Go interfaces at the native call boundary.
var Bridges = map[string]reflect.Type{}

// DisplayBridges is the subset of Bridges that should be used when the
// target type is interface{}/any. These are "display" methods (String,
// Error, GoString, etc.) that change how the value appears in fmt output.
// Behavioral methods (Write, Read, Close) are NOT in this set because
// wrapping a value as e.g. BridgeWrite for an interface{} parameter
// changes its identity without benefit.
var DisplayBridges = map[string]bool{}

// InterfaceBridges maps Go interface types to bridge pointer types that
// implement all methods of the interface. Each bridge struct has fields
// named Fn<MethodName> for each method. Used for multi-method interfaces
// like heap.Interface or sort.Interface.
var InterfaceBridges = map[reflect.Type]reflect.Type{}

// CompositeBridges maps sorted pairs of method names to composite bridge
// pointer types that implement both methods. Used to preserve additional
// interface capabilities when wrapping for a single-method target interface
// (e.g. wrapping a Reader+WriterTo value for an io.Reader parameter keeps
// the WriterTo capability so io.Copy's internal type assertion succeeds).
var CompositeBridges = map[[2]string]reflect.Type{}

// MultiCompositeBridge ties a set of method names to a bridge pointer type
// that implements all of them. Used when 3+ methods must coexist on the
// host-side proxy (e.g. Error+Is+As+Unwrap for multierror's chain so
// stderrors.Is/As reach the interpreted Is/As bodies; a pair-only composite
// would hide Is/As and break the chain walk).
type MultiCompositeBridge struct {
	Methods []string // sorted method names; the bridge must implement all
	Type    reflect.Type
}

// multiCompositeBridges is kept sorted by descending len(Methods) so
// wrapIface picks the richest match first.
var multiCompositeBridges []MultiCompositeBridge

// RegisterMultiCompositeBridge installs mcb, preserving the
// richest-first ordering wrapIface relies on.
func RegisterMultiCompositeBridge(mcb MultiCompositeBridge) {
	multiCompositeBridges = append(multiCompositeBridges, mcb)
	for i := len(multiCompositeBridges) - 1; i > 0; i-- {
		if len(multiCompositeBridges[i].Methods) <= len(multiCompositeBridges[i-1].Methods) {
			break
		}
		multiCompositeBridges[i], multiCompositeBridges[i-1] = multiCompositeBridges[i-1], multiCompositeBridges[i]
	}
}

// IfaceFallbackHook is consulted by bridgeIface when no bridgeable method
// matches the target interface (typically `any`). It lets a higher layer
// (e.g. stdlib) substitute a custom wrapper that preserves mvm-level type
// information lost at the reflect layer (defined types whose underlying
// kind is a basic kind, e.g. `type Frame uintptr`). Returning an invalid
// reflect.Value falls through to the default unwrap.
var IfaceFallbackHook func(m *Machine, ifc Iface, targetType reflect.Type) reflect.Value

// ProxyFactory builds a pointer-to-struct that wraps a mvm Iface and
// re-enters mvm. Used at native-call boundaries to hand a stdlib shadow
// package (e.g. jsonx) a proxy whose methods (MarshalJSON, UnmarshalJSON,
// etc.) dispatch back into the interpreter with full Iface metadata.
type ProxyFactory func(m *Machine, ifc Iface) reflect.Value

// argProxyKey keys an entry in funcArgProxies: the code pointer of a
// plain native function plus the zero-based argument index.
type argProxyKey struct {
	fnPtr uintptr
	arg   int
}

var funcArgProxies = map[argProxyKey]ProxyFactory{}

type methodProxyKey struct {
	recvType reflect.Type
	method   string
	arg      int // method arg count (without receiver)
}

var methodArgProxies = map[methodProxyKey]ProxyFactory{}

type methodProxySet struct {
	recvType reflect.Type
	method   string
}

var methodsWithArgProxies = map[methodProxySet]bool{}

// RegisterArgProxy installs a ProxyFactory for argument arg of the
// native function fn. arg is zero-based. reflect.ValueOf(fn).Pointer()
// is used as the key. For methods, use RegisterArgProxyMethod instead.
func RegisterArgProxy(fn any, arg int, factory ProxyFactory) {
	if fn == nil || factory == nil {
		return
	}
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func {
		return
	}
	funcArgProxies[argProxyKey{rv.Pointer(), arg}] = factory
}

// RegisterArgProxyMethod installs a ProxyFactory for argument arg of
// the named method on recvInstance's type. arg is the zero-based index
// into the explicit (non-receiver) argument list. recvInstance may be
// a typed-nil pointer (e.g. (*Encoder)(nil)); only its type is used.
func RegisterArgProxyMethod(recvInstance any, methodName string, arg int, factory ProxyFactory) {
	if recvInstance == nil || methodName == "" || factory == nil {
		return
	}
	t := reflect.TypeOf(recvInstance)
	methodArgProxies[methodProxyKey{t, methodName, arg}] = factory
	methodsWithArgProxies[methodProxySet{t, methodName}] = true
}

func hasMethodArgProxies(recvType reflect.Type, methodName string) bool {
	return methodsWithArgProxies[methodProxySet{recvType, methodName}]
}

func lookupFuncArgProxy(fnPtr uintptr, arg int) ProxyFactory {
	return funcArgProxies[argProxyKey{fnPtr, arg}]
}

// DeepUnbridge returns rv with stdlib bridge wrappers replaced by the
// concrete interpreted value they hold, recursing through interfaces and
// slices (the shapes error trees take). Used by reflect.DeepEqual's arg
// proxy: an interpreted error stored in a native []error slot becomes a
// fresh bridge instance with non-nil func fields, so two logically-equal
// slices (a []error from native errors.Join vs an interpreted literal)
// never compare deeply equal until the nested bridges are stripped. The
// depth bound guards pathological self-referential slices; maps, structs
// and pointers are left untouched.
func DeepUnbridge(rv reflect.Value) reflect.Value { return deepUnbridgeDepth(rv, 64) }

func deepUnbridgeDepth(rv reflect.Value, depth int) reflect.Value {
	if !rv.IsValid() || depth == 0 {
		return rv
	}
	if u := UnbridgeValue(rv); u.IsValid() {
		return deepUnbridgeDepth(u, depth-1)
	}
	switch rv.Kind() {
	case reflect.Interface:
		if rv.IsNil() {
			return rv
		}
		e := deepUnbridgeDepth(rv.Elem(), depth-1)
		if e.IsValid() && e.Type().AssignableTo(rv.Type()) {
			out := reflect.New(rv.Type()).Elem()
			out.Set(e)
			return out
		}
		return rv
	case reflect.Slice, reflect.Array:
		et := rv.Type().Elem()
		if et.Kind() != reflect.Interface && et.Kind() != reflect.Slice && et.Kind() != reflect.Array {
			return rv
		}
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return rv
		}
		n := rv.Len()
		elems := make([]reflect.Value, n)
		allAssignable := true
		for j := range n {
			e := deepUnbridgeDepth(rv.Index(j), depth-1)
			if !e.IsValid() {
				e = rv.Index(j)
			}
			elems[j] = e
			allAssignable = allAssignable && e.Type().AssignableTo(et)
		}
		// A bridge's underlying value may not satisfy the named element
		// interface (e.g. an interpreted multierror unbridges to a plain
		// []error, which is not itself an error). Widen the element type to
		// `any` so both DeepEqual operands reduce to the same comparable shape.
		var out reflect.Value
		if rv.Kind() == reflect.Slice {
			st := rv.Type()
			if !allAssignable {
				st = reflect.SliceOf(AnyRtype)
			}
			out = reflect.MakeSlice(st, n, n)
		} else {
			at := rv.Type()
			if !allAssignable {
				at = reflect.ArrayOf(n, AnyRtype)
			}
			out = reflect.New(at).Elem()
		}
		for j := range n {
			out.Index(j).Set(elems[j])
		}
		return out
	default:
		return rv
	}
}

func lookupMethodArgProxy(recvType reflect.Type, methodName string, arg int) ProxyFactory {
	return methodArgProxies[methodProxyKey{recvType, methodName, arg}]
}

// NativeMethodHook fully replaces a native method call from interpreted
// code. recv is the receiver as resolved at the call site; args are the
// already-bridged input values. The returned slice is treated like
// reflect.Value.Call's result. Used by stdlib intercepts (e.g.
// testing_virt) that need to inject mvm-side context (source file:line)
// into the native semantics without changing the testing package.
type NativeMethodHook func(m *Machine, recv reflect.Value, args []reflect.Value) []reflect.Value

var nativeMethodHooks = map[methodProxySet]NativeMethodHook{}

// RegisterNativeMethodHook installs hook for the named method on
// recvInstance's type. recvInstance may be a typed-nil pointer (only its
// type is used). The hook fires from the interpreted->native Call path
// instead of reflect.Value.Call(in).
func RegisterNativeMethodHook(recvInstance any, methodName string, hook NativeMethodHook) {
	if recvInstance == nil || methodName == "" || hook == nil {
		return
	}
	t := reflect.TypeOf(recvInstance)
	nativeMethodHooks[methodProxySet{t, methodName}] = hook
}

func hasNativeMethodHook(recvType reflect.Type, methodName string) bool {
	if len(nativeMethodHooks) == 0 {
		return false
	}
	_, ok := nativeMethodHooks[methodProxySet{recvType, methodName}]
	return ok
}

func lookupNativeMethodHook(recvType reflect.Type, methodName string) NativeMethodHook {
	if len(nativeMethodHooks) == 0 {
		return nil
	}
	return nativeMethodHooks[methodProxySet{recvType, methodName}]
}
