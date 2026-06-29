package vm

import (
	"reflect"

	"github.com/mvm-sh/mvm/derive"
	"github.com/mvm-sh/mvm/runtype"
)

// Interpreted code reaching native reflect (Value.MethodByName/Call, Type.MethodByName,
// SliceOf/MapOf/...) on a synth type can't dispatch through the host: these shims
// reroute it to VM method resolution. nativeMethodLookup invokes them.

var reflectValueRtype = reflect.TypeFor[reflect.Value]()

// Shim MakeFunc signatures.
var (
	methodByNameShimType     = reflect.TypeOf(func(string) reflect.Value { return reflect.Value{} })
	callShimType             = reflect.TypeOf(func([]reflect.Value) []reflect.Value { return nil })
	typeMethodByNameShimType = reflect.TypeOf(func(string) (reflect.Method, bool) { return reflect.Method{}, false })
	setIterValueShimType     = reflect.TypeOf(func(*reflect.MapIter) {})
)

var reflectMethodRtype = reflect.TypeFor[reflect.Method]()

var notFoundMethodResult = []reflect.Value{reflect.Zero(reflectMethodRtype), reflect.ValueOf(false)}

var zeroReflectValueResult = []reflect.Value{reflect.Zero(reflectValueRtype)}

func reflectValueShim(m *Machine, rv reflect.Value, name string) reflect.Value {
	if m == nil || !rv.IsValid() || rv.Type() != reflectValueRtype {
		return reflect.Value{}
	}
	innerRV, ok := rv.Interface().(reflect.Value)
	if !ok || !innerRV.IsValid() {
		return reflect.Value{}
	}
	synthRecv := innerRV.Type() != ifaceRtype && runtype.IsSynth(innerRV.Type())
	if synthRecv && name == "SetIterValue" && innerRV.Kind() == reflect.Interface && innerRV.CanAddr() {
		dst := innerRV
		return reflect.MakeFunc(setIterValueShimType,
			func(args []reflect.Value) []reflect.Value {
				if it, _ := args[0].Interface().(*reflect.MapIter); it != nil {
					m.storeIfaceFromReflect(dst, it.Value())
				}
				return nil
			})
	}
	if synthRecv && name != "MethodByName" {
		return reflect.Value{}
	}
	switch name {
	case "MethodByName":
		// Build the Iface that MakeMethodCallable expects.
		var ifc Iface
		if innerRV.Type() == ifaceRtype {
			ifc = innerRV.Interface().(Iface)
			if ifc.Typ == nil {
				return reflect.Value{}
			}
		} else {
			t := m.typeByRtype(innerRV.Type())
			if t == nil {
				return reflect.Value{}
			}
			ifc = Iface{Typ: t, Val: FromReflect(innerRV)}
		}
		return reflect.MakeFunc(methodByNameShimType,
			func(args []reflect.Value) []reflect.Value {
				methodName := args[0].String()
				// Supported-shape methods are in the native table; prefer them.
				// On the shared-PC (wasm) build the native table entry is a trap
				// stub, so always route through VM dispatch instead.
				if synthRecv && !synthSharedPC {
					if nm := innerRV.MethodByName(methodName); nm.IsValid() {
						return []reflect.Value{reflect.ValueOf(nm)}
					}
				}
				method, found := m.MethodByName(ifc.Typ, methodName)
				if !found {
					return zeroReflectValueResult
				}
				// Prefer the method's own bound signature. The global
				// interface-method table only registers methods reached through
				// an interface, so a concrete method invoked solely via reflect
				// (text/template's MethodByName.Call) is absent there.
				ft := method.Rtype
				if ft == nil {
					ft = m.ifaceMethodFuncType(methodName)
				}
				if ft == nil {
					return zeroReflectValueResult
				}
				closure := m.MakeMethodCallable(ifc, method)
				return []reflect.Value{reflect.ValueOf(m.makeCallFunc(closure, ft))}
			})
	case "Call", "CallSlice":
		if innerRV.Kind() != reflect.Func {
			return reflect.Value{}
		}
		spread := name == "CallSlice"
		return reflect.MakeFunc(callShimType,
			func(args []reflect.Value) []reflect.Value {
				var in []reflect.Value
				if len(args) > 0 && args[0].IsValid() && !args[0].IsNil() {
					in, _ = args[0].Interface().([]reflect.Value)
				}
				if out, ok := callSynthMethodFunc(innerRV, in, spread); ok {
					return []reflect.Value{reflect.ValueOf(out)}
				}
				return []reflect.Value{reflect.ValueOf(callWithSpread(innerRV, in, spread))}
			})
	}
	return reflect.Value{}
}

func reflectTypeShim(m *Machine, rv reflect.Value, name string) reflect.Value {
	if m == nil || name != "MethodByName" || !rv.IsValid() || !rv.CanInterface() {
		return reflect.Value{}
	}
	rt, ok := rv.Interface().(reflect.Type)
	if !ok || !isSynthOrSynthPtr(rt) {
		return reflect.Value{}
	}
	return reflect.MakeFunc(typeMethodByNameShimType,
		func(args []reflect.Value) []reflect.Value {
			methodName := args[0].String()
			// Supported-shape methods live in the native uncommon table; prefer them.
			// On the shared-PC (wasm) build that entry is a trap stub, so route
			// through VM dispatch (synthMethodExpr) instead.
			if !synthSharedPC {
				if mm, found := rt.MethodByName(methodName); found {
					return []reflect.Value{reflect.ValueOf(mm), reflect.ValueOf(true)}
				}
			}
			mm, found := m.synthMethodExpr(rt, methodName)
			if !found {
				return notFoundMethodResult
			}
			return []reflect.Value{reflect.ValueOf(mm), reflect.ValueOf(true)}
		})
}

