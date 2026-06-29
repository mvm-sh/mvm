// Package interp implements an interpreter.
package interp

import (
	"errors"
	"fmt"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mvm-sh/mvm/comp"
	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/lang"
	"github.com/mvm-sh/mvm/stdlib"
	"github.com/mvm-sh/mvm/stdlib/stdmod"
	"github.com/mvm-sh/mvm/symbol"
	"github.com/mvm-sh/mvm/vm"
)

// ExitError is the error returned from Eval when interpreted code calls
// os.Exit, log.Fatal*, or any other path that bottoms out in the bound
// os.Exit stub. Callers type-assert (or errors.As) to recover Code and
// decide the host process exit status; treating it as a plain error is
// also fine for embedders that do not need the code.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// CleanExit marks ExitError as vm.CleanExit so the VM propagates it past any
// interpreted recover() (mirroring Go, where recover cannot intercept os.Exit)
// instead of treating it as a recoverable native panic.
func (e *ExitError) CleanExit() {}

var (
	debug              = os.Getenv("MVM_DEBUG") != ""
	traceLine, traceOp = ParseTraceModes(os.Getenv("MVM_TRACE"))
)

// ParseTraceModes parses a comma-separated trace-mode value (used by both
// MVM_TRACE and the -x CLI flag). Recognized tokens: "1" or "line" enable
// line tracing; "op" or "bytecode" enable bytecode tracing; "all" enables
// both. Unknown or empty tokens are ignored.
func ParseTraceModes(s string) (line, op bool) {
	for t := range strings.SplitSeq(s, ",") {
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
	hostStdio     bool              // keep interpreted os.Std* bound to host *os.File
	synthAttached map[*vm.Type]bool // types already passed through AttachSynthMethods
	Stats         Stats
}

// Close releases the interpreter's stub-pool handler slots (native only) and
// breaks the Machine->Interp link so it becomes collectable. Long-lived hosts
// spawning many interpreters should call it; do not use the interpreter after.
func (i *Interp) Close() {
	i.ReleaseSynthMethods()
	i.SetDebugInfo(nil)
}

// UseHostStdio keeps the interpreted os.Std{in,out,err} bound to the host's real
// *os.File streams rather than routing them through the machine's SetIO writers.
// The CLI (mvm run / mvm test) sets this so interpreted code sees genuine *os.File
// stdio -- os.Stdout.Fd(), isatty, a *os.File-typed parameter. Embedders and the
// in-process test harness leave it off so interpreted output (interpreted fmt,
// text/template writing os.Stdout) follows their SetIO writer. Call before Eval.
func (i *Interp) UseHostStdio() { i.hostStdio = true }

// Stats accumulates timing across all Eval calls on an Interp.
type Stats struct {
	CompileTime time.Duration
	RunTime     time.Duration
}

// NewInterpreter returns a new interpreter.
func NewInterpreter(s *lang.Spec) *Interp {
	i := &Interp{Compiler: comp.NewCompiler(s), Machine: vm.NewMachine()}
	i.SetStdlibFS(stdmod.DefaultFS())
	i.SetRemoteFS(stdmod.DefaultRemoteFS())
	return i
}

// Eval evaluates code string and return the last produced value if any, or an error.
// name labels the source for error positions (a file path or any identifier).
func (i *Interp) Eval(name, src string) (reflect.Value, error) {
	return i.evalCompiled(name, func() error { return i.Compile(name, src) })
}

// EvalFiles compiles several local source files as one main-package unit and
// runs the result. Backs `mvm run f1.go f2.go ...`: top-level symbols declared
// in any file are visible to the others regardless of file or declaration order.
func (i *Interp) EvalFiles(sources []goparser.PackageSource) (reflect.Value, error) {
	name := "main"
	if len(sources) > 0 {
		name = sources[0].Name
	}
	return i.evalCompiled(name, func() error { return i.CompileFiles(sources) })
}

// evalCompiled compiles via the supplied closure, then sets up and runs the VM,
// returning the last produced value. Shared by Eval and EvalFiles.
func (i *Interp) evalCompiled(name string, compile func() error) (res reflect.Value, err error) {
	codeOffset := len(i.Code)
	dataOffset := 0
	if codeOffset > 0 {
		// All data must be copied to the VM the first time only (re-entrance).
		dataOffset = len(i.Data)
	}
	// Drop the previous Eval's leftover init/main call shims so m.code
	// stays index-aligned with the compiler's Code.
	i.TrimCode(codeOffset)
	initsBefore := len(i.InitFuncs)

	if !i.stdlibPatched {
		i.patchStdlibOverrides()
		i.stdlibPatched = true
	}

	tCompile := time.Now()
	err = compile()
	i.Stats.CompileTime += time.Since(tCompile)
	if err != nil {
		return res, i.withSourceContext(err)
	}
	i.configureSentinels()

	i.Machine.MethodNames = i.Compiler.MethodNames()
	i.Machine.MethodFuncTypes = i.Compiler.MethodFuncTypes()

	// Materialize every reachable type's rtype (goparser builds them symbolically
	// post-flip), so the synth attach sees layout rtypes and the VM never reads a
	// nil Rtype at run time. Bind the active Machine so materialize can share a
	// method-bearing type's identity across this interp's re-Evals (see
	// vm.sharedMethodStructs).
	prevMachine := vm.SetActiveMachine(i.Machine)
	defer vm.SetActiveMachine(prevMachine)
	i.MaterializeAll()

	if err := i.attachSynthMethods(); err != nil {
		return res, i.withSourceContext(err)
	}

	i.TrimStack()
	i.Push(i.Data[dataOffset:]...)
	// start is the VM-code position of the freshly compiled code.
	// It can sit past codeOffset because prior Evals left their per-Evals
	// init/main call shims in m.code, and PopExit reclaims only the trailing Exit.
	start := i.PushCode(i.Code[codeOffset:]...)
	emitCall := func(fn string) {
		if s, ok := i.Symbols[fn]; ok {
			i.PushCode(vm.Instruction{Op: vm.Push, A: int32(i.Data[s.Index].Int())})
			i.PushCode(vm.Instruction{Op: vm.Call})
		}
	}
	if d := i.Defer; d.Active {
		// Wire each package's var-init group to its init-call shims for Go init
		// order (see comp.VarDeferral). off maps Code indexes into m.code (start
		// may sit past codeOffset on a re-entrant Eval). A group's jump routes to
		// its init shims, which jump to JumpPos+1 (the next group's vars, or rest).
		off := start - codeOffset
		// main shim sits right after the compiled code so `rest` falls into it.
		emitCall("main")
		i.PushCode(vm.Instruction{Op: vm.Exit})
		initIdx := initsBefore
		for _, g := range d.Groups {
			lInit := i.CodeLen()
			for k := 0; k < g.NumInits; k++ {
				emitCall(i.InitFuncs[initIdx])
				initIdx++
			}
			i.PatchJump(i.PushCode(vm.Instruction{Op: vm.Jump}), g.JumpPos+1+off)
			i.PatchJump(g.JumpPos+off, lInit)
		}
	} else {
		for _, fn := range i.InitFuncs[initsBefore:] {
			emitCall(fn)
		}
		emitCall("main")
		i.PushCode(vm.Instruction{Op: vm.Exit})
	}
	i.SetIP(max(start, i.Entry))
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
	i.EnableGoroutineFaults()
	if goparser.DebugComp {
		fmt.Fprint(os.Stderr, FormatStats(i))
		fmt.Fprintf(os.Stderr, "[comp] execution starts %s  +%v\n", name, goparser.Elapsed().Round(time.Microsecond))
	}
	tRun := time.Now()
	err = i.Run()
	i.Stats.RunTime += time.Since(tRun)
	// An unrecovered goroutine panic surfaces either as the channel-abort sentinel
	// (main was blocked) or as a pending fault (main finished first). record()
	// already logged it; exit non-zero like Go, which crashes on such a panic.
	if errors.Is(err, vm.ErrGoroutineFault) || (err == nil && i.PendingGoroutineFault() != nil) {
		err = &ExitError{Code: 2}
	}
	return i.Top().Reflect(), err
}

func (i *Interp) withSourceContext(err error) error {
	var ep interface{ ErrPos() int }
	if !errors.As(err, &ep) {
		return err
	}
	if snip := i.Sources.Snippet(ep.ErrPos()); snip != "" {
		return fmt.Errorf("%w%s", err, snip)
	}
	return err
}

func (i *Interp) installExitVirtualization() {
	if pkg, ok := i.Packages["os"]; ok {
		pkg.Values["Exit"] = vm.FromReflect(reflect.ValueOf(func(code int) {
			panic(&ExitError{Code: code})
		}))
	}
	// log is interpreted, so log.Fatal* need no bridge override: they call the
	// interpreted os.Exit, virtualized above to panic an ExitError.
}

// WordShapeDropReport forwards vm.WordShapeDropReport (MVM_WORDDROPS; see ADR-022).
func WordShapeDropReport() string { return vm.WordShapeDropReport() }

// FormatStats returns a multi-line summary of an Interp's accumulated work for the -stat CLI flag.
func FormatStats(i *Interp) string {
	totalLines, totalBytes, srcFiles := 0, 0, 0
	srcDirs := map[string]struct{}{}
	for _, s := range i.Sources {
		// Skip mvm-internal generic-template shims (goparser registers them
		// with a "/<shim>" suffix); they are scaffolding, not user source.
		if path.Base(s.Name) == "<shim>" {
			continue
		}
		totalLines += s.Lines()
		totalBytes += s.Len
		srcFiles++
		srcDirs[path.Dir(s.Name)] = struct{}{}
	}
	binPkgs := 0
	for _, p := range i.Packages {
		if p != nil && p.Bin {
			binPkgs++
		}
	}
	const instrSize = 16 // sizeof(vm.Instruction)
	var b strings.Builder
	fmt.Fprintln(&b, "mvm stats:")
	fmt.Fprintf(&b, "  packages: %d bridged, %d source-loaded\n", binPkgs, len(srcDirs))
	fmt.Fprintf(&b, "  sources:  %d (%d lines) (%d bytes)\n", srcFiles, totalLines, totalBytes)
	fmt.Fprintf(&b, "  code:     %d instructions (%d bytes)\n", len(i.Code), len(i.Code)*instrSize)
	dataLine := fmt.Sprintf("  data:     %d slots (%d bytes)", len(i.Data), len(i.Data)*vm.ValueSize)
	if h := i.HeapSize(); h > 0 {
		dataLine += fmt.Sprintf(", heap %d cells (%d bytes)", h, h*vm.ValueSize)
	}
	fmt.Fprintln(&b, dataLine)
	fmt.Fprintf(&b, "  compile:  %v\n", i.Stats.CompileTime)
	fmt.Fprintf(&b, "  execute:  %v\n", i.Stats.RunTime)
	return b.String()
}

// Needed only when io is interpreted (wasm, or MVM_INTERP=io); bridged io shares
// the host io.EOF, so there is nothing to reconcile. See vm/sentinel.go.
func (i *Interp) configureSentinels() {
	if i.SentinelsConfigured() {
		return
	}
	pkg, ok := i.Packages["io"]
	if !ok || pkg.Bin {
		return
	}
	if sym, ok := i.Symbols[goparser.QualifyName("io", "EOF")]; ok && sym.Index >= 0 {
		i.SetInterpEOFSlot(sym.Index)
	}
}

func (i *Interp) patchStdlibOverrides() {
	i.patchFmtBindings()
	i.patchStdStreams()
	i.installExitVirtualization()
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

// machineStream backs the interpreted os.Stdin/Stdout/Stderr bindings so that
// interpreted code routes through the interpreter's SetIO streams rather than the
// host fd. It matters when fmt/text-template etc. are interpreted (no fmt bridge
// to patch, e.g. on wasm) and write to os.Stdout directly: a host *os.File would
// reach the real fd 1, escaping a caller's SetIO writer (an embedder's buffer, a
// test's bytes.Buffer). The streams are used only as io.Reader/io.Writer.
type machineStream struct {
	m   *vm.Machine
	std int // 0=stdin, 1=stdout, 2=stderr
}

func (s *machineStream) Write(p []byte) (int, error) {
	if s.std == 2 {
		return s.m.Err().Write(p)
	}
	return s.m.Out().Write(p)
}

func (s *machineStream) Read(p []byte) (int, error) { return s.m.In().Read(p) }

// patchStdStreams redirects the interpreted os.Std{in,out,err} bindings to the
// machine's SetIO streams. A patched binding is consumed as-is (unlike the
// original reflect.ValueOf(&os.Stdout) var binding, which the importer derefs),
// so each value is a single *machineStream -- the type os.Std* keeps for its
// io.Reader/io.Writer uses.
func (i *Interp) patchStdStreams() {
	if i.hostStdio {
		return
	}
	pkg, ok := i.Packages["os"]
	if !ok {
		return
	}
	m := i.Machine
	pkg.Values["Stdin"] = vm.FromReflect(reflect.ValueOf(&machineStream{m: m, std: 0}))
	pkg.Values["Stdout"] = vm.FromReflect(reflect.ValueOf(&machineStream{m: m, std: 1}))
	pkg.Values["Stderr"] = vm.FromReflect(reflect.ValueOf(&machineStream{m: m, std: 2}))
}

// FuncNames returns top-level functions whose name matches cmd/go's isTest for
// prefix (exact match, e.g. "Test", or a non-lower-case rune after it), in
// source-declaration order.
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
		if rest := name[len(prefix):]; rest != "" {
			if r, _ := utf8.DecodeRuneInString(rest); unicode.IsLower(r) {
				continue
			}
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
