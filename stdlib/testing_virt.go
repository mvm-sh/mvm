package stdlib

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mvm-sh/mvm/internal/runtype"
	"github.com/mvm-sh/mvm/vm"
)

func init() {
	registerTestingHooks((*testing.T)(nil))
	registerTestingHooks((*testing.B)(nil))
	registerTestingHooks((*testing.F)(nil))
	registerRunHook((*testing.T)(nil))
	registerRunHook((*testing.B)(nil))
}

// debugTB traces testing.TB receivers (stale-receiver hunting):
// MVM_DEBUG_TB=1 logs Fail/Fatal receivers, =2 also every Run registration.
var debugTB = os.Getenv("MVM_DEBUG_TB")

func tbName(recv reflect.Value) string {
	defer func() { _ = recover() }()
	if m := runtype.ValueMethodByName(recv, "Name"); m.IsValid() {
		return m.Call(nil)[0].String()
	}
	return "?"
}

func registerRunHook(recv any) {
	t := reflect.TypeOf(recv)
	runIdx := methodIndex(t, "Run")
	if runIdx < 0 {
		return
	}
	vm.RegisterNativeMethodHook(recv, "Run", func(_ *vm.Machine, recvVal reflect.Value, args []reflect.Value) []reflect.Value {
		if debugTB == "2" && len(args) > 0 {
			fmt.Fprintf(os.Stderr, "[mvmdbg] Run %q on %q\n", args[0].String(), tbName(recvVal))
		}
		if len(args) == 2 && args[1].IsValid() && args[1].Kind() == reflect.Func {
			f := args[1]
			args = []reflect.Value{args[0], reflect.MakeFunc(f.Type(), func(in []reflect.Value) []reflect.Value {
				defer printPanicDiag()
				if debugTB == "2" && len(in) > 0 {
					fmt.Fprintf(os.Stderr, "[mvmdbg] body start %q\n", tbName(in[0]))
				}
				return f.Call(in)
			})}
		}
		return recvVal.Method(runIdx).Call(args)
	})
}

func printPanicDiag() {
	if r := recover(); r != nil {
		if diag, ok := vm.FormatPanic(r); ok {
			fmt.Fprintln(os.Stderr, diag)
		}
		panic(r)
	}
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

type tbMethods struct {
	output, fail, failNow, skipNow int
}

type msgFormatter func(*vm.Machine, reflect.Value, []reflect.Value) string

func (mh tbMethods) hook(format msgFormatter, afterIdx int) vm.NativeMethodHook {
	return func(m *vm.Machine, recv reflect.Value, args []reflect.Value) []reflect.Value {
		if debugTB != "" && afterIdx >= 0 {
			fmt.Fprintf(os.Stderr, "[mvmdbg] fail(%d) on %q\n", afterIdx, tbName(recv))
		}
		mh.writeLine(m, recv, format(m, recv, args))
		if afterIdx >= 0 {
			recv.Method(afterIdx).Call(nil)
		}
		return nil
	}
}

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
	m, ok := runtype.TypeMethodByName(t, name)
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
