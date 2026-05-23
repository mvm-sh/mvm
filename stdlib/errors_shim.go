package stdlib

import "github.com/mvm-sh/mvm/goparser"

// errorsGenericShim provides an interpreted definition of errors.AsType, the
// generic finder added in Go 1.26. It cannot be exposed as a reflect.ValueOf
// binding (Go generics are not introspectable via reflect), so the rest of the
// errors package stays a native bridge (stdlib/core/errors.go, patched by
// stdlib/errorsx) while AsType is supplied here as source and instantiated
// through the normal generic pipeline.
//
// This is a self-recursive simplification of the upstream AsType+asType pair:
// it avoids the lazy **E accumulator and the unexported helper, recursing on
// AsType[E] directly. It is behaviorally equivalent for matching purposes (a
// fresh As target is allocated per call either way).
const errorsGenericShim = `package errors

func AsType[E error](err error) (E, bool) {
	for err != nil {
		if e, ok := err.(E); ok {
			return e, true
		}
		if x, ok := err.(interface{ As(any) bool }); ok {
			var pe E
			if x.As(&pe) {
				return pe, true
			}
		}
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		case interface{ Unwrap() []error }:
			for _, e := range x.Unwrap() {
				if t, ok := AsType[E](e); ok {
					return t, true
				}
			}
			var zero E
			return zero, false
		default:
			var zero E
			return zero, false
		}
	}
	var zero E
	return zero, false
}
`

func init() {
	goparser.RegisterGenericShim("errors", errorsGenericShim, nil)
}
