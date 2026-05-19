package stdlib

import (
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/vm"
)

// init registers NativeMethodHooks for the common testing.TB diagnostic
// methods (Log/Logf/Error/Errorf/Fatal/Fatalf/Skip/Skipf) on *testing.T,
// *testing.B, and *testing.F. Without these hooks, native testing's
// (*common).log resolves the call site via runtime.Callers walked
// against the host Go stack -- which lands on reflect.Value.call
// (reflect/value.go:586) because the interpreted caller invokes the
// native method through reflect.Value.Call in vm.Call. The hook formats
// the message with the interpreter's own source position and writes it
// through t.Output() (which bypasses callSite) before invoking the
// matching Fail/FailNow/SkipNow.
func init() {
	registerTestingHooks((*testing.T)(nil))
	registerTestingHooks((*testing.B)(nil))
	registerTestingHooks((*testing.F)(nil))
}

func registerTestingHooks(recv any) {
	t := reflect.TypeOf(recv)
	mh := tbMethods{
		output:  methodIndex(t, "Output"),
		fail:    methodIndex(t, "Fail"),
		failNow: methodIndex(t, "FailNow"),
		skipNow: methodIndex(t, "SkipNow"),
	}

	log := func(_ *vm.Machine, _ reflect.Value, args []reflect.Value) string {
		return fmt.Sprintln(ifaces(args)...)
	}
	logf := func(_ *vm.Machine, _ reflect.Value, args []reflect.Value) string {
		if len(args) == 0 {
			return ""
		}
		format, _ := args[0].Interface().(string)
		return fmt.Sprintf(format, ifaces(args[1:])...)
	}

	vm.RegisterNativeMethodHook(recv, "Log", mh.hook(log, -1))
	vm.RegisterNativeMethodHook(recv, "Logf", mh.hook(logf, -1))
	vm.RegisterNativeMethodHook(recv, "Error", mh.hook(log, mh.fail))
	vm.RegisterNativeMethodHook(recv, "Errorf", mh.hook(logf, mh.fail))
	vm.RegisterNativeMethodHook(recv, "Fatal", mh.hook(log, mh.failNow))
	vm.RegisterNativeMethodHook(recv, "Fatalf", mh.hook(logf, mh.failNow))
	vm.RegisterNativeMethodHook(recv, "Skip", mh.hook(log, mh.skipNow))
	vm.RegisterNativeMethodHook(recv, "Skipf", mh.hook(logf, mh.skipNow))
}

// tbMethods caches reflect method indices for one testing receiver type
// (*T, *B, or *F). Storing the indices at init time lets the per-call
// hook use recv.Method(idx) instead of recv.MethodByName(name), avoiding
// a method-set string walk per t.Errorf invocation.
type tbMethods struct {
	output, fail, failNow, skipNow int
}

type msgFormatter func(*vm.Machine, reflect.Value, []reflect.Value) string

func (mh tbMethods) hook(format msgFormatter, afterIdx int) vm.NativeMethodHook {
	return func(m *vm.Machine, recv reflect.Value, args []reflect.Value) []reflect.Value {
		mh.writeLine(m, recv, format(m, recv, args))
		if afterIdx >= 0 {
			recv.Method(afterIdx).Call(nil)
		}
		return nil
	}
}

// writeLine writes msg through recv.Output(), which is the same sink as
// (*common).log but without the callSite prefix. The prefix is computed
// from the interpreter's own call site so it points at the *.go line
// where the user actually called t.Errorf etc.
func (mh tbMethods) writeLine(m *vm.Machine, recv reflect.Value, msg string) {
	if mh.output < 0 {
		return
	}
	w, ok := recv.Method(mh.output).Call(nil)[0].Interface().(io.Writer)
	if !ok {
		return
	}
	// Mirror (*common).log: trim a trailing newline, then re-add one.
	if n := len(msg); n > 0 && msg[n-1] == '\n' {
		msg = msg[:n-1]
	}
	_, _ = io.WriteString(w, mvmCallSite(m)+msg+"\n")
}

func methodIndex(t reflect.Type, name string) int {
	m, ok := t.MethodByName(name)
	if !ok {
		return -1
	}
	return m.Index
}

func ifaces(args []reflect.Value) []any {
	out := make([]any, len(args))
	for i, a := range args {
		if !a.IsValid() {
			continue
		}
		out[i] = a.Interface()
	}
	return out
}

func mvmCallSite(m *vm.Machine) string {
	const fallback = "???:1: "
	di := m.DebugInfo()
	if di == nil {
		return fallback
	}
	file, line, _ := di.Sources.Resolve(int(m.CallSitePos()))
	if file == "" {
		return fallback
	}
	return fmt.Sprintf("%s:%d: ", filepath.Base(file), line)
}
