// Package interp implements an interpreter.
package interp

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/mvm-sh/mvm/comp"
	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/stdlib/stdmod"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

var (
	debug              = os.Getenv("MVM_DEBUG") != ""
	traceLine, traceOp = ParseTraceModes(os.Getenv("MVM_TRACE"))
)

// ParseTraceModes parses a comma-separated trace-mode value (used by both
// MVM_TRACE and the -x CLI flag). Recognized tokens: "1" or "line" enable
// line tracing; "op" or "bytecode" enable bytecode tracing; "all" enables
// both. Unknown or empty tokens are ignored.
func ParseTraceModes(s string) (line, op bool) {
	for _, t := range strings.Split(s, ",") {
		switch strings.TrimSpace(t) {
		case "1", "line":
			line = true
		case "op", "bytecode":
			op = true
		case "all":
			line, op = true, true
		}
	}
	return
}

// Interp represents the state of an interpreter.
type Interp struct {
	*comp.Compiler
	*vm.Machine
	stdlibPatched bool
}

// NewInterpreter returns a new interpreter.
func NewInterpreter(s *lang.Spec) *Interp {
	i := &Interp{Compiler: comp.NewCompiler(s), Machine: vm.NewMachine()}
	i.SetStdlibFS(stdmod.DefaultFS())
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
	i.Machine.MethodFuncTypes = i.Compiler.MethodFuncTypes()

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
	if traceLine {
		i.SetTracing(true)
	}
	if traceOp {
		i.SetTraceOps(true)
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
// prefix and whose first character after prefix is an ASCII uppercase letter,
// in source-declaration order.
func (i *Interp) FuncNames(prefix string) []string {
	type entry struct {
		name string
		pos  vm.Pos
	}
	var entries []entry
	for name, s := range i.Symbols {
		if s.Kind != symbol.Func || !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if rest == "" || !unicode.IsUpper(rune(rest[0])) {
			continue
		}
		var pos vm.Pos
		if s.Value.IsValid() {
			if addr := int(s.Value.Int()); addr >= 0 && addr < len(i.Code) {
				pos = i.Code[addr].Pos
			}
		}
		entries = append(entries, entry{name, pos})
	}
	sort.Slice(entries, func(a, b int) bool {
		if entries[a].pos != entries[b].pos {
			return entries[a].pos < entries[b].pos
		}
		return entries[a].name < entries[b].name
	})
	names := make([]string, len(entries))
	for j, e := range entries {
		names[j] = e.name
	}
	return names
}
