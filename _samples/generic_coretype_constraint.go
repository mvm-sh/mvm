package main

// Core-type constraint elements (a type term like *P, []E, map[K]V inside an
// interface constraint) drive constraint type inference: the param in the term
// is inferred from the argument's structure. Mirrors protobuf's
// proto.ValueOrDefault[T interface{ *P; Message }, P any].

type Stringer interface{ String() string }

// *P core type plus an embedded method-set interface, the ValueOrDefault shape.
func valueOrDefault[T interface {
	*P
	Stringer
}, P any](val T) T {
	if val == nil {
		return T(new(P))
	}
	return val
}

type box struct{ n int }

func (b *box) String() string { return "box" }

// []E core type: E is inferred from the slice element.
func firstOr[S interface{ ~[]E }, E any](s S, d E) E {
	if len(s) == 0 {
		return d
	}
	return s[0]
}

func main() {
	var nilBox *box
	println(valueOrDefault(nilBox).String())  // inference, nil -> new(box)
	println(valueOrDefault(&box{7}).String()) // inference, non-nil
	// Explicit instantiation of the same template.
	println(valueOrDefault[*box, box](&box{1}).String())
	println(firstOr([]int{4, 5}, -1)) // inference: S=[]int, E=int
	println(firstOr([]int{}, -1))     // empty -> default
}

// Output:
// box
// box
// box
// 4
// -1
