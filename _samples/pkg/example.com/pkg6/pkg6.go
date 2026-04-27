package pkg6

func Max[T comparable](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func Id[T any](x T) T { return x }

type Box[T any] struct{ Value T }
