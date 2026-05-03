// Package interp implements an interpreter.
package interp

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/comp"
	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

var debug = os.Getenv("MVM_DEBUG") != ""

// Interp represents the state of an interpreter.
type Interp struct {
	*comp.Compiler
	*vm.Machine
	stdlibPatched bool
}

// NewInterpreter returns a new interpreter.
func NewInterpreter(s *lang.Spec) *Interp {
	i := &Interp{Compiler: comp.NewCompiler(s), Machine: vm.NewMachine()}
	i.SetStdlibFS(stdlib.SrcFS())
	return i
}

// Eval evaluates code string and return the last produced value if any, or an error.
// name labels the source for error positions (a file path or any identifier).
func (i *Interp) Eval(name, src string) (res reflect.Value, err error) {
	codeOffset := len(i.Code)
	dataOffset := 0
	if codeOffset > 0 {
		// All data must be copied to the VM the first time only (re-entrance).
		dataOffset = len(i.Data)
	}
	i.PopExit() // Remove last exit from previous run (re-entrance).
	initsBefore := len(i.InitFuncs)

	if !i.stdlibPatched {
		i.patchStdlibOverrides()
		i.stdlibPatched = true
	}

	if err = i.Compile(name, src); err != nil {
		return res, err
	}

	i.Machine.MethodNames = i.Compiler.MethodNames()

	i.TrimStack()
	i.Push(i.Data[dataOffset:]...)
	i.PushCode(i.Code[codeOffset:]...)
	emitCall := func(fn string) {
		if s, ok := i.Symbols[fn]; ok {
			i.PushCode(vm.Instruction{Op: vm.Push, A: int32(i.Data[s.Index].Int())}) //nolint:gosec
			i.PushCode(vm.Instruction{Op: vm.Call})
		}
	}
	for _, fn := range i.InitFuncs[initsBefore:] {
		emitCall(fn)
	}
	emitCall("main")
	i.PushCode(vm.Instruction{Op: vm.Exit})
	i.SetIP(max(codeOffset, i.Entry))
	i.SetDebugInfo(func() *vm.DebugInfo { return i.BuildDebugInfo() })
	if debug {
		i.PrintData()
		i.PrintCode()
	}
	err = i.Run()
	return i.Top().Reflect(), err
}

func (i *Interp) patchStdlibOverrides() {
	i.patchFmtBindings()
	for importPath, fns := range stdlib.PackagePatchers() {
		pkg, ok := i.Packages[importPath]
		if !ok {
			continue
		}
		for _, fn := range fns {
			fn(i.Machine, pkg.Values)
		}
	}
}

func (i *Interp) patchFmtBindings() {
	pkg, ok := i.Packages["fmt"]
	if !ok {
		return
	}
	m := i.Machine
	pkg.Values["Print"] = vm.FromReflect(reflect.ValueOf(func(a ...any) (int, error) {
		return fmt.Fprint(m.Out(), a...)
	}))
	pkg.Values["Printf"] = vm.FromReflect(reflect.ValueOf(func(format string, a ...any) (int, error) {
		return fmt.Fprintf(m.Out(), format, a...)
	}))
	pkg.Values["Println"] = vm.FromReflect(reflect.ValueOf(func(a ...any) (int, error) {
		return fmt.Fprintln(m.Out(), a...)
	}))

	// Also export the Stringer type so interpreted code can reference it.
	if _, ok := pkg.Values["Stringer"]; !ok {
		pkg.Values["Stringer"] = vm.FromReflect(reflect.ValueOf((*fmt.Stringer)(nil)))
	}
}

// FuncNames returns names of top-level functions whose name starts with
// prefix and whose first character after prefix is an ASCII uppercase letter.
// It is intended for callers that need to enumerate Test*, Benchmark* or
// Example* functions after evaluating sources. The order is unspecified.
func (i *Interp) FuncNames(prefix string) []string {
	var names []string
	for name, s := range i.Symbols {
		if s.Kind != symbol.Func || !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if rest == "" || rest[0] < 'A' || rest[0] > 'Z' {
			continue
		}
		names = append(names, name)
	}
	return names
}