type reflectCtorKind int

const (
	ctorSlice reflectCtorKind = iota + 1
	ctorMap
	ctorArray
	ctorChan
)

// reflectCtorPCs maps each constructor's code pointer to its kind.
var reflectCtorPCs = map[uintptr]reflectCtorKind{
	reflect.ValueOf(reflect.SliceOf).Pointer(): ctorSlice,
	reflect.ValueOf(reflect.MapOf).Pointer():   ctorMap,
	reflect.ValueOf(reflect.ArrayOf).Pointer(): ctorArray,
	reflect.ValueOf(reflect.ChanOf).Pointer():  ctorChan,
}

func interceptReflectCtor(rv reflect.Value, in []reflect.Value) (out []reflect.Value, ok bool) {
	if rv.Kind() != reflect.Func {
		return nil, false
	}
	kind, isCtor := reflectCtorPCs[rv.Pointer()]
	if !isCtor {
		return nil, false
	}
	argType := func(i int) reflect.Type {
		if i >= len(in) || !in[i].IsValid() || !in[i].CanInterface() {
			return nil
		}
		t, _ := in[i].Interface().(reflect.Type)
		return t
	}
	var result reflect.Type
	switch kind {
	case ctorSlice:
		elem := argType(0)
		if elem == nil || !runtype.IsSynth(elem) {
			return nil, false
		}
		result = runtype.DeriveSliceOf(elem)
	case ctorMap:
		key, elem := argType(0), argType(1)
		if key == nil || elem == nil || (!runtype.IsSynth(key) && !runtype.IsSynth(elem)) {
			return nil, false
		}
		result = runtype.DeriveMapOf(key, elem)
	case ctorArray: // ArrayOf(length int, elem Type)
		elem := argType(1)
		if elem == nil || !runtype.IsSynth(elem) {
			return nil, false
		}
		result = runtype.DeriveArrayOf(int(in[0].Int()), elem)
	case ctorChan: // ChanOf(dir ChanDir, elem Type)
		elem := argType(1)
		if elem == nil || !runtype.IsSynth(elem) {
			return nil, false
		}
		result = runtype.DeriveChanOf(reflect.ChanDir(in[0].Int()), elem)
	}
	if result == nil {
		return nil, false
	}
	box := reflect.New(rv.Type().Out(0)).Elem() // reflect.Type (interface) return
	box.Set(reflect.ValueOf(result))
	return []reflect.Value{box}, true
}

func (m *Machine) synthMethodExpr(rt reflect.Type, name string) (reflect.Method, bool) {
	t := m.typeByRtype(rt)
	if t == nil {
		return reflect.Method{}, false
	}
	method, ok := m.MethodByName(t, name)
	if !ok {
		return reflect.Method{}, false
	}
	if rt.Kind() != reflect.Pointer && method.PtrRecv {
		return reflect.Method{}, false
	}
	boundFt := method.Rtype
	if (boundFt == nil || boundFt.Kind() != reflect.Func) && method.Sig != nil {
		boundFt = derive.MaterializeRtype(method.Sig)
	}
	if boundFt == nil || boundFt.Kind() != reflect.Func {
		boundFt = m.ifaceMethodFuncType(name)
	}
	if boundFt == nil || boundFt.Kind() != reflect.Func {
		return reflect.Method{}, false
	}
	// Method-expression signature: the receiver prepended to the bound signature.
	in := make([]reflect.Type, 0, boundFt.NumIn()+1)
	in = append(in, rt)
	for pt := range boundFt.Ins() {
		in = append(in, pt)
	}
	out := make([]reflect.Type, boundFt.NumOut())
	for i := range out {
		out[i] = boundFt.Out(i)
	}
	exprType := reflect.FuncOf(in, out, boundFt.IsVariadic())
	fn := reflect.MakeFunc(exprType, func(args []reflect.Value) []reflect.Value {
		ifc := Iface{Typ: t, Val: FromReflect(args[0])}
		closure := m.MakeMethodCallable(ifc, method)
		return m.makeCallFunc(closure, boundFt).Call(args[1:])
	})
	// Index 0 is a placeholder: a beyond-cap method has no native table slot.
	return reflect.Method{Name: name, Type: exprType, Func: fn, Index: 0}, true
}

func callSynthMethodFunc(fn reflect.Value, in []reflect.Value, spread bool) ([]reflect.Value, bool) {
	ft := fn.Type()
	if ft.NumIn() == 0 || len(in) == 0 {
		return nil, false
	}
	recvT := ft.In(0)
	if derive.IsDirectIface(recvT) || !runtype.IsSynth(recvT) ||
		!in[0].IsValid() || !in[0].Type().AssignableTo(recvT) {
		return nil, false
	}
	pc := fn.Pointer()
	var name string
	for mm := range recvT.Methods() {
		if mm.Type == ft && mm.Func.Pointer() == pc {
			name = mm.Name
			break
		}
	}
	if name == "" {
		return nil, false
	}
	bound := in[0].MethodByName(name)
	if !bound.IsValid() {
		return nil, false
	}
	return callWithSpread(bound, in[1:], spread), true
}

func callWithSpread(fn reflect.Value, args []reflect.Value, spread bool) []reflect.Value {
	if spread {
		return fn.CallSlice(args)
	}
	return fn.Call(args)
}
