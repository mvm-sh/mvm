package stdlib

import (
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"github.com/mvm-sh/mvm/vm"
)

// frameKey is the dedup key for sentinelByFrame.
//
// Lifetime caveat: sentinelByFrame is a process-global sync.Map with no
// deletion path. Including *Machine makes the key set grow as
// O(distinct Machines x distinct call sites) rather than O(call sites),
// and each entry also pins one runtimeFuncMeta entry.
// For long-lived hosts that spawn many short-lived interpreters this is a
// slow leak (the agent-pool runners share a Machine across invocations,
// so they don't contribute new keys per call).
// A proper bound would move the cache onto vm.Machine and run a finalizer
// to drop the matching runtimeFuncMeta entries; deferred because captured
// pkg/errors-style stack traces can legitimately outlive the Machine
// that produced them, which complicates cleanup-on-GC.
type frameKey struct {
	M   *vm.Machine
	IP  int
	Pos uint32
}

var sentinelByFrame sync.Map // frameKey -> *runtime.Func

func init() {
	RegisterPackagePatcher("runtime", patchRuntime)
}

func patchRuntime(_ *vm.Machine, values map[string]vm.Value) {
	values["Callers"] = vm.FromReflect(reflect.ValueOf(func(skip int, pcs []uintptr) int {
		active := vm.ActiveMachine()
		if active == nil {
			return 0
		}
		return mvmCallers(active, skip, pcs)
	}))
	values["Caller"] = vm.FromReflect(reflect.ValueOf(func(skip int) (uintptr, string, int, bool) {
		active := vm.ActiveMachine()
		if active == nil {
			return 0, "", 0, false
		}
		return mvmCaller(active, skip)
	}))
	values["FuncForPC"] = vm.FromReflect(reflect.ValueOf(mvmFuncForPC))
	values["CallersFrames"] = vm.FromReflect(reflect.ValueOf(mvmCallersFrames))
	// CallersFrames returns *mvmFrames, so a `*runtime.Frames` field or variable
	// must resolve to *mvmFrames too -- else assigning the result fails reflect.Set
	// (zap internal/stacktrace.Stack.frames). Frame (singular, the Next result)
	// stays native; mvmFrames.Next returns a real runtime.Frame.
	values["Frames"] = vm.FromReflect(reflect.ValueOf((*mvmFrames)(nil)))
}

// mvmCaller mirrors runtime.Caller for the live interpreter stack.
// runtime.Caller(skip) counts skip 0 as the caller of Caller -- the first
// interpreted frame -- so it indexes the combined stack directly.
func mvmCaller(m *vm.Machine, skip int) (pc uintptr, file string, line int, ok bool) {
	pcs := mvmStackPCs(m)
	i := max(skip, 0)
	if i >= len(pcs) {
		return 0, "", 0, false
	}
	pc = pcs[i]
	if _, info := LookupRuntimeFuncByPC(pc); info != nil {
		file = info.File
		line = info.Line
	}
	return pc, file, line, true
}

// mvmFrames replaces *runtime.Frames for code that consumes PCs produced
// by the virtualized runtime.Callers.
type mvmFrames struct {
	pcs []uintptr
	pos int
}

// Next returns the next runtime.Frame, mirroring (*runtime.Frames).Next.
func (f *mvmFrames) Next() (runtime.Frame, bool) {
	if f.pos >= len(f.pcs) {
		return runtime.Frame{}, false
	}
	pc := f.pcs[f.pos]
	f.pos++
	frame := runtime.Frame{PC: pc}
	if fn := mvmFuncForPC(pc); fn != nil {
		frame.Func = fn
		// fn.Name()/fn.FileLine() are intercepted only when invoked through
		// reflect (vm.nativeMethodLookup). Calling them directly from Go
		// would hit the host runtime, which doesn't know our sentinel
		// addresses. Read the registered metadata directly instead.
		if info := LookupRuntimeFunc(fn); info != nil {
			frame.Function = info.Name
			frame.File = info.File
			frame.Line = info.Line
		}
	}
	return frame, f.pos < len(f.pcs)
}

