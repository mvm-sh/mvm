package stdlib

import (
	"errors"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// Allowlist the callees that may retype a pointer-to-interpreted-interface
// argument to a method-bearing synth interface rtype (see vm.bridgePtrToIface
// for why this is gated, not global).
// errors.As matches by the target's method set; reflect.ValueOf/TypeOf read the
// result back through the same pointer and must observe the same retype.
func init() {
	vm.RegisterSynthIfaceTargetFunc(reflect.ValueOf(errors.As))
	vm.RegisterSynthIfaceTargetFunc(reflect.ValueOf(reflect.ValueOf))
	vm.RegisterSynthIfaceTargetFunc(reflect.ValueOf(reflect.TypeOf))
	// errors.As writes the match through the retyped pointer, so its pointee
	// needs normalizing back to mvm form afterward.
	vm.RegisterSynthIfaceWriteTargetFunc(reflect.ValueOf(errors.As))
}
