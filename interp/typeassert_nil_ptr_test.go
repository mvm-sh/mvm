package interp_test

import "testing"

// A native typed-nil pointer (reflect.Zero(mt).Interface()) asserted to an
// interpreted interface collapsed to a nil interface, so m == nil was true and a
// valid nil-receiver method call panicked. Was protobuf proto.Marshal via
// getMessageInfo.
func TestTypeAssertNativeNilPtrToInterface(t *testing.T) {
	const decl = `
		import "reflect"
		type I interface{ M() string }
		type T struct{ X int }
		func (t *T) M() string { if t == nil { return "nil-recv" }; return "non-nil" }
		func nilOf() I {
			m, ok := reflect.Zero(reflect.TypeOf((*T)(nil))).Interface().(I)
			if !ok { panic("assert failed") }
			return m
		}
	`
	run(t, []etest{
		{n: "nil_receiver_method", src: decl + `nilOf().M()`, res: "nil-recv"},
		{n: "not_equal_nil", src: decl + `nilOf() == nil`, res: "false"},
	})
}