func mvmCallersFrames(callers []uintptr) *mvmFrames {
	pcs := append([]uintptr(nil), callers...)
	return &mvmFrames{pcs: pcs}
}

// mvmCallers fills pcs with synthetic PCs for the live interpreter stack.
// runtime.Callers(skip) counts skip 0 as Callers itself (off the interpreted
// stack) and skip 1 as its caller (the first interpreted frame), so the start
// index into the combined stack is skip-1.
func mvmCallers(m *vm.Machine, skip int, pcs []uintptr) int {
	if len(pcs) == 0 {
		return 0
	}
	all := mvmStackPCs(m)
	n := 0
	for i := max(skip-1, 0); i < len(all) && n < len(pcs); i++ {
		pcs[n] = all[i]
		n++
	}
	return n
}

// mvmStackPCs returns sentinel PCs for the full logical call stack: the
// interpreted frames (innermost first) followed by the genuine host frames
// below the VM boundary (testing.tRunner, runtime.goexit, ...). The host tail
// lets runtime.Caller/Callers with a skip past the interpreted depth reach the
// real runtime frames, matching Go (zap's caller-skip-into-runtime tests).
func mvmStackPCs(m *vm.Machine) []uintptr {
	di := m.DebugInfo()
	if di == nil {
		return nil
	}
	var pcs []uintptr
	m.WalkCallStack(func(f vm.StackFrame) bool {
		rf := internSentinel(m, di, f)
		// pkg/errors' Frame.pc() does (uintptr(f) - 1) so we add 1.
		pcs = append(pcs, uintptr(unsafe.Pointer(rf))+1)
		return true
	})
	for _, f := range hostTailFrames() {
		rf := hostFrameSentinel(f)
		pcs = append(pcs, uintptr(unsafe.Pointer(rf))+1)
	}
	return pcs
}

var hostSentinelByPC sync.Map // uintptr (host pc) -> *runtime.Func

// hostFrameSentinel returns a sentinel *runtime.Func carrying the real host
// frame's Name/File/Line, cached per host PC so the metadata path stays
// uniform with interpreted-frame sentinels.
func hostFrameSentinel(f runtime.Frame) *runtime.Func {
	if v, ok := hostSentinelByPC.Load(f.PC); ok {
		return v.(*runtime.Func)
	}
	rf := NewRuntimeFuncSentinel()
	RegisterRuntimeFunc(rf, f.Function, f.File, f.Line)
	actual, _ := hostSentinelByPC.LoadOrStore(f.PC, rf)
	return actual.(*runtime.Func)
}

// hostTailFrames returns the genuine host frames below the VM boundary,
// innermost first. It walks the real host stack from the bottom
// (runtime.goexit) upward, keeping frames until the first mvm/reflect bridge
// frame that separates the interpreted stack from its host caller.
func hostTailFrames() []runtime.Frame {
	pcs := make([]uintptr, 64)
	for {
		n := runtime.Callers(2, pcs)
		if n < len(pcs) {
			pcs = pcs[:n]
			break
		}
		pcs = make([]uintptr, 2*len(pcs))
	}
	var all []runtime.Frame
	frames := runtime.CallersFrames(pcs)
	for {
		f, more := frames.Next()
		all = append(all, f)
		if !more {
			break
		}
	}
	var tail []runtime.Frame
	for _, f := range slices.Backward(all) {
		if isHostBoundary(f.Function) {
			break
		}
		tail = append(tail, f)
	}
	for l, r := 0, len(tail)-1; l < r; l, r = l+1, r-1 {
		tail[l], tail[r] = tail[r], tail[l]
	}
	return tail
}

// isHostBoundary reports whether name belongs to the mvm/reflect bridge that
// sits between the interpreted stack and its genuine host caller.
func isHostBoundary(name string) bool {
	return strings.HasPrefix(name, "github.com/mvm-sh/mvm/") ||
		strings.HasPrefix(name, "reflect.") ||
		strings.HasPrefix(name, "main.")
}

