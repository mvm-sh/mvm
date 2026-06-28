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
func init() {
	vm.RegisterSynthIfaceTargetFunc(reflect.ValueOf(errors.As))
	vm.RegisterSynthIfaceTargetFunc(reflect.ValueOf(reflect.TypeOf))
	vm.RegisterSynthIfaceWriteTargetFunc(reflect.ValueOf(errors.As))
}
