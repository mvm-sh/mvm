package vm

import "reflect"

// ValBridgeTypes is the set of bridge pointer types that carry a Val field
// holding the original concrete value. Populated at init time by stdlib.
var ValBridgeTypes = map[reflect.Type]bool{}

// unbridgeValue checks whether rv is a known bridge wrapper and returns the
// original concrete value stored in its Val field. This is used during type
// assertions to look through bridge wrappers created at the native boundary.
func unbridgeValue(rv reflect.Value) reflect.Value {
	if rv.Kind() != reflect.Pointer || rv.IsNil() || !ValBridgeTypes[rv.Type()] {
		return reflect.Value{}
	}
	valField := rv.Elem().FieldByName("Val")
	if !valField.IsValid() || valField.IsNil() {
		return reflect.Value{}
	}
	return reflect.ValueOf(valField.Interface())
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

// funcArgProxies registers ProxyFactory callbacks for plain native
// functions, keyed by (reflect.Value.Pointer(), arg index). Populated
// via RegisterArgProxy.
var funcArgProxies = map[argProxyKey]ProxyFactory{}

// methodProxyKey keys an entry in methodArgProxies: the receiver
// reflect.Type, the method name, and the zero-based argument index
// (receiver not counted). Bound method Pointer()s share a single
// reflect trampoline across all methods and types, so methods must
// be keyed by (type, name) rather than by pointer.
type methodProxyKey struct {
	recvType reflect.Type
	method   string
	arg      int
}

// methodArgProxies registers ProxyFactory callbacks for native methods,
// keyed by (receiver type, method name, arg index). Populated via
// RegisterArgProxyMethod.
var methodArgProxies = map[methodProxyKey]ProxyFactory{}

// methodsWithArgProxies is the set of (receiver type, method name)
// pairs that have at least one entry in methodArgProxies. Used as a
// cheap check at IfaceCall time to decide whether to wrap the bound
// method in a boundProxyCall sentinel.
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

// hasMethodArgProxies reports whether (recvType, methodName) has any
// registered arg proxy. Cheap test used at IfaceCall to decide whether
// to wrap the bound method in a boundProxyCall sentinel.
func hasMethodArgProxies(recvType reflect.Type, methodName string) bool {
	return methodsWithArgProxies[methodProxySet{recvType, methodName}]
}

// lookupFuncArgProxy returns the factory registered for plain function
// fnPtr at arg, or nil.
func lookupFuncArgProxy(fnPtr uintptr, arg int) ProxyFactory {
	return funcArgProxies[argProxyKey{fnPtr, arg}]
}

// lookupMethodArgProxy returns the factory registered for method
// (recvType, methodName) at arg, or nil.
func lookupMethodArgProxy(recvType reflect.Type, methodName string, arg int) ProxyFactory {
	return methodArgProxies[methodProxyKey{recvType, methodName, arg}]
}
