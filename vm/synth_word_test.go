//go:build !wasm

// TestDetectWordShape pins the vm seam that gates a register-ABI word key against
// the generated stub pools (detectWordShape resolves to the register classifier
// only when wordabi.WordABI0 is false). The pure classifier/marshaling is covered
// by the wordabi package; end-to-end dispatch by interptest under a wasm runtime.

package vm

import (
	"reflect"
	"testing"
	"time"
)

func TestDetectWordShape(t *testing.T) {
	cases := []struct {
		sig reflect.Type
		key string
		ok  bool
	}{
		{reflect.TypeOf((func() int64)(nil)), "_i", true},
		{reflect.TypeOf((func() any)(nil)), "_pp", true},
		{reflect.TypeOf((func(string) (any, error))(nil)), "pi_pppp", true},
		{reflect.TypeOf((func(string) ([]int, error))(nil)), "pi_piipp", true},
		{reflect.TypeOf((func() time.Time)(nil)), "_iip", true},     // word-sized-leaf struct result
		{reflect.TypeOf((func(float32) float32)(nil)), "g_g", true}, // single-precision unary op
		{reflect.TypeOf((func() complex64)(nil)), "_gg", true},      // complex64 result -> two 'g' halves
		// no generated pool for this word-shape -> drop (not error).
		{reflect.TypeOf((func() (int, int, int))(nil)), "", false},
		// a sub-word-packed struct param flattens to its leaves' words (ii) -> "ii_i".
		{reflect.TypeOf((func(struct{ X, Y int32 }) int32)(nil)), "ii_i", true},
		// over the register-word budget -> drop.
		{reflect.TypeOf((func(*int, *int, *int, *int, *int, *int, *int))(nil)), "", false},
		{nil, "", false},
	}
	for _, tc := range cases {
		key, ok := detectWordShape(tc.sig)
		if key != tc.key || ok != tc.ok {
			t.Errorf("detectWordShape(%v) = %q,%v want %q,%v", tc.sig, key, ok, tc.key, tc.ok)
		}
	}
}
