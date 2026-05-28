package stdlib

import "github.com/mvm-sh/mvm/goparser"

// errorsGenericShim provides an interpreted definition of errors.AsType, the
// go1.26 generic that cannot be expressed as a reflect.ValueOf binding (the
// rest of errors is bound natively in stdlib/core/errors.go). It is reached
// only by interpreted callers; native bridged packages use compiled errors.
//
// The body is the verbatim upstream source: it uses only type assertions and
// builtins, so it references no errors symbol (nativeRefs is nil). Mirrors
// stdlib/reflect_shim.go.
const errorsGenericShim = `package errors

func AsType[E error](err error) (E, bool) {
	if err == nil {
		var zero E
		return zero, false
	}
	var pe *E // lazily initialized
	return asType(err, &pe)
}

func asType[E error](err error, ppe **E) (_ E, _ bool) {
	for {
		if e, ok := err.(E); ok {
			return e, true
		}
		if x, ok := err.(interface{ As(any) bool }); ok {
			if *ppe == nil {
				*ppe = new(E)
			}
			if x.As(*ppe) {
				return **ppe, true
			}
		}
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
			if err == nil {
				return
			}
		case interface{ Unwrap() []error }:
			for _, err := range x.Unwrap() {
				if err == nil {
					continue
				}
				if x, ok := asType(err, ppe); ok {
					return x, true
				}
			}
			return
		default:
			return
		}
	}
}
`

func init() {
	goparser.RegisterGenericShim("errors", errorsGenericShim, nil)
}
