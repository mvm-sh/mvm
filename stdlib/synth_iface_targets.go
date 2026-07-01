package stdlib

import (
	"errors"
	"reflect"

	"github.com/mvm-sh/mvm/vm"
)

// Allowlist callees that may retype a pointer-to-interpreted-interface argument
// to a method-bearing synth interface rtype. reflect.TypeOf reads only the type
// (safe; lets reflect.Implements see an interpreted interface's methods).
// reflect.ValueOf must stay off: it reads the slot value, and the retype would
// misread errors.As's normalized writeback (-> TestAs nil-deref).
//
// reflectliteValueOf (not reflect.ValueOf) is allowlisted so interpreted errors.As
// gets a precise anon-iface targetType (no AssignableTo over-match).
// InstallReflectSetSynthIfaceHook then keeps its Set writeback in eface form, since a
// synth-iface Set writes itab form the interpreter's iface slots cannot decode.
func init() {
	vm.RegisterSynthIfaceTargetFunc(reflect.ValueOf(errors.As))
	vm.RegisterSynthIfaceTargetFunc(reflect.ValueOf(reflect.TypeOf))
	vm.RegisterSynthIfaceTargetFunc(reflect.ValueOf(reflectliteValueOf))
	vm.RegisterSynthIfaceWriteTargetFunc(reflect.ValueOf(errors.As))
	vm.InstallReflectSetSynthIfaceHook()
}
