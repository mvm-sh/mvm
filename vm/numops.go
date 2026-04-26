package vm

import "math"

type integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

type float interface{ ~float32 | ~float64 }

func add[T integer](a, b uint64) uint64 { return uint64(T(a) + T(b)) } //nolint:gosec
func sub[T integer](a, b uint64) uint64 { return uint64(T(a) - T(b)) } //nolint:gosec
func mul[T integer](a, b uint64) uint64 { return uint64(T(a) * T(b)) } //nolint:gosec
func div[T integer](a, b uint64) uint64 { return uint64(T(a) / T(b)) } //nolint:gosec
func rem[T integer](a, b uint64) uint64 { return uint64(T(a) % T(b)) } //nolint:gosec
func neg[T integer](a uint64) uint64    { return uint64(-T(a)) }       //nolint:gosec

func addf[T float](a, b uint64) uint64 {
	return math.Float64bits(float64(T(math.Float64frombits(a)) + T(math.Float64frombits(b))))
}

func subf[T float](a, b uint64) uint64 {
	return math.Float64bits(float64(T(math.Float64frombits(a)) - T(math.Float64frombits(b))))
}

func mulf[T float](a, b uint64) uint64 {
	return math.Float64bits(float64(T(math.Float64frombits(a)) * T(math.Float64frombits(b))))
}

func divf[T float](a, b uint64) uint64 {
	return math.Float64bits(float64(T(math.Float64frombits(a)) / T(math.Float64frombits(b))))
}

func negf[T float](a uint64) uint64 {
	return math.Float64bits(float64(-T(math.Float64frombits(a))))
}

// getf32 extracts a float32 from a Value's uint64 storage (float64-bits encoding).
func getf32(n uint64) float32 { return float32(math.Float64frombits(n)) }

// putf32 stores a float32 into a Value's uint64 storage (float64-bits encoding).
func putf32(f float32) uint64 { return math.Float64bits(float64(f)) }
