package main

// `comparable` as an embedded element of a constraint interface
// (`interface { comparable; error }`), used to constrain a generic function.
// The interface body parser used to try resolving `comparable` as an ordinary
// type and fail with `undefined: comparable`; it is now recognized as the
// built-in constraint element. Surfaced by `mvm test errors` on go1.26, whose
// wrap_test.go declares `type compError interface { comparable; error }`.
import "fmt"

type compError interface {
	comparable
	error
}

func firstNonZero[E compError](xs []E) (E, bool) {
	var zero E
	for _, x := range xs {
		if x != zero {
			return x, true
		}
	}
	return zero, false
}

func main() {
	got, ok := firstNonZero([]error{nil, fmt.Errorf("boom")})
	fmt.Println(ok, got)
}

// Output:
// true boom
