package stdlib

import "github.com/mvm-sh/mvm/goparser"

// syncGenericShim defines the generic sync helpers, which can't be expressed
// as a single reflect.ValueOf binding, for interpreted callers (native bridged
// packages use compiled sync). Equivalents of upstream oncefunc.go built on
// the bridged sync.Once, minus the single-alloc struct. On a panicking f,
// upstream re-panics inside the once.Do callback for a trace into f; here the
// callback captures the value and the outer closure raises it -- same panic
// value and panics-every-call contract, only the first-call stack origin differs.
const syncGenericShim = `package sync

func OnceValue[T any](f func() T) func() T {
	var (
		once   Once
		valid  bool
		p      any
		result T
	)
	g := func() {
		defer func() {
			if !valid {
				p = recover()
			}
		}()
		result = f()
		valid = true
	}
	return func() T {
		once.Do(g)
		if !valid {
			panic(p)
		}
		return result
	}
}

func OnceValues[T1, T2 any](f func() (T1, T2)) func() (T1, T2) {
	var (
		once  Once
		valid bool
		p     any
		r1    T1
		r2    T2
	)
	g := func() {
		defer func() {
			if !valid {
				p = recover()
			}
		}()
		r1, r2 = f()
		valid = true
	}
	return func() (T1, T2) {
		once.Do(g)
		if !valid {
			panic(p)
		}
		return r1, r2
	}
}
`

func init() {
	goparser.RegisterGenericShim("sync", syncGenericShim, []string{"Once"})
}