// internSentinel returns a *runtime.Func sentinel for the given frame,
// reusing a previously allocated one when the (Machine, IP, Pos) call
// site has been seen before.
func internSentinel(m *vm.Machine, di *vm.DebugInfo, f vm.StackFrame) *runtime.Func {
	key := frameKey{M: m, IP: f.IP, Pos: uint32(f.Pos)}
	if v, ok := sentinelByFrame.Load(key); ok {
		return v.(*runtime.Func)
	}
	file, line, _ := di.Sources.Resolve(int(f.Pos))
	var rawName string
	if f.TopLevel {
		// Top-level entry sequence (var initializers run before main).
		// FuncAt's Labels fallback would pick the closest preceding label,
		// which may misattribute the frame; force "init" so it qualifies as
		// "<pkg>.init".
		rawName = "init"
	} else {
		rawName = di.FuncAt(f.IP)
	}
	name := qualifyFuncName(rawName, file)
	if file == "" {
		file = "?"
		line = 0
	} else {
		file = "modfs/" + file
	}
	rf := NewRuntimeFuncSentinel()
	RegisterRuntimeFunc(rf, name, file, line)
	actual, loaded := sentinelByFrame.LoadOrStore(key, rf)
	if loaded {
		// Lost the race: another goroutine interned a sentinel for the
		// same key. The metadata is identical so it doesn't matter
		// which one wins; just drop ours and use theirs. The orphaned
		// sentinel stays in runtimeFuncMeta -- a small bounded leak
		// proportional to race count, not capture count.
		return actual.(*runtime.Func)
	}
	return rf
}

// mvmFuncForPC accepts either a sentinel pc produced by mvmCallers or a
// real host pc. For sentinels it returns the registered *runtime.Func;
// otherwise it delegates to runtime.FuncForPC so non-mvm uses still work.
func mvmFuncForPC(pc uintptr) *runtime.Func {
	if rf, _ := LookupRuntimeFuncByPC(pc); rf != nil {
		return rf
	}
	return runtime.FuncForPC(pc)
}

// qualifyFuncName turns a debug-info label such as "TestFormatNew" into
// "github.com/pkg/errors.TestFormatNew" using the import-path prefix of
// the function's source file.
//
// Method labels are normalized to Go's stack-trace convention:
//   - "T.M" -> "<pkg>.T.M"
//   - "*T.M" -> "<pkg>.(*T).M"
//
// Anonymous closures (label starts with '#') are stripped of the '#' and
// qualified with the package path so the result matches Go's
// "<pkg>.<outer>.funcN" form.
func qualifyFuncName(label, file string) string {
	if label == "" {
		return "?"
	}
	// Mvm marks anonymous closures with a leading '#'. Nested anons in
	// goparser stack hashes (parser builds the name as "#" + enclosing func
	// name + ..., where that name already carries its own '#'), so strip them all.
	for strings.HasPrefix(label, "#") {
		label = label[1:]
	}
	// Inner '#' from concatenated fnames (e.g. "#X.#Y.func1") have no
	// equivalent in Go's stack trace output; strip them as well.
	label = strings.ReplaceAll(label, "#", "")
	short := label
	if i := strings.LastIndexByte(label, '/'); i >= 0 {
		short = label[i+1:]
	}
	// Method on pointer receiver: rewrite "*T.M" as "(*T).M".
	if strings.HasPrefix(short, "*") {
		if dot := strings.IndexByte(short, '.'); dot > 1 {
			short = "(" + short[:dot] + ")" + short[dot:]
		}
	}
	pkgPath, _ := splitPathFile(file)
	if pkgPath == "" {
		return short
	}
	return pkgPath + "." + short
}

func splitPathFile(file string) (dir, name string) {
	i := strings.LastIndexByte(file, '/')
	if i < 0 {
		return "", file
	}
	return file[:i], file[i+1:]
}
