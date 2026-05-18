package stdlib

import (
	"reflect"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/mvm-sh/mvm/vm"
)

// frameKey is the dedup key for sentinelByFrame: the bytecode position
// IP plus the source position Pos uniquely identify a call site within
// a single program. Two calls to runtime.Callers from the same line
// reuse the same sentinel, which bounds runtimeFuncMeta growth at the
// number of distinct interpreted call sites rather than the number of
// stack captures.
//
// M is included because distinct Machines have disjoint code spaces but
// overlapping IP ranges; without it, the first Machine to register
// sentinel{IP:0,...} would steal that key from every other Machine's
// frame zero. M is the *vm.Machine pointer of the Machine that walked
// the frame.
//
// Lifetime caveat: sentinelByFrame is a process-global sync.Map with no
// deletion path. Including *Machine makes the key set grow as
// O(distinct Machines x distinct call sites) rather than O(call sites),
// and each entry also pins one runtimeFuncMeta entry in vm. For
// long-lived hosts that spawn many short-lived interpreters this is a
// slow leak (the agent-pool runners share a Machine across invocations,
// so they don't contribute new keys per call). A proper bound would
// move the cache onto vm.Machine and run a finalizer to drop the
// matching runtimeFuncMeta entries; deferred because captured
// pkg/errors-style stack traces can legitimately outlive the Machine
// that produced them, which complicates cleanup-on-GC.
type frameKey struct {
	M   *vm.Machine
	IP  int
	Pos uint32
}

var sentinelByFrame sync.Map // frameKey -> *runtime.Func

// init registers a PackagePatcher for "runtime" so that interpreted code
// calling runtime.Callers / runtime.FuncForPC observes the interpreter's
// own call stack instead of the host Go stack frames inside vm.Machine.
//
// The replacement Callers walks m's frame-pointer chain and produces one
// synthetic uintptr per yielded interpreter frame. Each synthetic PC is
// the address of a freshly allocated *runtime.Func sentinel registered
// with vm.RegisterRuntimeFunc, so vm.nativeMethodLookup intercepts the
// later Name()/FileLine() calls on the sentinel and returns the recorded
// metadata. PCs that do not match a sentinel fall through to the host
// runtime.FuncForPC, preserving behavior for code that captured real
// host PCs.
func init() {
	RegisterPackagePatcher("runtime", patchRuntime)
}

func patchRuntime(_ *vm.Machine, values map[string]vm.Value) {
	values["Callers"] = vm.FromReflect(reflect.ValueOf(func(skip int, pcs []uintptr) int {
		// makeCallFunc spawns a fresh Machine for native callbacks, so the
		// Machine passed to the patcher is not necessarily the one running
		// when this bridge fires. Resolve the live one through the
		// active-machine stack.
		active := vm.ActiveMachine()
		if active == nil {
			return 0
		}
		return mvmCallers(active, skip, pcs)
	}))
	values["FuncForPC"] = vm.FromReflect(reflect.ValueOf(mvmFuncForPC))
	values["CallersFrames"] = vm.FromReflect(reflect.ValueOf(mvmCallersFrames))
}

// mvmFrames replaces *runtime.Frames for code that consumes PCs produced
// by the virtualized runtime.Callers. The native runtime.CallersFrames
// would not recognize our synthetic sentinel PCs and would yield empty
// frames; this implementation preserves the PC and resolves names/files
// via mvmFuncForPC.
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
		if info := vm.LookupRuntimeFunc(fn); info != nil {
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
// The skip semantics mirror runtime.Callers: skip=0 identifies Callers
// itself. mvm has no Callers vm-frame, so we drop the first (skip-1)
// interpreter frames before recording.
func mvmCallers(m *vm.Machine, skip int, pcs []uintptr) int {
	if len(pcs) == 0 {
		return 0
	}
	di := m.DebugInfo()
	if di == nil {
		return 0
	}
	drop := skip - 1
	if drop < 0 {
		drop = 0
	}
	n := 0
	m.WalkCallStack(func(f vm.StackFrame) bool {
		if drop > 0 {
			drop--
			return true
		}
		if n >= len(pcs) {
			return false
		}
		rf := internSentinel(m, di, f)
		// pkg/errors' Frame.pc() does (uintptr(f) - 1) so we add 1.
		pcs[n] = uintptr(unsafe.Pointer(rf)) + 1
		n++
		return true
	})
	return n
}

// internSentinel returns a *runtime.Func sentinel for the given frame,
// reusing a previously allocated one when the (Machine, IP, Pos) call
// site has been seen before. First-encounter sentinels are registered
// with vm.RegisterRuntimeFunc. The intern cache bounds runtimeFuncMeta
// size at O(distinct call sites across all interpreters) instead of
// O(stack captures). Keying on the Machine pointer is what keeps two
// interpreters running on different goroutines from stealing each
// other's frame-zero sentinel.
//
// di is threaded through instead of derived from m.DebugInfo() because
// m.DebugInfo() invokes a debugInfoFn callback that rebuilds the
// DebugInfo on demand; mvmCallers hoists the result before the
// WalkCallStack loop so each frame doesn't re-trigger the rebuild.
func internSentinel(m *vm.Machine, di *vm.DebugInfo, f vm.StackFrame) *runtime.Func {
	key := frameKey{M: m, IP: f.IP, Pos: uint32(f.Pos)} //nolint:gosec
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
	rf := vm.NewRuntimeFuncSentinel()
	vm.RegisterRuntimeFunc(rf, name, file, line)
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
	if rf, _ := vm.LookupRuntimeFuncByPC(pc); rf != nil {
		return rf
	}
	return runtime.FuncForPC(pc)
}

// qualifyFuncName turns a debug-info label such as "TestFormatNew" into
// "github.com/pkg/errors.TestFormatNew" using the import-path prefix of
// the function's source file. file has the form "<pkgPath>/<filename>"
// (set by goparser's source registry).
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
	// goparser stack hashes (parser builds the name as "#" + p.fname + ...
	// where p.fname already contains its own '#'), so strip them all.
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
