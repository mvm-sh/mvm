package stdlib

import "github.com/mvm-sh/mvm/goparser"

// reflectGenericShim provides interpreted definitions for reflect symbols
// that cannot be expressed as a single reflect.ValueOf binding (they are
// generic). These are reached only by INTERPRETED callers; native bridged
// packages use compiled reflect.
//
//   - TypeFor[T any]() Type (Go 1.22): exactly the upstream fallback branch.
//   - TypeAssert[T any](v Value) (T, bool) (Go 1.26): delegates to the mvm
//     `.(T)` opcode via v.Interface().(T). That opcode recovers interpreted
//     types (typeByRtype + vm.Type.Implements), so an interpreted concrete
//     type asserts to an interpreted/native interface here -- unlike upstream's
//     unsafe path, which checks only the (methodless) synthetic rtype. This is
//     a correct, simpler equivalent of upstream across all concrete/interface
//     cases; it differs only in panic behavior on invalid/read-only Values.
const reflectGenericShim = `package reflect

func TypeFor[T any]() Type {
	return TypeOf((*T)(nil)).Elem()
}

func TypeAssert[T any](v Value) (T, bool) {
	r, ok := v.Interface().(T)
	return r, ok
}
`

func init() {
	goparser.RegisterGenericShim("reflect", reflectGenericShim, []string{"Type", "TypeOf", "Value"})
}
