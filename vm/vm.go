// Package vm implement a stack based virtual machine.
package vm

import (
	"errors"
	"fmt" // for tracing only
	"io"
	"iter"
	"math" // for float arithmetic
	"math/bits"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe" // to allow setting unexported struct fields
	"weak"

	"github.com/mvm-sh/mvm/runtype"
)

// Op is a VM opcode (bytecode instruction).
type Op int32

//go:generate stringer -type=Op

// Closure bundles a function code address with its captured variables.
type Closure struct {
	Code int      // code address (same as the plain-int function value)
	Heap []*Value // heap-allocated cells, one per captured variable
}

// SelectCaseInfo describes one case of a select statement.
type SelectCaseInfo struct {
	Dir    reflect.SelectDir // SelectSend, SelectRecv, or SelectDefault
	Slot   int               // local/global index for received value (-1 if unused)
	OkSlot int               // local/global index for ok bool (-1 if unused)
	Local  bool              // true if slots are local (frame-relative), false for global
	Cell   bool              // true if Slot holds a heap-cell pointer (captured var); write through it
	OkCell bool              // true if OkSlot holds a heap-cell pointer (captured var)
}

// SelectMeta holds metadata for a select statement, stored in the data section.
type SelectMeta struct {
	Cases    []SelectCaseInfo
	TotalPop int // precomputed number of stack slots consumed by channel/value entries
}

// Byte-code instruction set.
const (
	// Instruction effect on stack: values consumed -- values produced.
	Nop          Op = iota // --
	Addr                   // a -- &a ;
	AddrLocal              // -- &local ; push pointer to mem[fp-1+$1]; promotes slot to addressable storage so writes via the pointer propagate back; $2!=0 retypes a func slot to func type globals[$2-1]
	Append                 // slice [v0..vn-1] -- slice' ; append $0 values to slice
	AppendSlice            // slice [v0..vn-1] -- slice' ; pack $0 values into []T, reflect.AppendSlice; elem type at mem[$1]; $0=0 means spread mode: append(a, b...)
	Call                   // f [a1 .. ai] -- [r1 .. rj] ; r1, ... = prog[f](a1, ...); B bit 15 = spread flag
	CallImm                // [a1 .. ai] -- [r1 .. rj] ; $1=dataIdx of func, $2=narg<<16|nret
	CallImmFast            // like CallImm; emitted when no callee param has reflect Kind Struct or Array so detachByValueArgs can be elided
	Cap                    // -- x ; x = cap(mem[sp-$0])
	Clear                  // x -- ; clear(x): delete all map entries or zero all slice elements
	Convert                // v -- v' ; v' = convert(v, type at mem[$1]); optional $2 = stack depth offset
	CopySlice              // dst src -- n ; n = copy(dst, src)
	DeferPush              // func [a0..an-1] -- func [a0..an-1] [packed prevHead retIP] ; register deferred call on stack; $0=narg, $1=1 if native
	DeferRet               // -- ; sentinel: restore outer frame after a deferred call returns
	DeleteMap              // map key -- ; delete(map, key)
	Deref                  // x -- *x ;
	DerefSet               // ptr val -- ; *ptr = val
	Equal                  // n1 n2 -- cond ; cond = n1 == n2
	EqualSet               // n1 n2 -- n1 cond ; cond = n1 == n2
	Exit                   // -- ;
	Field                  // s -- f ; f = s.FieldIndex($1, ...)
	FieldFset              // s i v -- s; s.FieldIndex(i) = v
	FieldRefSet            // fref v -- ; fref = v (via setFuncField)
	FieldSet               // s d -- s ; s.FieldIndex($1, ...) = d
	Fnew                   // -- x; x = new mem[$1]
	FnewE                  // -- x; x = new mem[$1].Elem()
	Get                    // addr -- value ; value = mem[addr]
	Grow                   // -- ; sp += $1
	HeapAlloc              // -- &cell ; cell = new(Value), push its pointer
	HeapGet                // -- v    ; v = *State.Heap[$1]
	HeapPtr                // -- &cell ; push State.Heap[$1] itself (transitive capture)
	HeapSet                // v --    ; *State.Heap[$1] = v
	CellGet                // -- v    ; cell = mem[fp-1+$1].(*Value); push *cell
	CellSet                // v --    ; cell = mem[fp-1+$1].(*Value); *cell = v
	IfaceCall              // iface -- closure ; dynamic dispatch method $1 on iface
	IfaceWrap              // v -- iface ; wrap v in Iface{type at $1, v}
	Index                  // a i -- a[i] ;
	IndexAddr              // a i -- &a[i] ; pointer to element
	IndexSet               // a i v -- a; a[i] = v
	Jump                   // -- ; ip += $1
	JumpFalse              // cond -- ; if cond { ip += $1 }
	JumpSetFalse           //
	JumpSetTrue            //
	JumpTrue               // cond -- ; if cond { ip += $1 }
	Len                    // -- x; x = mem[sp-$1]
	MapIndex               // a i -- a[i]
	MapIndexOk             // a i -- v ok ; v, ok = a[i]
	MapSet                 // a i v -- a; a[i] = v
	MkClosure              // code [&c0..&cn-1] -- clo ; clo = Closure{code, heap}
	MkMap                  // (size?) -- map ; create a map of the type at mem[$0] (keeps a named map's identity); $0<0 (= -idx-1) pops a size hint
	MkSlice                // [v0..vn-1] -- slice ; collect $0 values into []T, elem type at mem[$1]
	New                    // -- x; mem[fp+$1] = new mem[$2]
	Next                   // -- ; iterator next, set K
	Next0                  // -- ; iterator next, no variable
	Next2                  // -- ; iterator next, set K V
	Not                    // c -- r ; r = !c
	Panic                  // v -- ; pop value, start stack unwinding
	PanicUnwind            // -- ; sentinel: handle panic stack unwinding
	Pop                    // v --
	PtrNew                 // -- ptr ; ptr = new(T), type at mem[$0]
	Pull                   // a -- a s n; pull iterator next and stop function
	Pull2                  // a -- a s n; pull iterator next and stop function
	Push                   // -- v
	Recover                // -- v ; push recovered value (or nil if not panicking in a deferred call)
	Return                 // [r1 .. ri] -- ; exit frame, nret and frameBase from frames
	SetGlobal              // v -- ; mem[$1] = v (globals)
	SetLocal               // v -- ; mem[fp-1+$1] = v
	SetS                   // dest val -- ; dest.Set(val)
	Slice                  // a l h -- a; a = a [l:h]
	Slice3                 // a l h m -- a; a = a[l:h:m]
	Stop                   // -- iterator stop; sp -= 3 + $1
	Swap                   // --
	Trap                   // -- ; pause VM execution and enter debug mode
	TypeAssert             // iface -- v [ok] ; assert iface holds type at mem[$1]; $2=0 panics, $2=1 ok form
	TypeBranch             // iface -- ; pop iface; if iface doesn't hold type at mem[$2] (or $2==-1 for nil), ip += $1
	WrapFunc               // mvmFuncVal -- MvmFunc ; wrap mvm func in reflect.MakeFunc for native callbacks; $0=typeIdx, $1=depth from sp (0=top)
	MkMethodExpr           // -- f ; push func value for interpreted method expression T.M; $0=method code global, $1=method-expr (recv-first) typeIdx

	// Goroutine and channel opcodes.
	GoCall     // f [a1..ai] -- ; spawn goroutine; $0=narg
	GoCallImm  // [a1..ai] -- ; spawn goroutine to known func; $0=dataIdx, $1=narg
	MkChan     // -- ch ; create channel; $0=elemTypeIdx, $1=bufsize (-1=from stack)
	ChanSend   // ch v -- ; send to channel
	ChanRecv   // ch -- v [ok] ; receive from channel; $0=1 for ok-form
	ChanClose  // ch -- ; close channel
	SelectExec // ch0 [v0] .. chN [vN] -- chosenIdx ; $0=metaIdx, $1=ncase; calls reflect.Select

	Print   // [v0..vn-1] -- ; print $0 values to m.out
	Println // [v0..vn-1] -- ; println $0 values to m.out, space-separated, trailing newline

	Min // [v0..vn-1] -- min ; find min of $0 values; $1 = reflect.Kind for dispatch
	Max // [v0..vn-1] -- max ; find max of $0 values; $1 = reflect.Kind for dispatch

	Complex // f1 f2 -- c ; c = complex(f1, f2); $0 = reflect.Kind for dispatch
	Real    // c -- f ; f = real(c); $0 = reflect.Kind for dispatch
	Imag    // c -- f ; f = imag(c); $0 = reflect.Kind for dispatch

	// Per-type numeric opcodes. Each block of NumTypes (12) opcodes follows the
	// order: Int, Int8, Int16, Int32, Int64, Uint, Uint8, Uint16, Uint32, Uint64, Float32, Float64.
	// The compiler computes: baseOp + Op(NumKindOffset[kind]).

	AddStr     // s1 s2 -- s ; s = s1 + s2 (string concatenation)
	GreaterStr // s1 s2 -- cond ; cond = s1 > s2
	LowerStr   // s1 s2 -- cond ; cond = s1 < s2

	AddInt // n1 n2 -- sum
	AddInt8
	AddInt16
	AddInt32
	AddInt64
	AddUint
	AddUint8
	AddUint16
	AddUint32
	AddUint64
	AddFloat32
	AddFloat64

	SubInt // n1 n2 -- diff
	SubInt8
	SubInt16
	SubInt32
	SubInt64
	SubUint
	SubUint8
	SubUint16
	SubUint32
	SubUint64
	SubFloat32
	SubFloat64

	MulInt // n1 n2 -- prod
	MulInt8
	MulInt16
	MulInt32
	MulInt64
	MulUint
	MulUint8
	MulUint16
	MulUint32
	MulUint64
	MulFloat32
	MulFloat64

	NegInt // n -- -n
	NegInt8
	NegInt16
	NegInt32
	NegInt64
	NegUint
	NegUint8
	NegUint16
	NegUint32
	NegUint64
	NegFloat32
	NegFloat64

	GreaterInt // n1 n2 -- cond
	GreaterInt8
	GreaterInt16
	GreaterInt32
	GreaterInt64
	GreaterUint
	GreaterUint8
	GreaterUint16
	GreaterUint32
	GreaterUint64
	GreaterFloat32
	GreaterFloat64

	LowerInt // n1 n2 -- cond
	LowerInt8
	LowerInt16
	LowerInt32
	LowerInt64
	LowerUint
	LowerUint8
	LowerUint16
	LowerUint32
	LowerUint64
	LowerFloat32
	LowerFloat64

	DivInt // n1 n2 -- quot
	DivInt8
	DivInt16
	DivInt32
	DivInt64
	DivUint
	DivUint8
	DivUint16
	DivUint32
	DivUint64
	DivFloat32
	DivFloat64

	RemInt // n1 n2 -- rem (integer only)
	RemInt8
	RemInt16
	RemInt32
	RemInt64
	RemUint
	RemUint8
	RemUint16
	RemUint32
	RemUint64
	RemFloat32 // unused, but keeps NumTypes alignment
	RemFloat64 // unused, but keeps NumTypes alignment

	// Bitwise opcodes (generic, operate on raw uint64 bits).
	BitAnd    // n1 n2 -- n1 & n2
	BitOr     // n1 n2 -- n1 | n2
	BitXor    // n1 n2 -- n1 ^ n2
	BitAndNot // n1 n2 -- n1 &^ n2
	BitShl    // n1 n2 -- n1 << n2
	BitShr    // n1 n2 -- n1 >> n2 (arithmetic for signed)
	BitComp   // n -- ^n

	// Bit manipulation opcodes (32-bit and 64-bit variants).
	Clz32    // n -- count ; count leading zeros (32-bit)
	Clz64    // n -- count ; count leading zeros (64-bit)
	Ctz32    // n -- count ; count trailing zeros (32-bit)
	Ctz64    // n -- count ; count trailing zeros (64-bit)
	Popcnt32 // n -- count ; population count (32-bit)
	Popcnt64 // n -- count ; population count (64-bit)
	Rotl32   // n k -- result ; rotate left (32-bit)
	Rotl64   // n k -- result ; rotate left (64-bit)
	Rotr32   // n k -- result ; rotate right (32-bit)
	Rotr64   // n k -- result ; rotate right (64-bit)

	// Float math opcodes (unary: 1 operand; binary: 2 operands).
	AbsFloat32      // n -- |n|
	AbsFloat64      // n -- |n|
	SqrtFloat32     // n -- sqrt(n)
	SqrtFloat64     // n -- sqrt(n)
	CeilFloat32     // n -- ceil(n)
	CeilFloat64     // n -- ceil(n)
	FloorFloat32    // n -- floor(n)
	FloorFloat64    // n -- floor(n)
	TruncFloat32    // n -- trunc(n)
	TruncFloat64    // n -- trunc(n)
	NearestFloat32  // n -- nearest(n)
	NearestFloat64  // n -- nearest(n)
	MinFloat32      // a b -- min(a,b)
	MinFloat64      // a b -- min(a,b)
	MaxFloat32      // a b -- max(a,b)
	MaxFloat64      // a b -- max(a,b)
	CopysignFloat32 // a b -- copysign(a,b)
	CopysignFloat64 // a b -- copysign(a,b)

	// Immediate operand variants: fold Push+BinOp into one instruction.
	// Arg[0] holds the right-hand constant (int, sign-extended to int64).
	AddIntImm      // n -- n+$1
	SubIntImm      // n -- n-$1
	MulIntImm      // n -- n*$1
	GreaterIntImm  // n -- n>$1  (signed)
	GreaterUintImm // n -- n>$1 (unsigned)
	LowerIntImm    // n -- n<$1  (signed)
	LowerUintImm   // n -- n<$1  (unsigned)

	GetGlobal    // -- value ; value = mem[$1] (global variable, syncs num from ref if needed)
	GetLocal     // -- value ; value = mem[$1+fp-1] (local variable, no scope check)
	GetLocalSync // -- value ; value = mem[$1+fp-1] and re-read num from ref (used after AddrLocal)
	NextLocal    // -- ; iterator next, set K (local scope); like Next but scope is always Local
	Next2Local   // -- ; iterator next, set K V (local scope); like Next2 but scope is always Local

	// Fused GetLocal + operation superinstructions.
	// $1 = local offset (as in GetLocal), $2 = immediate operand.
	GetLocal2              // -- v1 v2 ; push two locals: mem[$1+fp-1] then mem[$2+fp-1]
	GetLocalAddIntImm      // -- n+$2 ; push local $1 then add immediate $2
	GetLocalSubIntImm      // -- n-$2 ; push local $1 then subtract immediate $2
	GetLocalMulIntImm      // -- n*$2 ; push local $1 then multiply by immediate $2
	GetLocalLowerIntImm    // -- cond ; push local $1 then compare < immediate $2 (signed)
	GetLocalLowerUintImm   // -- cond ; push local $1 then compare < immediate $2 (unsigned)
	GetLocalGreaterIntImm  // -- cond ; push local $1 then compare > immediate $2 (signed)
	GetLocalGreaterUintImm // -- cond ; push local $1 then compare > immediate $2 (unsigned)
	GetLocalReturn         // -- ; push local $1 then return (nret/frameBase from frame)

	// Fused compare + conditional-jump superinstructions.
	// Only LowerInt variants are needed, compiler rewrites Greater comparisons
	// using the identity: (a > imm) same as !(a < imm+1).
	LowerIntImmJumpFalse         // n -- ; if n >= $2 { ip += $1 } ; sp--
	LowerIntImmJumpTrue          // n -- ; if n < $2 { ip += $1 } ; sp--
	GetLocalLowerIntImmJumpFalse // -- ; if local[$1.lo] >= $2 { ip += $1.hi } ($1 = jumpOff_int16<<16 | localOff_int16, $2 = imm32)
	GetLocalLowerIntImmJumpTrue  // -- ; if local[$1.lo] < $2 { ip += $1.hi } ($1 = jumpOff_int16<<16 | localOff_int16, $2 = imm32)

	// In-place local update super-instructions for `x op= y` and `x op= n`,
	// collapsing the GetLocal2+RHS+SetLocal+Pop sequence. No stack effect.
	AddLocalLocal  // -- ; local[$1] += local[$2]
	SubLocalLocal  // -- ; local[$1] -= local[$2]
	AddLocalIntImm // -- ; local[$1] += $2 (signed, fits int32)
	SubLocalIntImm // -- ; local[$1] -= $2 (signed, fits int32)
	IndexSetBool   // a i -- ; a[i] = bool($1)  (fuses Push/GetGlobal bool + IndexSet + Pop)

	MarkNamedRet // -- ; flag this frame as having captured named returns (set bit in retIPInfo)

	// Complex arithmetic. Operands live in ref (complex does not fit the num
	// word); $0 = reflect.Kind (Complex64 or Complex128) for precision.
	AddComplex // c1 c2 -- sum
	SubComplex // c1 c2 -- diff
	MulComplex // c1 c2 -- prod
	DivComplex // c1 c2 -- quot
	NegComplex // c -- -c
)

// Memory attributes.
const (
	Global = 0
	Local  = 1
)

// frameOverhead is the number of bookkeeping slots in a call frame
// (deferHead, retIP, prevFP), between the arguments and locals.
const frameOverhead = 3

// Pos is the source code position of instruction.
type Pos int32

// Instruction represents a virtual machine bytecode instruction (16 bytes).
// Fields A, B hold up to 2 immediate operands (0 when unused).
type Instruction struct {
	Op   Op
	A, B int32
	Pos  Pos
}

func (i Instruction) String() (s string) {
	s = fmt.Sprintf("%3d: %v", i.Pos, i.Op)
	if i.A != 0 || i.B != 0 {
		s += fmt.Sprintf(" %v", i.A)
	}
	if i.B != 0 {
		s += fmt.Sprintf(" %v", i.B)
	}
	return s
}

// Code represents the virtual machine byte code.
type Code []Instruction

// Machine is a stack-based virtual machine that executes bytecode instructions.
type Machine struct {
	code       Code       // code to execute
	globals    []Value    // global variable storage, shared across goroutines (set by Push)
	mem        []Value    // stack only (no globals; indices are frame-relative)
	ip, fp     int        // instruction pointer and frame pointer
	heap       []*Value   // active closure's captured cells (nil for plain functions)
	heapFrames [][]*Value // saved caller heaps (only for closure calls where heap != nil)

	panicking     bool        // true while unwinding due to panic
	panicVal      Value       // value passed to panic()
	panicInfo     *PanicError // diagnostic snapshot (source pos + mvm stack) captured at panic before unwind
	panicReraised bool        // panicInfo was adopted from a re-entrant run via invokeNative

	baseCodeLen int // len(code) before Run() appends sentinel instructions

	// trampBase, when non-zero, marks code as a shared pooledCode slice: the
	// Run sentinels sit at trampBase and the static Call/Exit trampoline table
	// follows, so re-entry never appends to code (an append on the shared
	// capacity-capped slice would copy the whole program). See ensurePooledCode.
	trampBase       int
	pooledCode      []Instruction // cached shared runner code (built by ensurePooledCode)
	pooledCodeBase  int           // baseCodeLen pooledCode was built from
	pooledCodeEpoch int           // codeEpoch pooledCode was built at
	codeEpoch       int           // bumped by TrimCode: trim+regrow can hit the same length with different content

	funcFields   *funcFieldsTable // see funcFieldsTable doc
	funcWrappers *funcWrapTable   // see funcWrapTable doc
	typesByRtype *typesIndex      // see typesIndex doc

	fault         *goroutineFault // shared goroutine-panic sink, lazily created on first `go`
	faultContinue bool            // policy seed copied into fault when it is created
	isRoot        bool            // the top-level machine; only it aborts channel waits on a fault

	in       io.Reader // machine standard input (nil = os.Stdin)
	out, err io.Writer // machine standard output and error

	MethodNames     []string       // names by global method ID
	MethodFuncTypes []reflect.Type // bound-method func type (no receiver) by global method ID

	// runnerPool holds reusable runner Machines for native->mvm callbacks.
	// Safe for concurrent Get/Put across goroutines.
	runnerPool sync.Pool

	debugInfoFn func() *DebugInfo // builds DebugInfo on demand (breaks vm->comp cycle)
	debugIn     io.Reader         // debug command input (nil = os.Stdin)
	debugOut    io.Writer         // debug output (nil = os.Stderr)
	trapOrig    int               // ip to resume after Trap

	traceFlags    uint8      // bitmask of traceFlag* values; checked in the hot loop via a single load
	traceLastPos  Pos        // last instruction Pos seen by traceStep (fast-path dedup)
	traceLastFile string     // last source file emitted (slow-path dedup across distinct Pos on same line)
	traceLastLine int        // last source line emitted
	traceDI       *DebugInfo // lazy cache for traceStep; invalidated by SetDebugInfo

	iterStack []iterEntry // active range-loop iterators, kept off the value stack so a defer in the body can't displace them
	iterBase  int         // iterStack floor for the current Run (entries below belong to an outer re-entrant Run)

	// PanicNilCompat keeps panic(nil) recovering as nil (pre-Go 1.21 semantics)
	// instead of substituting *runtime.PanicNilError. Set when the target
	// module's go directive predates 1.21, mirroring GODEBUG panicnil.
	PanicNilCompat bool
}

// iterEntry is one active range-loop iterator.
type iterEntry struct {
	fp         int
	next, stop Value
}

func (m *Machine) popIterStop() {
	e := m.iterStack[len(m.iterStack)-1]
	m.iterStack = m.iterStack[:len(m.iterStack)-1]
	e.stop.ref.Interface().(func())()
}

func (m *Machine) dropIterFrames(fp int) {
	for len(m.iterStack) > m.iterBase && m.iterStack[len(m.iterStack)-1].fp >= fp {
		m.popIterStop()
	}
}

// traceFlag* are bits stored in Machine.traceFlags.
const (
	traceFlagLine uint8 = 1 << iota // emit one line per distinct source line
	traceFlagOp                     // emit one line per executed bytecode instruction
)

// NewMachine returns a pointer on a new Machine.
func NewMachine() *Machine { return &Machine{in: os.Stdin, out: os.Stdout, err: os.Stderr} }

// SetIO sets the I/O streams for the machine.
func (m *Machine) SetIO(in io.Reader, out, err io.Writer) { m.in = in; m.out = out; m.err = err }

// Out returns the machine's standard output writer.
func (m *Machine) Out() io.Writer { return m.out }

// SetDebugInfo registers a function that builds DebugInfo on demand and
// invalidates the trace-step cache so the next traceStep call rebuilds.
func (m *Machine) SetDebugInfo(fn func() *DebugInfo) {
	m.debugInfoFn = fn
	m.traceDI = nil
}

// DebugInfo returns the current DebugInfo, or nil if no builder was
// registered with SetDebugInfo.
func (m *Machine) DebugInfo() *DebugInfo {
	if m.debugInfoFn == nil {
		return nil
	}
	return m.debugInfoFn()
}

// CallSitePos returns the source Pos of the instruction that triggered
// the currently executing native call.  Returns 0 when the IP is out of range.
func (m *Machine) CallSitePos() Pos {
	if m.ip <= 0 || m.ip-1 >= len(m.code) {
		return 0
	}
	return m.code[m.ip-1].Pos
}

// SetDebugIO sets the I/O streams for the interactive debug mode.
func (m *Machine) SetDebugIO(in io.Reader, out io.Writer) {
	m.debugIn = in
	m.debugOut = out
}

// SetTracing enables or disables `set -x`-style line tracing.
// Toggles take effect at the next Run().
func (m *Machine) SetTracing(on bool) { m.setTraceFlag(traceFlagLine, on) }

// Tracing reports whether line tracing is enabled.
func (m *Machine) Tracing() bool { return m.traceFlags&traceFlagLine != 0 }

// SetTraceOps enables or disables bytecode-level tracing.
func (m *Machine) SetTraceOps(on bool) { m.setTraceFlag(traceFlagOp, on) }

// TraceOps reports whether bytecode-level tracing is enabled.
func (m *Machine) TraceOps() bool { return m.traceFlags&traceFlagOp != 0 }

func (m *Machine) setTraceFlag(bit uint8, on bool) {
	if on {
		m.traceFlags |= bit
	} else {
		m.traceFlags &^= bit
	}
}

const (
	traceTopDepth     = 3  // operand stack window size
	traceIndentSpaces = 2  // number of spaces per call-stack level
	traceIndentMax    = 32 // maximum number of levels
)

func traceIndent(mem []Value, fp int) string {
	d := 0
	for fp > 0 && fp-1 < len(mem) {
		d++
		fp = int(mem[fp-1].num &^ (1 << 63))
	}
	d-- // discard Eval driver frame
	if d <= 0 {
		return ""
	}
	if d > traceIndentMax {
		d = traceIndentMax
	}
	return strings.Repeat(" ", d*traceIndentSpaces)
}

func (m *Machine) traceOp(ip, fp int, c *Instruction, mem []Value, sp int) {
	_, _ = fmt.Fprintf(m.err, "+ %s[ip:%-4d sp:%-3d fp:%-3d]  [%-16s]  %s\n",
		traceIndent(mem, fp), ip, sp, fp, opString(c), stackTop(mem, sp, fp, traceTopDepth))
}

func opString(c *Instruction) string {
	s := c.Op.String()
	if c.A != 0 || c.B != 0 {
		s += fmt.Sprintf(" %d", c.A)
	}
	if c.B != 0 {
		s += fmt.Sprintf(" %d", c.B)
	}
	return s
}

func stackTop(mem []Value, sp, fp, n int) string {
	if sp < 0 {
		return "[]"
	}
	start := sp + 1 - n
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	var sb strings.Builder
	sb.WriteByte('[')
	if truncated {
		sb.WriteString("... ")
	}
	for i := start; i <= sp; i++ {
		if i > start {
			sb.WriteByte(' ')
		}
		v := mem[i]
		if v.ref.IsValid() {
			fmt.Fprintf(&sb, "%d:%v", i, v.Interface())
			continue
		}
		switch i {
		case fp - 3:
			fmt.Fprintf(&sb, "%d:deferHead=%d", i, v.num)
		case fp - 2:
			retIP := int(int32(v.num))
			nret := int((v.num >> 32) & retNretMask)
			fb := int(v.num >> 48)
			fmt.Fprintf(&sb, "%d:ret=%d,nret=%d,fb=%d", i, retIP, nret, fb)
		case fp - 1:
			prevFP := int(v.num &^ (1 << 63))
			heap := v.num>>63 != 0
			if heap {
				fmt.Fprintf(&sb, "%d:prevFP=%d,heap", i, prevFP)
			} else {
				fmt.Fprintf(&sb, "%d:prevFP=%d", i, prevFP)
			}
		default:
			fmt.Fprintf(&sb, "%d:%d", i, v.num)
		}
	}
	sb.WriteByte(']')
	return sb.String()
}

func (m *Machine) traceStep(pos Pos, fp int, mem []Value) {
	if pos == 0 || pos == m.traceLastPos {
		return
	}
	m.traceLastPos = pos
	di := m.traceDI
	if di == nil {
		di = m.DebugInfo()
		if di == nil {
			return
		}
		m.traceDI = di
	}
	file, line, _ := di.Sources.Resolve(int(pos))
	if file == "" {
		return
	}
	if file == m.traceLastFile && line == m.traceLastLine {
		return
	}
	m.traceLastFile, m.traceLastLine = file, line
	_, _ = fmt.Fprintf(m.err, "+ %s%s:%d: %s\n", traceIndent(mem, fp), file, line, di.Sources.LineText(int(pos)))
}

func (m *Machine) execConvert(c *Instruction, mem []Value, sp int) {
	idx := sp - int(c.B)
	v := mem[idx]
	dstType := m.globals[int(c.A)].ref.Type()
	dstKind := dstType.Kind()
	if !v.ref.IsValid() {
		// nil source: zero value of destination type.
		if dstKind != reflect.Interface {
			mem[idx] = FromReflect(reflect.Zero(dstType))
		}
		return
	}
	srcKind := v.ref.Type().Kind()

	switch {
	case isNum(srcKind) && isNum(dstKind):
		bits := v.num
		switch {
		case isFloat(srcKind) && isFloat(dstKind):
			// float32 -> float64 or float64 -> float32: re-precision.
			if srcKind != dstKind {
				f := math.Float64frombits(bits)
				if dstKind == reflect.Float32 {
					bits = math.Float64bits(float64(float32(f)))
				}
			}
		case isFloat(srcKind):
			// float -> int/uint: truncate. Route unsigned destinations
			// through uint64(f) directly: `int64(f)` is undefined for
			// values above MaxInt64 (clamps to MaxInt64 on amd64/arm64),
			// which would silently saturate uint64 results.
			f := math.Float64frombits(bits)
			if dstKind >= reflect.Uint && dstKind <= reflect.Uintptr {
				bits = uint64(f)
			} else {
				bits = uint64(int64(f))
			}
		case isFloat(dstKind):
			// int -> float.
			if srcKind >= reflect.Uint && srcKind <= reflect.Uintptr {
				bits = math.Float64bits(float64(bits))
			} else {
				bits = math.Float64bits(float64(int64(bits)))
			}
		}
		// Truncate to target width for sub-word types.
		switch dstKind {
		case reflect.Int:
			mem[idx] = Value{num: bits, ref: zint}
		case reflect.Int8:
			mem[idx] = Value{num: uint64(int8(bits)), ref: zint8}
		case reflect.Int16:
			mem[idx] = Value{num: uint64(int16(bits)), ref: zint16}
		case reflect.Int32:
			mem[idx] = Value{num: uint64(int32(bits)), ref: zint32}
		case reflect.Int64:
			mem[idx] = Value{num: bits, ref: zint64}
		case reflect.Uint:
			mem[idx] = Value{num: bits, ref: zuint}
		case reflect.Uint8:
			mem[idx] = Value{num: uint64(uint8(bits)), ref: zuint8}
		case reflect.Uint16:
			mem[idx] = Value{num: uint64(uint16(bits)), ref: zuint16}
		case reflect.Uint32:
			mem[idx] = Value{num: uint64(uint32(bits)), ref: zuint32}
		case reflect.Uint64:
			mem[idx] = Value{num: bits, ref: zuint64}
		case reflect.Uintptr:
			mem[idx] = Value{num: bits, ref: zuintptr}
		case reflect.Float32:
			mem[idx] = Value{num: math.Float64bits(float64(float32(math.Float64frombits(bits)))), ref: zfloat32}
		case reflect.Float64:
			mem[idx] = Value{num: bits, ref: zfloat64}
		}
		// Keep a defined numeric type's named rtype (e.g. `type Grams int` with
		// String): the canonical zXXX ref above dropped it, so a later box into
		// an interface would lose its methods.
		// Gated on NumMethod so plain numeric conversions keep the shared-ref
		// fast path.
		if dstType.NumMethod() > 0 {
			mem[idx].ref = reflect.Zero(dstType)
		}

	case isNum(srcKind) && (dstKind == reflect.Complex64 || dstKind == reflect.Complex128):
		// numeric -> complex (a constant conversion in Go; reflect.Convert
		// rejects int/float -> complex, so build it from the real part here).
		var re float64
		switch {
		case isFloat(srcKind):
			re = math.Float64frombits(v.num)
		case srcKind >= reflect.Uint && srcKind <= reflect.Uintptr:
			re = float64(v.num)
		default:
			re = float64(int64(v.num))
		}
		nv := reflect.New(dstType).Elem()
		nv.SetComplex(complex(re, 0))
		mem[idx] = Value{ref: nv}

	case isNum(srcKind) && dstKind == reflect.String:
		// int/rune -> string (e.g. string(65) -> "A").
		mem[idx] = Value{ref: reflect.ValueOf(string(rune(int64(v.num))))}

	case srcKind == reflect.String && dstKind == reflect.Slice && dstType.Elem().Kind() == reflect.Uint8:
		// string -> []byte, or a named type over []byte (e.g. json.RawMessage):
		// keep the destination's named rtype so a later box into interface{}
		// dispatches its methods / matches its own type-switch case.
		b := reflect.ValueOf([]byte(v.ref.String()))
		if b.Type() != dstType {
			b = b.Convert(dstType)
		}
		mem[idx] = Value{ref: b}

	case srcKind == reflect.Slice && v.ref.Type().Elem().Kind() == reflect.Uint8 && dstKind == reflect.String:
		// []byte -> string.
		mem[idx] = Value{ref: reflect.ValueOf(string(v.ref.Bytes()))}

	case dstKind == reflect.UnsafePointer &&
		(srcKind == reflect.Pointer || srcKind == reflect.UnsafePointer || srcKind == reflect.Uintptr):
		// *T, unsafe.Pointer, or uintptr -> unsafe.Pointer.
		// reflect.Value.Convert has no convertOp for UnsafePointer, so
		// we build the destination value manually.
		var up unsafe.Pointer
		switch srcKind {
		case reflect.Pointer, reflect.UnsafePointer:
			up = v.ref.UnsafePointer()
		case reflect.Uintptr:
			up = runtype.PointerFromUintptr(uintptr(v.num))
		}
		nv := reflect.New(dstType).Elem()
		nv.SetPointer(up)
		mem[idx] = Value{ref: nv}

	case srcKind == reflect.UnsafePointer &&
		(dstKind == reflect.Pointer || dstKind == reflect.Uintptr):
		// unsafe.Pointer -> *T or uintptr.
		up := v.ref.UnsafePointer()
		if dstKind == reflect.Uintptr {
			mem[idx] = Value{num: uint64(uintptr(up)), ref: reflect.Zero(dstType)}
		} else {
			mem[idx] = FromReflect(reflect.NewAt(dstType.Elem(), up))
		}

	case srcKind == reflect.Pointer && dstKind == reflect.Pointer:
		// Convert when possible: the NewAt path below rebuilds an unnamed
		// *elem, losing a named pointer type's identity (`type P *T`).
		if v.ref.Type().ConvertibleTo(dstType) {
			mem[idx] = FromReflect(v.ref.Convert(dstType))
			return
		}
		// *T -> *U via unsafe reinterpretation.
		// reflect.Value.Convert for two pointer types requires
		// name-identity on the elem types (haveIdenticalType compares
		// nameFor strictly), so it rejects conversions Go's spec allows
		// -- e.g., `(*ipNetValue)(p)` where p is *net.IPNet and
		// ipNetValue is `type ipNetValue net.IPNet`.  reflect.NewAt with
		// the same underlying pointer matches Go's language-level
		// convertibility for unnamed-pointer types.
		mem[idx] = FromReflect(reflect.NewAt(dstType.Elem(), v.ref.UnsafePointer()))

	default:
		// Fallback: use reflect.
		mem[idx] = FromReflect(v.Reflect().Convert(dstType))
	}
}

func (m *Machine) handleTrap(ip, fp, sp int, mem []Value) (int, int, int, []Value) {
	m.trapOrig = ip + 1
	mem = mem[:sp+1]
	m.mem, m.ip, m.fp = mem, m.trapOrig, fp
	m.enterDebug()
	mem, ip, fp = m.mem, m.ip, m.fp
	sp = len(mem) - 1
	mem = mem[:cap(mem)]
	return ip, fp, sp, mem
}

func derefCell(v Value) Value {
	if v.ref.IsValid() && v.ref.Kind() == reflect.Pointer {
		if pv, ok := v.ref.Interface().(*Value); ok {
			return *pv
		}
	}
	return v
}

// finalizeReturns copies the nret result values from a function's fixed
// named-return slots (mem[ofp+0..nret-1]) into the caller's return area at
// newBase, dereferencing cells. Results are registered right-to-left, so
// result i is at slot nret-i (mem[ofp+nret-1-i]). A temp avoids source/dest
// overlap; the stack array covers the common small-nret case allocation-free.
func finalizeReturns(mem []Value, ofp, newBase, nret int) {
	if nret == 0 {
		return
	}
	var tmp [8]Value
	var ret []Value
	if nret <= len(tmp) {
		ret = tmp[:nret]
	} else {
		ret = make([]Value, nret)
	}
	for i := range nret {
		ret[i] = derefCell(mem[ofp+nret-1-i])
	}
	copy(mem[newBase:newBase+nret], ret)
}

func (m *Machine) handleRecover(fp, sp int, mem []Value, deferRetAddr int) (int, []Value) {
	if m.panicking && int(int32(mem[fp-2].num)) == deferRetAddr {
		m.panicking = false
		pv := m.panicVal
		if pv.IsValid() && !pv.IsIface() {
			rt := pv.Reflect().Type()
			typ := &Type{Name: rt.Name(), Rtype: rt}
			pv = Value{ref: reflect.ValueOf(Iface{Typ: typ, Val: pv})}
		}
		if sp+1 >= len(mem) {
			mem = growStack(mem, sp, 1)
		}
		sp++
		mem[sp] = pv
		m.panicVal = Value{}
		return sp, mem
	}
	if sp+1 >= len(mem) {
		mem = growStack(mem, sp, 1)
	}
	sp++
	mem[sp] = Value{}
	return sp, mem
}

func (m *Machine) recoverPanic(err *error) {
	if r := recover(); r != nil {
		if pe, ok := asPanicError(r); ok {
			*err = pe
			return
		}
		// A *reflect.ValueError is genuine crash (the VM read a corrupted Value),
		// but it is not a runtime.Error, so capture it explicitly.
		var ve *reflect.ValueError
		if e, ok := r.(error); ok && !errors.As(e, &ve) {
			if _, isRuntimeErr := r.(runtime.Error); !isRuntimeErr {
				*err = e
				return
			}
		}
		*err = m.capturePanic(r)
	}
}

func (m *Machine) posPrefix(pos Pos) string {
	if m.debugInfoFn == nil {
		return ""
	}
	if loc := m.debugInfoFn().PosToLine(pos); loc != "" {
		return loc + ": "
	}
	return ""
}

const heapSavedFlag = uint64(1) << 63

// CallSpreadFlag is set in the B operand of Call to indicate a spread call
// (f(s...)), so the VM uses reflect.CallSlice instead of reflect.Call for
// native variadic functions.
const CallSpreadFlag int32 = 1 << 15

// nilFuncAddr is the resolved code address of a nil/unresolved func value.
// Real functions are never placed at code index 0 -- that slot holds the
// program-entry Jump emitted by the first Eval -- so a call target of 0 means
// the func value was nil (or, for the *Imm variants, a corrupt global slot).
// Calling it panics with Go's nil-func deref rather than jumping there, which
// would re-run the program/_testmain and recurse without bound.
const nilFuncAddr = 0

// retIPInfo word layout: retIP in bits 0..31, nret in bits 32..46 (retNretMask),
// namedRetFlag in bit 47, frameBase in bits 48..63. namedRetFlag is set by
// MarkNamedRet for functions with captured named returns and tells Return and
// panicUnwind to finalize results from the fixed named-return slots (deref
// cells) after defers.
const (
	retNretMask         = 0x7FFF
	namedRetFlag uint64 = 1 << 47
)

func packRetIP(retIP, nret, frameBase int) uint64 {
	return uint64(uint32(retIP)) | uint64(nret&retNretMask)<<32 | uint64(frameBase)<<48
}

// A defer entry's mem[dh-2] slot packs isX (bits 0..1: 0 VM, 1 native, 2
// builtin), narg (bits 2..62), and deferStartedFlag (bit 63), via the helpers
// below. The flag lets panicUnwind skip an already-dispatched defer (Go's
// _defer.started) rather than re-run one whose body panicked before deferRet.
const deferStartedFlag = uint64(1) << 63

// DeferSpreadFlag marks that the call's final argument is a variadic slice, so
// native defers use reflect.CallSlice. Set in the DeferPush B operand alongside
// isX. Defer header layout: isX bits 0-1, spread bit 2, narg bits 3-62, started 63.
const DeferSpreadFlag = 4

// IfaceCallDetachBit in an IfaceCall's B operand (set by the compiler for
// defer/go) makes the native path capture the receiver by value, not alias its
// slot. Low bits stay the recvTypeHint.
const IfaceCallDetachBit = 1 << 30

func packDefer(narg, isX, spread int) uint64 { return uint64(narg<<3 | spread<<2 | isX) }
func deferNarg(packed uint64) int            { return int((packed &^ deferStartedFlag) >> 3) }
func deferIsX(packed uint64) int             { return int(packed & 3) }
func deferSpread(packed uint64) bool         { return packed&DeferSpreadFlag != 0 }

func growStack(mem []Value, sp, need int) []Value {
	n := max(len(mem)*2, sp+1+need+256)
	newMem := make([]Value, n)
	copy(newMem, mem[:sp+1])
	return newMem
}

// Run runs a program.
func (m *Machine) Run() (err error) {
	// Outermost defer (runs last in LIFO order): catches raw Go panics that
	// escape the VM loop (e.g. reflect.Convert) and wraps them with mvm
	// source context. Declared before the state-restore defer below so the
	// state-restore runs first and m.mem/m.ip/m.fp hold panic-time values
	// when capturePanic reads them.
	defer m.recoverPanic(&err)
	prev := SetActiveMachine(m)
	defer SetActiveMachine(prev)

	// Isolate this Run's range iterators from an outer (re-entrant) Run's:
	// raise the floor, then on exit drop any this Run leaked and restore it.
	savedIterBase := m.iterBase
	runIterBase := len(m.iterStack)
	m.iterBase = runIterBase
	defer func() {
		m.iterStack = m.iterStack[:runIterBase]
		m.iterBase = savedIterBase
	}()

	// Append sentinel instructions so negative-IP handlers become normal opcodes.
	// Save baseCodeLen too: a re-entrant Run (CallFunc from a native callback)
	// overwrites it, and panicAddr() reads it freshly, so leaving the inner value
	// would point stageUnwind at a sentinel that the code trim below removed.
	// A pooledCode machine (trampBase != 0) has the sentinels baked in at
	// trampBase and must not append: its code is shared read-only.
	savedBaseCodeLen := m.baseCodeLen
	sentBase := m.trampBase
	sentAppended := false
	if sentBase == 0 {
		sentBase = len(m.code)
		m.code = append(m.code, Instruction{Op: DeferRet}, Instruction{Op: PanicUnwind}, Instruction{Op: Exit})
		sentAppended = true
	}
	m.baseCodeLen = sentBase
	deferRetAddr := sentBase
	panicAddr := m.panicAddr()
	deferRetBits := uint64(deferRetAddr)

	// Hoist trace flags into a register-resident local so the hot-loop check
	// is a single compare-against-zero on a register. Toggles via
	// SetTracing/SetTraceOps don't take effect until the next Run() entry.
	traceFlags := m.traceFlags

	defer func() {
		if sentAppended {
			m.code = m.code[:sentBase]
		}
		m.baseCodeLen = savedBaseCodeLen
	}()

	// Re-enter the dispatch loop each time it converts a catchable runtime
	// panic from an interpreted opcode into an mvm panic (done=false: m.ip
	// was staged at the unwind sentinel, so defers and recover() run).
	for {
		done, lerr := m.runLoop(traceFlags, panicAddr, deferRetAddr, deferRetBits)
		if done {
			return lerr
		}
	}
}

// opPanicCatchable reports whether a Go panic raised by an interpreted opcode
// (not a native call) is a user-level runtime panic that Go programs can
// recover: index/slice bounds, divide by zero, and reflect's equivalents for
// container ops. VM-internal faults (reflect.ValueError, nil derefs inside VM
// code) stay uncatchable so they surface as crashes, not as program panics.
func opPanicCatchable(r any) bool {
	switch v := r.(type) {
	case string:
		// Index ops panic "reflect: ... out of range"; Slice/Slice3 panic
		// "reflect.Value.Slice: slice index out of bounds" (a different prefix).
		return (strings.HasPrefix(v, "reflect:") || strings.HasPrefix(v, "reflect.Value.Slice")) &&
			(strings.Contains(v, "out of range") || strings.Contains(v, "out of bounds") || strings.Contains(v, "slice bounds"))
	case runtime.Error:
		s := v.Error()
		return strings.Contains(s, "index out of range") ||
			strings.Contains(s, "slice bounds out of range") ||
			strings.Contains(s, "divide by zero")
	}
	return false
}

// opRuntimeError is the recovered value for a reflect-raised opcode panic,
// shaped like gc's runtime.Error ("runtime error: ..." message).
type opRuntimeError string

func (e opRuntimeError) Error() string { return "runtime error: " + string(e) }
func (e opRuntimeError) RuntimeError() {}

// normalizeOpPanic maps a reflect string panic (index ops' "reflect: slice
// index out of range" or Slice's "reflect.Value.Slice: ... out of bounds") to
// gc's runtime.Error shape; genuine runtime.Error values (e.g. divide by zero)
// pass through unchanged.
func normalizeOpPanic(r any) any {
	s, ok := r.(string)
	if !ok {
		return r
	}
	if strings.Contains(s, "slice bounds") || strings.Contains(s, "out of bounds") {
		return opRuntimeError("slice bounds out of range")
	}
	return opRuntimeError("index out of range")
}

// runLoop executes the dispatch loop until program exit or an unrecovered
// panic (done=true). A catchable Go runtime panic from an interpreted op is
// converted into an mvm panic at the faulting instruction (done=false), so an
// interpreted recover() catches it like a native-call panic (see invokeNative).
func (m *Machine) runLoop(traceFlags uint8, panicAddr, deferRetAddr int, deferRetBits uint64) (done bool, err error) {
	mem, ip, fp := m.mem, m.ip, m.fp
	sp := len(mem) - 1
	// Extend mem to full capacity so all writes up to cap are in bounds.
	mem = mem[:cap(mem)]
	var safepointCtr uint // throttles the interrupt poll on the call opcodes

	defer func() {
		m.mem, m.ip, m.fp = mem[:sp+1], ip, fp
		if r := recover(); r != nil {
			if !opPanicCatchable(r) {
				panic(r)
			}
			m.panicking = true
			m.panicVal = FromReflect(reflect.ValueOf(normalizeOpPanic(r)))
			m.ip = m.stageUnwindAt(ip, fp, mem)
		}
	}()

	for {
		c := &m.code[ip] // current instruction (pointer avoids 16-byte struct copy per iter)
		if traceFlags != 0 {
			if traceFlags&traceFlagLine != 0 {
				m.traceStep(c.Pos, fp, mem)
			}
			if traceFlags&traceFlagOp != 0 {
				m.traceOp(ip, fp, c, mem, sp)
			}
		}
		switch c.Op {
		case Addr:
			v := mem[sp]
			switch {
			case v.ref.IsValid() && v.ref.Type() == ifaceRtype:
				// An mvm Iface struct is not a Go eface, so &iface for unsafe access
				// (protobuf's pointerOfIface) must alias a real *interface{}. Before
				// CanAddr since an addressable Iface struct has the same wrong layout.
				// Struct form only: a real interface{} var keeps CanAddr below so writes
				// through &iface propagate.
				r := reflect.New(AnyRtype)
				if cv := v.IfaceVal().Val.Reflect(); cv.IsValid() {
					r.Elem().Set(cv)
				}
				mem[sp] = Value{ref: r}
			case v.ref.CanAddr():
				mem[sp] = Value{ref: v.ref.Addr()}
			case isNum(v.ref.Kind()):
				// Materialize via Reflect() to get an addressable value, then take its address.
				mem[sp] = Value{ref: v.Reflect().Addr()}
			case v.IsIface():
				// Non-addressable interface{} holding an Iface: no slot to alias.
				r := reflect.New(AnyRtype)
				r.Elem().Set(v.IfaceVal().Val.Reflect())
				mem[sp] = Value{ref: r}
			case !v.ref.IsValid():
				// Nil interface parameter: allocate *interface{} with zero value.
				mem[sp] = Value{ref: reflect.New(AnyRtype)}
			default:
				// Non-numeric, non-addressable composite (e.g. string parameter):
				// allocate addressable storage and copy.
				r := reflect.New(v.ref.Type())
				r.Elem().Set(v.ref)
				mem[sp] = Value{ref: r}
			}
		case SetLocal:
			m.assignSlot(&mem[fp-1+int(c.A)], mem[sp])
			sp--
		case SetGlobal:
			m.assignSlot(&m.globals[int(c.A)], mem[sp])
			sp--
		case Call:
			// State-dump safepoint: covers recursion that never hits a back-edge.
			if dumpRequested.Load() {
				m.handleDumpReq(ip, fp, mem)
			}
			// Interrupt poll, gated so mvm run (no hook) pays only a predicted
			// branch; off the loop back-edge so pure compute loops are unaffected.
			if safepointHook != nil {
				m.callSafepoint(ip, fp, mem, &safepointCtr)
			}
			narg := int(c.A)
			fval := mem[sp-narg]
			// Inline fast path: only call resolveFuncField for addressable Func fields.
			if fval.ref.Kind() == reflect.Func && fval.ref.CanAddr() {
				fval = m.resolveFuncField(fval)
			}
			fval.ref = Exportable(fval.ref)
			prevHeap := m.heap
			var nip int
			// fval.ref may be non-addressable AND read-only (method value
			// taken off an unexported struct field, which make .Interface() panics).
			canCallInterface := fval.ref.IsValid() && fval.ref.CanInterface()
			var clo Closure
			var isClosure, isInt bool
			var iv int
			if canCallInterface {
				clo, isClosure = fval.ref.Interface().(Closure)
				if !isClosure {
					iv, isInt = fval.ref.Interface().(int)
				}
			}
			// Cannot use switch here: the final else branch contains a `break`
			// that must exit the outer Op switch (native func call returns
			// values directly without setting up a call frame).
			if isNum(fval.ref.Kind()) { //nolint:gocritic
				// Plain int code address stored inline in num.
				nip = int(fval.num)
				m.heap = nil
			} else if isClosure {
				nip = clo.Code
				m.heap = clo.Heap
			} else if isInt {
				// Function variable slot holds a plain code address boxed as interface{}.
				nip = iv
				m.heap = nil
			} else {
				rv := fval.ref
				// Method-call sentinel: IfaceCall placed a boundHookCall on
				// the stack because the target method has a registered
				// NativeMethodHook. Unwrap it and thread (RecvType, Method,
				// Recv) to the hook lookup further down.
				var hookRecvType reflect.Type
				var hookMethod string
				var hookRecv reflect.Value
				if rv.IsValid() && rv.Type() == boundHookCallRtype {
					bhc := rv.Interface().(boundHookCall)
					rv = bhc.Fn
					hookRecvType = bhc.RecvType
					hookMethod = bhc.Method
					hookRecv = bhc.Recv
				}
				if rv.Kind() == reflect.Interface && !rv.IsNil() {
					rv = rv.Elem()
				}
				rv = Exportable(rv)
				if codeAddr, ok := m.methodExprBypass(rv, narg >= 1 && c.B&CallSpreadFlag == 0); ok {
					// Dispatch a method expression T.M through the interpreter:
					// bind the first arg as the receiver heap cell, drop it from
					// the arg list, then fall through to the closure frame setup.
					recv := mem[sp-narg+1]
					if k := recv.ref.Kind(); k == reflect.Struct || k == reflect.Array {
						nv := reflect.New(recv.ref.Type()).Elem() // value receiver gets a copy
						nv.Set(Exportable(recv.ref))
						recv.ref = nv
					}
					cell := new(Value)
					*cell = recv
					copy(mem[sp-narg+1:sp], mem[sp-narg+2:sp+1]) // shift args over receiver slot
					mem[sp] = Value{}
					sp--
					narg--
					m.heap = []*Value{cell}
					nip = codeAddr
				} else if rv.Kind() == reflect.Func {
					funcType := rv.Type()
					in := make([]reflect.Value, narg)
					for i := range in {
						in[i] = mem[sp-narg+1+i].Reflect()
					}
					ifaceWB := m.bridgeArgs(in, funcType, rv)
					coerceInterfaceArgs(in, funcType)
					m.wrapFuncArgs(in, mem[sp-narg+1:sp+1], funcType)
					sp -= narg + 1
					// Sync mem/fp/ip into the Machine so native funcs that
					// introspect the interpreter (e.g. the runtime.Callers
					// bridge) see live state. The local fp/ip remain
					// authoritative; this is a one-way push.
					m.mem, m.fp, m.ip = mem, fp, ip+1
					// Invoke the native func/method, converting any Go panic
					// into an mvm panic so an interpreted recover() can catch it.
					hook := lookupNativeMethodHook(hookRecvType, hookMethod)
					out, panicked := m.invokeNative(hook, hookRecv, rv, in, c.B&CallSpreadFlag != 0)
					if panicked {
						ip = m.stageUnwind(ip, fp, mem)
						continue
					}
					if ifaceWB != nil {
						m.normalizeIfaceWritebacks(ifaceWB)
					}
					for _, v := range out {
						if sp+1 >= len(mem) {
							mem = growStack(mem, sp, 1)
						}
						sp++
						mem[sp] = FromReflect(v)
					}
					break
				} else {
					nip = int(fval.num)
					m.heap = nil
				}
			}
			if nip == nilFuncAddr {
				m.raiseNilDeref()
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			nret := int(c.B &^ CallSpreadFlag)
			fpVal := uint64(fp)
			if prevHeap != nil {
				m.heapFrames = append(m.heapFrames, prevHeap)
				fpVal |= heapSavedFlag
			}
			// Inline the callee's leading Grow (when present); see CallImm.
			var locals, slack int
			if g := m.code[nip]; g.Op == Grow {
				locals, slack = int(g.A), int(g.B)
				nip++
			}
			if sp+3+locals+slack >= len(mem) {
				mem = growStack(mem, sp, 3+locals+slack)
			}
			mem[sp+1] = Value{}
			mem[sp+2] = Value{num: packRetIP(ip+1, nret, narg+4)}
			mem[sp+3] = Value{num: fpVal}
			sp += 3 // deferHead, retIP+info, prevFP+heapFlag
			fp = sp + 1
			for i := 1; i <= locals; i++ {
				mem[sp+i] = Value{}
			}
			sp += locals
			if narg > 0 {
				detachByValueArgs(mem[fp-narg-3 : fp-3])
			}
			ip = nip
			continue
		case CallImm:
			if safepointHook != nil {
				m.callSafepoint(ip, fp, mem, &safepointCtr)
			}
			narg := int(c.B) >> 16
			nret := int(c.B) & 0xFFFF
			fpVal := uint64(fp)
			if m.heap != nil {
				// preserve caller closure context
				m.heapFrames = append(m.heapFrames, m.heap)
				fpVal |= heapSavedFlag
				m.heap = nil
			}
			nip := int(m.globals[int(c.A)].num)
			if nip == nilFuncAddr { // defense in depth: nil/corrupt global slot
				m.raiseNilDeref()
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			// Inline the callee's leading Grow (when present) to save one
			// dispatch per call and combine its bounds check with our own.
			var locals, slack int
			if g := m.code[nip]; g.Op == Grow {
				locals, slack = int(g.A), int(g.B)
				nip++
			}
			if sp+3+locals+slack >= len(mem) {
				mem = growStack(mem, sp, 3+locals+slack)
			}
			mem[sp+1] = Value{} // clear deferHead slot
			mem[sp+2] = Value{num: packRetIP(ip+1, nret, narg+3)}
			mem[sp+3] = Value{num: fpVal}
			sp += 3
			fp = sp + 1
			for i := 1; i <= locals; i++ {
				mem[sp+i] = Value{}
			}
			sp += locals
			if narg > 0 {
				detachByValueArgs(mem[fp-narg-3 : fp-3])
			}
			ip = nip
			continue
		case CallImmFast:
			// Fast-path CallImm: emitted when no callee param has reflect Kind
			// Struct or Array, so detachByValueArgs would be a no-op.
			if safepointHook != nil {
				m.callSafepoint(ip, fp, mem, &safepointCtr)
			}
			narg := int(c.B) >> 16
			nret := int(c.B) & 0xFFFF
			fpVal := uint64(fp)
			if m.heap != nil {
				m.heapFrames = append(m.heapFrames, m.heap)
				fpVal |= heapSavedFlag
				m.heap = nil
			}
			nip := int(m.globals[int(c.A)].num)
			if nip == nilFuncAddr { // defense in depth: nil/corrupt global slot
				m.raiseNilDeref()
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			var locals, slack int
			if g := m.code[nip]; g.Op == Grow {
				locals, slack = int(g.A), int(g.B)
				nip++
			}
			if sp+3+locals+slack >= len(mem) {
				mem = growStack(mem, sp, 3+locals+slack)
			}
			mem[sp+1] = Value{}
			mem[sp+2] = Value{num: packRetIP(ip+1, nret, narg+3)}
			mem[sp+3] = Value{num: fpVal}
			sp += 3
			fp = sp + 1
			for i := 1; i <= locals; i++ {
				mem[sp+i] = Value{}
			}
			sp += locals
			ip = nip
			continue
		case Deref:
			r := mem[sp].ref.Elem()
			if !r.IsValid() {
				// *nil: recoverable nil deref, not a raw reflect panic.
				m.raiseNilDeref()
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			v := Value{ref: r}
			if isNum(r.Kind()) {
				v.num = numBits(r)
			}
			mem[sp] = v
		case DerefSet:
			ptr := mem[sp-1]
			val := mem[sp]
			elem := ptr.ref.Elem()
			sp -= 2
			if !elem.IsValid() {
				// *nil = v: recoverable nil deref, not a raw reflect panic.
				m.raiseNilDeref()
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			if elem.Kind() == reflect.Func && elem.CanAddr() {
				// *p = f: wrap the mvm func value into a Go func (reflect.Set
				// rejects the Closure/code-address representation).
				m.setFuncField(elem, val)
				break
			}
			numSet(elem, val)
			// Update the .num cache of any stack slot whose ref shares the
			// same underlying address, so fused GetLocal*Imm instructions and
			// num-first reads see the updated value. Scan ALL frames on the
			// stack, not just the current one: a pointer-receiver method that
			// mutates `*v` must propagate to the caller's slot (the typical
			// `(*X).Set` pattern on `type X int`).
			if isNum(elem.Kind()) {
				addr := elem.UnsafeAddr()
				n := numBits(elem)
				for i := 0; i <= sp; i++ {
					if mem[i].ref.IsValid() && mem[i].ref.CanAddr() && mem[i].ref.UnsafeAddr() == addr {
						mem[i].num = n
					}
				}
			}
		case AddrLocal:
			slot := &mem[int(c.A)+fp-1]
			switch {
			case c.B != 0:
				// Func slot is an interface{} box; retype to the declared func
				// type ($2-1) so &f is *func(...), not *interface{}.
				m.retypeFuncSlot(slot, m.globals[int(c.B)-1].ref.Type())
			case !slot.ref.CanAddr():
				// Promote to addressable storage so the pushed pointer aliases
				// the slot. DerefSet keeps slot.num in sync on writes.
				rt := slot.ref.Type()
				rv := reflect.New(rt).Elem()
				if isNum(rt.Kind()) {
					setNumReflect(rv, slot.num)
				} else {
					rv.Set(slot.ref)
				}
				slot.ref = rv
			}
			sp++
			mem[sp] = Value{ref: slot.ref.Addr()}
		case GetLocal:
			sp++
			mem[sp] = mem[int(c.A)+fp-1]
		case GetLocal2:
			mem[sp+1] = mem[int(c.A)+fp-1]
			mem[sp+2] = mem[int(c.B)+fp-1]
			sp += 2
		case GetLocalSync:
			// GetLocal variant emitted after AddrLocal for the same slot:
			// the local was promoted to addressable storage, so a native
			// callee writing through the pushed pointer bypasses slot.num.
			// Re-read num from ref so subsequent reads see native writes
			// (e.g. flag.BoolVar(&b, ...) + Parse must update `b`).
			sp++
			mem[sp] = mem[int(c.A)+fp-1]
			if isNum(mem[sp].ref.Kind()) {
				mem[sp].num = numBits(mem[sp].ref)
			}
		case GetLocalAddIntImm:
			sp++
			v := mem[int(c.A)+fp-1]
			v.num = uint64(int(v.num) + int(c.B))
			v.ref = zint
			mem[sp] = v
		case GetLocalSubIntImm:
			sp++
			v := mem[int(c.A)+fp-1]
			v.num = uint64(int(v.num) - int(c.B))
			v.ref = zint
			mem[sp] = v
		case GetLocalMulIntImm:
			sp++
			v := mem[int(c.A)+fp-1]
			v.num = uint64(int(v.num) * int(c.B))
			v.ref = zint
			mem[sp] = v
		case GetLocalLowerIntImm:
			sp++
			mem[sp] = boolVal(int(mem[int(c.A)+fp-1].num) < int(c.B))
		case GetLocalLowerUintImm:
			sp++
			mem[sp] = boolVal(uint(mem[int(c.A)+fp-1].num) < uint(int(c.B)))
		case GetLocalGreaterIntImm:
			sp++
			mem[sp] = boolVal(int(mem[int(c.A)+fp-1].num) > int(c.B))
		case GetLocalGreaterUintImm:
			sp++
			mem[sp] = boolVal(uint(mem[int(c.A)+fp-1].num) > uint(int(c.B)))
		case GetLocalReturn:
			sp++
			mem[sp] = mem[int(c.A)+fp-1]
			retIPInfo := mem[fp-2].num
			frameBase := int(retIPInfo >> 48)
			ip = int(int32(retIPInfo))
			ofp := fp
			if len(m.iterStack) != 0 {
				m.dropIterFrames(ofp) // early return out of a range loop skips Stop
			}
			fpVal := mem[fp-1].num
			if fpVal&heapSavedFlag != 0 {
				fp = int(fpVal &^ heapSavedFlag)
				top := len(m.heapFrames) - 1
				m.heap = m.heapFrames[top]
				m.heapFrames[top] = nil // clear for GC
				m.heapFrames = m.heapFrames[:top]
			} else {
				fp = int(fpVal)
				m.heap = nil
			}
			newBase := ofp - frameBase
			nret := int((retIPInfo >> 32) & retNretMask)
			switch nret {
			case 0:
			case 1:
				mem[newBase] = mem[sp]
			default:
				copy(mem[newBase:], mem[sp-nret+1:sp+1])
			}
			sp = newBase + nret - 1
			continue
		case LowerIntImmJumpFalse:
			sp--
			if int(mem[sp+1].num) >= int(c.B) {
				ip += int(c.A)
				continue
			}
		case LowerIntImmJumpTrue:
			sp--
			if int(mem[sp+1].num) < int(c.B) {
				ip += int(c.A)
				continue
			}
		case GetLocalLowerIntImmJumpFalse:
			if int(mem[int(int16(c.A))+fp-1].num) >= int(c.B) {
				ip += int(c.A >> 16)
				continue
			}
		case GetLocalLowerIntImmJumpTrue:
			if int(mem[int(int16(c.A))+fp-1].num) < int(c.B) {
				ip += int(c.A >> 16)
				continue
			}
		case AddLocalLocal:
			slot := &mem[int(c.A)+fp-1]
			n := uint64(int(slot.num) + int(mem[int(c.B)+fp-1].num))
			slot.num = n
			if isNum(slot.ref.Kind()) && slot.ref.CanSet() {
				setNumReflect(slot.ref, n)
			}
		case SubLocalLocal:
			slot := &mem[int(c.A)+fp-1]
			n := uint64(int(slot.num) - int(mem[int(c.B)+fp-1].num))
			slot.num = n
			if isNum(slot.ref.Kind()) && slot.ref.CanSet() {
				setNumReflect(slot.ref, n)
			}
		case AddLocalIntImm:
			slot := &mem[int(c.A)+fp-1]
			n := uint64(int(slot.num) + int(c.B))
			slot.num = n
			if isNum(slot.ref.Kind()) && slot.ref.CanSet() {
				setNumReflect(slot.ref, n)
			}
		case SubLocalIntImm:
			slot := &mem[int(c.A)+fp-1]
			n := uint64(int(slot.num) - int(c.B))
			slot.num = n
			if isNum(slot.ref.Kind()) && slot.ref.CanSet() {
				setNumReflect(slot.ref, n)
			}
		case IndexSetBool:
			idx := int(mem[sp].num)
			reflect.Indirect(mem[sp-1].ref).Index(idx).SetBool(c.A != 0)
			sp -= 2
		case GetGlobal:
			// Global slots written via SetS update ref through a shared pointer without
			// updating num in the original slot; sync num from ref before copying.
			v := m.globals[int(c.A)]
			if isNum(v.ref.Kind()) && v.ref.CanAddr() {
				v.num = numBits(v.ref)
			}
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = v
		case Get:
			if int(c.A) == Local {
				if sp+1 >= len(mem) {
					mem = growStack(mem, sp, 1)
				}
				sp++
				mem[sp] = mem[int(c.B)+fp-1]
			} else {
				v := m.globals[int(c.B)]
				if isNum(v.ref.Kind()) && v.ref.CanAddr() {
					v.num = numBits(v.ref)
				}
				if sp+1 >= len(mem) {
					mem = growStack(mem, sp, 1)
				}
				sp++
				mem[sp] = v
			}
		case New:
			typ := m.globals[int(c.B)].ref.Type()
			if isNum(typ.Kind()) {
				// Non-addressable backing: slot.num is authoritative; slot.ref
				// is a typed zero used only for Kind/Type. AddrLocal promotes
				// to addressable on demand (vm.go:1164). Lets the in-place
				// super-instructions skip setNumReflect when the local is not
				// address-taken (the common case).
				mem[int(c.A)+fp-1] = Value{ref: reflect.Zero(typ)}
			} else {
				mem[int(c.A)+fp-1] = NewValue(typ)
			}
		case Equal:
			mem[sp-1] = boolVal(mem[sp-1].Equal(mem[sp]))
			sp--
		case EqualSet:
			if mem[sp-1].Equal(mem[sp]) {
				// If equal then lhs and rhs are popped, replaced by test result, as in Equal.
				mem[sp-1] = boolVal(true)
				sp--
			} else {
				// If not equal then the lhs is let on stack for further processing.
				// This is used to simplify bytecode in case clauses of switch statments.
				mem[sp] = boolVal(false)
			}
		case Convert:
			m.execConvert(c, mem, sp)

		case IfaceWrap:
			typ := m.globals[int(c.A)].ref.Interface().(*Type)
			idx := sp - int(c.B)
			v := mem[idx]
			// Assigning a value to an interface copies it (Go spec).
			// Without this clone, the wrapped Iface.Val.ref would alias the source
			// slot's storage and later mutations to that slot would leak through
			// the interface (e.g. compact.Make stores `tag.full = t` and a caller
			// mutating t later would see the change inside tag.full).
			// Struct/array always copy; other kinds (map/slice headers in an
			// addressable local cell) copy when the slot is writable, else a
			// later reassignment of the local mutates the boxed value.
			// Numeric kinds are exempt: their payload is snapshotted in v.num
			// (ref is type metadata), so cloning would only waste an alloc.
			if v.ref.IsValid() {
				if k := v.ref.Kind(); k == reflect.Struct || k == reflect.Array || (!isNum(k) && v.ref.CanSet()) {
					nv := reflect.New(v.ref.Type()).Elem()
					nv.Set(Exportable(v.ref))
					v.ref = nv
				}
			}
			mem[idx] = Value{ref: reflect.ValueOf(Iface{Typ: typ, Val: v})}

		case IfaceCall:
			methodID := int(c.A)
			if !mem[sp].IsIface() {
				// A native value whose concrete rtype maps back to an
				// interpreted *Type carrying a compiled method (e.g. an
				// interpreted struct round-tripped through encoding/gob into an
				// interface) must dispatch through that type's method -- the
				// StructOf rtype carries no native methods. Gate on the rtype
				// having no native method for this call: defined types over a
				// bridged stdlib struct (type ipNetValue net.IPNet) share that
				// struct's rtype, and re-wrapping a genuine native receiver
				// there would hijack the call back into the interpreted method
				// and recurse forever.
				if rv := mem[sp].Reflect(); rv.IsValid() {
					if rv.Kind() == reflect.Interface && !rv.IsNil() {
						rv = rv.Elem()
					}
					rt := rv.Type()
					// A synth rtype's native methods are stubs back into the
					// interpreter; prefer direct compiled-method dispatch. It
					// keeps an interface-typed result in mvm Iface form rather
					// than losing it in the stub's result marshaling (a synth
					// iface the concrete synth rtype does not implement).
					if !hasNativeMethod(rt, m.MethodNames[methodID]) || isSynthOrSynthPtr(rt) {
						if t := m.typeByRtype(rt); t != nil && t.ResolveMethodType(methodID) != nil {
							// FromReflect, not Value{ref: rv}: a numeric concrete (rv from
							// iface.Elem()) is non-addressable, so its data must go in num
							// or the value-receiver body reads a stale zero.
							mem[sp] = Value{ref: reflect.ValueOf(Iface{Typ: t, Val: FromReflect(rv)})}
						}
					}
				}
			}
			if !mem[sp].IsIface() {
				// Native interface value: use reflect to get the method.
				methodName := m.MethodNames[methodID]
				recvHint := c.B &^ IfaceCallDetachBit
				recvRV := mem[sp].Reflect()
				if isNilReceiver(recvRV) {
					m.raiseNilDeref()
					ip = m.stageUnwind(ip, fp, mem)
					continue
				}
				// defer/go: capture the receiver by value, not its slot.
				if c.B&IfaceCallDetachBit != 0 && recvRV.CanAddr() {
					cp := reflect.New(recvRV.Type()).Elem()
					cp.Set(Exportable(recvRV))
					recvRV = cp
				}
				rv := nativeMethodLookup(m, recvRV, methodName)
				if !rv.IsValid() && recvHint != 0 {
					// Numeric value lost its named type (e.g. time.Duration stored as int64).
					// Convert to the named type encoded in B-1 and retry the method lookup.
					namedType := m.globals[int(recvHint)-1].ref.Type()
					rv = recvRV.Convert(namedType).MethodByName(methodName)
				}
				if rv.IsValid() && recvRV.IsValid() &&
					hasNativeMethodHook(recvRV.Type(), methodName) {
					mem[sp] = Value{ref: reflect.ValueOf(boundHookCall{Fn: rv, RecvType: recvRV.Type(), Method: methodName, Recv: recvRV})}
					break
				}
				mem[sp] = Value{ref: rv}
				break
			}
			ifc := mem[sp].IfaceVal()
			// Pointer types share their value type's method set in Go: mvm
			// registers pointer-receiver methods on the value type, so *T's
			// Methods slice may be empty even when the method is resolved
			// on T. ResolveMethodType walks to ElemType when needed.
			methodTyp := ifc.Typ.ResolveMethodType(methodID)
			if methodTyp == nil {
				// Fall back to reflect-based dispatch when neither T nor *T
				// has a compiled method entry (native type in mvm interface).
				rv := ifc.Val.Reflect()
				if !rv.IsValid() {
					m.raiseNilDeref()
					ip = m.stageUnwind(ip, fp, mem)
					continue
				}
				mem[sp] = Value{ref: nativeMethodLookup(m, rv, m.MethodNames[methodID])}
				break
			}
			method := methodTyp.Methods[methodID]
			// Outcome of walking the embedded-interface chain at runtime:
			// chain = keep going to compiled-method dispatch; native = chain
			// terminated at a Go iface and was reflect-dispatched; nilRcv =
			// chain hit a nil interface field (Go-style panic).
			const (
				outChain = iota
				outNative
				outNilRcv
			)
			outcome := outChain
			for method.EmbedIface {
				rv := ifc.Val.Reflect()
				if rv.Kind() == reflect.Pointer {
					rv = rv.Elem()
				}
				nilHop := false
				for _, fi := range method.Path {
					if !rv.IsValid() {
						// nil pointer receiver: promoted access derefs nil.
						nilHop = true
						break
					}
					rv = rv.Field(fi)
				}
				if nilHop {
					outcome = outNilRcv
					break
				}
				embedded := FromReflect(rv)
				if !embedded.IsIface() {
					// Embedded field native-boxed (lost its Iface): re-wrap to the
					// interpreted *Type and continue, as the IfaceCall re-wrap above.
					erv := rv
					if erv.Kind() == reflect.Interface && !erv.IsNil() {
						erv = erv.Elem()
					}
					if erv.IsValid() &&
						(!hasNativeMethod(erv.Type(), m.MethodNames[methodID]) || isSynthOrSynthPtr(erv.Type())) {
						if t := m.typeByRtype(erv.Type()); t != nil {
							if mt := t.ResolveMethodType(methodID); mt != nil {
								methodTyp = mt
								ifc = Iface{Typ: t, Val: FromReflect(erv)}
								method = mt.Methods[methodID]
								continue
							}
						}
					}
					if isNilReceiver(rv) {
						outcome = outNilRcv
					} else {
						mem[sp] = Value{ref: nativeMethodLookup(m, rv, m.MethodNames[methodID])}
						outcome = outNative
					}
					break
				}
				ifc = embedded.IfaceVal()
				method = ifc.Typ.Methods[methodID]
			}
			if outcome == outNilRcv {
				m.raiseNilDeref()
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			if outcome == outNative {
				break
			}
			codeAddr := int(m.globals[method.Index].num)
			// Build a closure with the concrete receiver as Heap[0], replacing the
			// interface value on the stack. Same result as HeapAlloc+Get+Swap+MkClosure.
			// For promoted methods, extract the embedded field as receiver.
			cell := new(Value)
			*cell = ifc.Val
			if path := method.Path; path != nil {
				rv := reflect.Indirect(ifc.Val.Reflect())
				nilHop := false
				for _, idx := range path {
					if rv.Kind() == reflect.Pointer {
						rv = rv.Elem()
					}
					if !rv.IsValid() {
						// Promoted method on a nil pointer: navigating to the
						// embedded field derefs nil, a recoverable nil-pointer
						// dereference in Go, not a raw reflect Field panic.
						nilHop = true
						break
					}
					rv = rv.Field(idx)
				}
				if nilHop {
					m.raiseNilDeref()
					ip = m.stageUnwind(ip, fp, mem)
					continue
				}
				// A ptr-recv method needs the pointer in the cell: with the bare
				// struct, passing the receiver to an inner call detaches a copy.
				if method.PtrRecv && rv.Kind() != reflect.Pointer && rv.CanAddr() {
					rv = rv.Addr()
				}
				*cell = FromReflect(rv)
			} else if methodTyp == ifc.Typ.ElemType && !method.PtrRecv && ifc.Val.ref.Kind() == reflect.Pointer {
				// Value-receiver method found by walking *T -> T (ResolveMethodType).
				// The iface holds *T but the method body expects T; deref so the body's
				// receiver storage is the value, not the pointer. PtrRecv is reliable
				// here because the method was registered directly on the value type
				// (comp/compiler.go where it sets PtrRecv from the receiver token).
				*cell = FromReflect(ifc.Val.Reflect().Elem())
			}
			// A value receiver is a copy: detach struct/array cells so the body
			// gets an addressable receiver (an iface unbox is read-only) and the
			// caller stays unmutated, matching detachByValueArgs at Call.
			if !method.PtrRecv && cell.ref.IsValid() {
				if k := cell.ref.Kind(); k == reflect.Struct || k == reflect.Array {
					nv := reflect.New(cell.ref.Type()).Elem()
					nv.Set(Exportable(cell.ref))
					cell.ref = nv
				}
			}
			mem[sp] = Value{ref: reflect.ValueOf(Closure{Code: codeAddr, Heap: []*Value{cell}})}

		case TypeAssert:
			dstTyp := m.globals[int(c.A)].ref.Interface().(*Type)
			okForm := int(c.B) == 1
			ifc := mem[sp]
			if !ifc.IsIface() {
				// Native interface value: use reflect for type assertion.
				rv := ifc.Reflect()
				isNil := !rv.IsValid()
				ifaceTyp := AnyRtype
				if !isNil && rv.Kind() == reflect.Interface {
					ifaceTyp = rv.Type()
					isNil = rv.IsNil()
					if !isNil {
						rv = rv.Elem()
					}
				}
				// For interface targets, check the method set directly. AssignableTo
				// would yield a false positive when dstTyp.Rtype is AnyRtype (the
				// reflect placeholder for user-defined interfaces) since every type
				// is assignable to `interface{}`.
				matched := false
				var wrapTyp *Type // non-nil => wrap result as Iface for interpreted-method dispatch
				if !isNil {
					if dstTyp.IsInterface() {
						matched = dstTyp.NativeImplements(rv.Type())
					} else {
						dstRT := MaterializeRtype(dstTyp)
						matched = (dstRT != nil && rv.Type().AssignableTo(dstRT)) || dstTyp.NativeImplements(rv.Type())
					}
					// Interpreted concrete type round-tripped through native reflect (e.g.
					// reflect.Value.Interface()): its synthetic rtype carries no native
					// methods, so the checks above miss interpreted methods. Recover the
					// *Type and consult mvm's method tables (mirrors IfaceCall above).
					if !matched {
						if ct := m.typeByRtype(rv.Type()); ct != nil {
							if dstTyp.IsInterface() {
								if ct.Implements(dstTyp) {
									matched, wrapTyp = true, ct
								}
							} else if ct.SameAs(dstTyp) {
								matched = true
							}
						}
					}
					// Wrap an interface-target match in an Iface so a bare typed-nil
					// pointer keeps its type instead of collapsing to a nil interface.
					if matched && wrapTyp == nil && dstTyp.IsInterface() {
						if ct := m.typeByRtype(rv.Type()); ct != nil {
							wrapTyp = ct
						}
					}
				}
				if matched {
					if wrapTyp != nil {
						mem[sp] = Value{ref: reflect.ValueOf(Iface{Typ: wrapTyp, Val: FromReflect(rv)})}
					} else {
						mem[sp] = FromReflect(rv)
					}
					if okForm {
						if sp+1 >= len(mem) {
							mem = growStack(mem, sp, 1)
						}
						sp++
						mem[sp] = boolVal(true)
					}
					break
				}
				if !okForm {
					var msg string
					switch {
					case isNil:
						msg = fmt.Sprintf("interface conversion: %s is nil, not %s", ifaceTyp, dstTyp)
					case dstTyp.IsInterface():
						missing := dstTyp.MissingMethod(rv.Type())
						msg = fmt.Sprintf("interface conversion: %s is not %s: missing method %s", rv.Type(), dstTyp, missing)
					default:
						msg = fmt.Sprintf("interface conversion: %s is %s, not %s", ifaceTyp, rv.Type(), dstTyp)
					}
					m.panicking = true
					m.panicVal = Value{ref: reflect.ValueOf(m.posPrefix(c.Pos) + msg)}
					sp--
					ip = m.stageUnwind(ip, fp, mem)
					continue
				}
				mem[sp] = NewValue(dstTyp.Rtype)
				if sp+1 >= len(mem) {
					mem = growStack(mem, sp, 1)
				}
				sp++
				mem[sp] = boolVal(false)
				break
			}
			concrete := ifc.IfaceVal()
			var matched bool
			dstIsIface := dstTyp.IsInterface()
			if dstIsIface {
				matched = concrete.Typ.Implements(dstTyp)
			} else {
				matched = concrete.Typ.SameAs(dstTyp)
			}
			if matched {
				// For interface targets, keep the Iface wrapping so IfaceCall still works.
				result := concrete.Val
				if dstIsIface {
					result = ifc
				}
				if okForm {
					mem[sp] = result
					if sp+1 >= len(mem) {
						mem = growStack(mem, sp, 1)
					}
					sp++
					mem[sp] = boolVal(true)
				} else {
					mem[sp] = result
				}
			} else {
				if !okForm {
					var msg string
					if dstIsIface {
						missing := dstTyp.MissingMethod(concrete.Typ.Rtype)
						msg = fmt.Sprintf("interface conversion: %s is not %s: missing method %s", concrete.Typ, dstTyp, missing)
					} else {
						msg = fmt.Sprintf("interface conversion: %s is %s, not %s", AnyRtype, concrete.Typ, dstTyp)
					}
					m.panicking = true
					m.panicVal = Value{ref: reflect.ValueOf(m.posPrefix(c.Pos) + msg)}
					sp--
					ip = m.stageUnwind(ip, fp, mem)
					continue
				}
				mem[sp] = NewValue(dstTyp.Rtype)
				if sp+1 >= len(mem) {
					mem = growStack(mem, sp, 1)
				}
				sp++
				mem[sp] = boolVal(false)
			}

		case TypeBranch: // Arg[0]=offset, Arg[1]=typeIdx (-1 for nil case)
			ifc := mem[sp]
			sp--
			var dtyp *Type
			if int(c.B) != -1 {
				dtyp = m.globals[int(c.B)].ref.Interface().(*Type)
			}
			var matched bool
			if ifc.IsIface() {
				if dtyp != nil {
					ctyp := ifc.IfaceVal().Typ
					if dtyp.IsInterface() {
						matched = ctyp.Implements(dtyp)
					} else {
						matched = ctyp.SameAs(dtyp)
					}
				}
			} else if rv := ifc.Reflect(); rv.IsValid() && rv.Kind() == reflect.Interface && !rv.IsNil() {
				// Native interface value (e.g. from json.Unmarshal map).
				if dtyp != nil {
					concrete := rv.Elem()
					switch {
					case dtyp.IsInterface() && dtyp.Rtype.NumMethod() > 0:
						// Genuine native interface: its rtype carries the method set.
						matched = concrete.Type().Implements(dtyp.Rtype)
					case dtyp.IsInterface():
						// Interpreted (or empty) interface target: dtyp.Rtype is the
						// methodless interface{} placeholder, so reflect.Implements would
						// false-positive. Recover the concrete *Type and consult mvm's
						// method tables; fall back to a name-based check for native concretes.
						if ct := m.typeByRtype(concrete.Type()); ct != nil {
							matched = ct.Implements(dtyp)
						} else {
							matched = dtyp.NativeImplements(concrete.Type())
						}
					default:
						// A type-switch case matches the exact dynamic type, not an
						// assignable one: net.IP (underlying []byte) must not match a
						// `case []byte`. Use identity-aware SameAs, falling back to rtype
						// equality for pure-native concretes.
						if ct := m.typeByRtype(concrete.Type()); ct != nil {
							matched = ct.SameAs(dtyp)
						} else {
							dtypRT := MaterializeRtype(dtyp)
							matched = dtypRT != nil && concrete.Type() == dtypRT
						}
					}
				}
			} else {
				// Nil or invalid value: only matches the nil case.
				matched = dtyp == nil
			}
			if !matched {
				ip += int(c.A)
				continue
			}

		case Exit:
			return true, err
		case Fnew:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = NewValue(m.globals[int(c.A)].ref.Type(), int(c.B))
		case FnewE:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = NewValue(m.globals[int(c.A)].ref.Type().Elem(), int(c.B))
		case Field:
			rv := reflect.Indirect(mem[sp].ref)
			if !rv.IsValid() {
				// A nil pointer here is a nil-receiver field access like x.v.
				// Raise a recoverable nil deref, not a raw reflect panic.
				m.raiseNilDeref()
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			fv := forceSettable(fieldByAB(rv, int(c.A), int(c.B)))
			// Strip the read-only flag from an unexported field read off a
			// non-addressable struct (e.g. a map index) so a later .Interface() won't panic.
			if !fv.CanInterface() {
				fv = Exportable(fv)
			}
			if isNum(fv.Kind()) {
				// Preserve addressable ref for write-through on struct field mutations.
				mem[sp] = Value{num: numBits(fv), ref: fv}
			} else {
				mem[sp] = Value{ref: fv}
			}
		case FieldSet:
			m.setFuncField(forceSettable(fieldByAB(mem[sp-1].ref, int(c.A), int(c.B))), mem[sp])
			sp--
		case FieldFset:
			m.setFuncField(forceSettable(mem[sp-2].ref.Field(int(mem[sp-1].num))), mem[sp])
			sp -= 2
		case FieldRefSet:
			m.setFuncField(forceSettable(mem[sp-1].ref), mem[sp])
			sp -= 2
		case Jump:
			// State-dump safepoint at loop back-edges (backward jumps), avoiding a
			// per-instruction poll. No interrupt poll here: it would tax the hottest
			// path of a pure compute loop. The poll lives on the call opcodes, which
			// any non-trivial loop body reaches.
			if c.A < 0 && dumpRequested.Load() {
				m.handleDumpReq(ip, fp, mem)
			}
			ip += int(c.A)
			continue
		case JumpTrue:
			cond := mem[sp].num != 0
			sp--
			if cond {
				ip += int(c.A)
				continue
			}
		case JumpFalse:
			cond := mem[sp].num != 0
			sp--
			if !cond {
				ip += int(c.A)
				continue
			}
		case JumpSetTrue:
			cond := mem[sp].num != 0
			if cond {
				ip += int(c.A)
				// Note that the stack is not modified if cond is true.
				continue
			}
			sp--
		case JumpSetFalse:
			cond := mem[sp].num != 0
			if !cond {
				ip += int(c.A)
				// Note that the stack is not modified if cond is false.
				continue
			}
			sp--
		case Len:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			// An invalid value represents a zero/nil slice/map/chan/string; Go's
			// len of those is 0, so avoid reflect.Value.Len's zero-Value panic.
			if src := mem[sp-1-int(c.A)].ref; src.IsValid() {
				mem[sp] = ValueOf(src.Len())
			} else {
				mem[sp] = ValueOf(0)
			}
		case Next:
			if k, ok := m.iterStack[len(m.iterStack)-1].next.ref.Interface().(func() (reflect.Value, bool))(); ok {
				m.assignSlot(&m.globals[int(c.B)], FromReflect(k))
			} else {
				ip += int(c.A)
				continue
			}
		case NextLocal:
			if k, ok := m.iterStack[len(m.iterStack)-1].next.ref.Interface().(func() (reflect.Value, bool))(); ok {
				m.assignSlot(&mem[fp-1+int(c.B)], FromReflect(k))
			} else {
				ip += int(c.A)
				continue
			}
		case Next0:
			if _, ok := m.iterStack[len(m.iterStack)-1].next.ref.Interface().(func() (reflect.Value, bool))(); !ok {
				ip += int(c.A)
				continue
			}
		case Next2:
			if k, v, ok := m.iterStack[len(m.iterStack)-1].next.ref.Interface().(func() (reflect.Value, reflect.Value, bool))(); ok {
				kAddr, vAddr := int(int16(c.B)), int(int16(c.B>>16))
				m.assignSlot(&m.globals[kAddr], FromReflect(k))
				m.assignSlot(&m.globals[vAddr], FromReflect(v))
			} else {
				ip += int(c.A)
				continue
			}
		case Next2Local:
			if k, v, ok := m.iterStack[len(m.iterStack)-1].next.ref.Interface().(func() (reflect.Value, reflect.Value, bool))(); ok {
				kAddr, vAddr := int(int16(c.B)), int(int16(c.B>>16))
				m.assignSlot(&mem[fp-1+kAddr], FromReflect(k))
				m.assignSlot(&mem[fp-1+vAddr], FromReflect(v))
			} else {
				ip += int(c.A)
				continue
			}
		case Not:
			if mem[sp].num != 0 {
				mem[sp].num = 0
			} else {
				mem[sp].num = 1
			}
			mem[sp].ref = zbool
		case Pop:
			sp -= int(c.A)
		case Push:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = Value{num: uint64(int(c.A)), ref: zint}
		case Pull:
			v := mem[sp]
			if v.IsIface() {
				v = v.IfaceVal().Val
			}
			seq := emptySeq // invalid (nil slice/map/string) -> empty range, as in Go
			if v.ref.IsValid() {
				if c.A&1 != 0 {
					v = v.CopyArray()
				}
				fromSeq2 := false
				if c.B != 0 {
					// Range-over-func: wrap a mvm Closure into a native Go func.
					funcType := m.globals[int(c.B)-1].ref.Type()
					v = Value{ref: m.wrapForFunc(v, funcType)}
					fromSeq2 = funcType.In(0).NumIn() == 2 // yield takes (K, V)
				}
				if fromSeq2 {
					// iter.Seq2 ranged with fewer than two iteration variables:
					// iterate as a Seq2 but bind only the first yielded value
					// (Go semantics for `for k := range seq2`).
					seq2 := v.Seq2()
					seq = func(yield func(reflect.Value) bool) {
						seq2(func(k, _ reflect.Value) bool { return yield(k) })
					}
				} else {
					seq = v.Seq()
				}
			}
			next, stop := iter.Pull(seq)
			m.iterStack = append(m.iterStack, iterEntry{fp: fp, next: ValueOf(next), stop: ValueOf(stop)})
			sp -= int(c.A>>1) + 1 // drop the n dead loop-var values + the subject
		case Pull2:
			v := mem[sp]
			if v.IsIface() {
				v = v.IfaceVal().Val
			}
			seq2 := emptySeq2 // invalid (nil slice/map) -> empty range, as in Go
			if v.ref.IsValid() {
				if c.A&1 != 0 {
					v = v.CopyArray()
				}
				if c.B != 0 {
					funcType := m.globals[int(c.B)-1].ref.Type()
					v = Value{ref: m.wrapForFunc(v, funcType)}
				}
				// No Seq-vs-Seq2 adapter needed here (unlike Pull): two iteration
				// variables require a yield arity of 2, so the func is always a Seq2.
				seq2 = v.Seq2()
			}
			next, stop := iter.Pull2(seq2)
			m.iterStack = append(m.iterStack, iterEntry{fp: fp, next: ValueOf(next), stop: ValueOf(stop)})
			sp -= int(c.A>>1) + 1 // drop the n dead loop-var values + the subject
		case Grow:
			a := int(c.A)
			if n := a + int(c.B); sp+n >= len(mem) {
				mem = growStack(mem, sp, n)
			}
			// Zero local-variable slots so named returns and other implicitly
			// declared locals don't retain values from a previous invocation
			// that reused this region of the stack.
			for i := 1; i <= a; i++ {
				mem[sp+i] = Value{}
			}
			sp += a
		case DeferPush:
			mem, sp = m.deferPush(c, ip, mem, fp, sp)

		case GoCall:
			narg := int(c.A)
			fval := mem[sp-narg]
			args := make([]Value, narg)
			for i := range args {
				args[i] = snapshotArg(mem[sp-narg+1+i])
			}
			sp -= narg + 1
			m.mem = mem[:sp+1]
			if m.newGoroutine(fval, args, c.B != 0) {
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			mem = m.mem[:cap(m.mem)]

		case GoCallImm:
			narg := int(c.B)
			fval := m.globals[int(c.A)]
			args := make([]Value, narg)
			for i := range args {
				args[i] = snapshotArg(mem[sp-narg+1+i])
			}
			sp -= narg
			m.mem = mem[:sp+1]
			// GoCallImm targets a known VM func: never reflect-spread.
			if m.newGoroutine(fval, args, false) {
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			mem = m.mem[:cap(m.mem)]

		case MkChan:
			elemType := m.globals[int(c.A)].ref.Type()
			// Derive, not reflect.ChanOf: a synth elem must match materialize's chan identity.
			chanType := runtype.DeriveChanOf(reflect.BothDir, elemType)
			bufSize := int(c.B)
			if bufSize < 0 {
				bufSize = int(mem[sp].num)
				sp--
			}
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = Value{ref: reflect.MakeChan(chanType, bufSize)}

		case ChanSend:
			ch := mem[sp-1].ref
			m.chanSend(ch, m.reflectForSend(mem[sp], ch.Type().Elem()))
			sp -= 2

		case ChanRecv:
			ch := mem[sp]
			v, ok := m.chanRecv(ch.ref)
			mem[sp] = FromReflect(v)
			if int(c.A) == 1 {
				if sp+1 >= len(mem) {
					mem = growStack(mem, sp, 1)
				}
				sp++
				mem[sp] = boolVal(ok)
			}

		case ChanClose:
			mem[sp].ref.Close()
			sp--

		case SelectExec:
			meta := m.globals[int(c.A)].ref.Interface().(*SelectMeta)
			ncase := int(c.B)
			base := sp - meta.TotalPop + 1
			// A blocking select (no default) can deadlock if a sender/receiver
			// goroutine died; reserve a trailing slot to watch the fault and abort.
			hasDefault := false
			for _, ci := range meta.Cases {
				if ci.Dir == reflect.SelectDefault {
					hasDefault = true
					break
				}
			}
			abortIdx := -1
			n := ncase
			if !hasDefault && m.watchFault() {
				abortIdx = ncase
				n++
			}
			cases := make([]reflect.SelectCase, n)
			idx := base
			for i, ci := range meta.Cases {
				switch ci.Dir {
				case reflect.SelectRecv:
					cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: mem[idx].ref}
					idx++
				case reflect.SelectSend:
					ch := mem[idx].ref
					cases[i] = reflect.SelectCase{Dir: reflect.SelectSend, Chan: ch, Send: m.reflectForSend(mem[idx+1], ch.Type().Elem())}
					idx += 2
				case reflect.SelectDefault:
					cases[i] = reflect.SelectCase{Dir: reflect.SelectDefault}
				}
			}
			if abortIdx >= 0 {
				cases[abortIdx] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(m.fault.abort)}
			}
			chosen, recv, recvOK := reflect.Select(cases)
			if chosen == abortIdx {
				panic(ErrGoroutineFault)
			}
			sp = base
			ci := meta.Cases[chosen]
			if ci.Dir == reflect.SelectRecv {
				if ci.Slot >= 0 {
					v := FromReflect(recv)
					switch {
					case ci.Cell:
						setCell(mem[fp-1+ci.Slot].ref.Interface().(*Value), v)
					case ci.Local:
						// reflect.Select returns an unaddressable value.
						m.assignSlot(&mem[fp-1+ci.Slot], v)
					default:
						m.assignSlot(&m.globals[ci.Slot], v)
					}
				}
				if ci.OkSlot >= 0 {
					v := boolVal(recvOK)
					switch {
					case ci.OkCell:
						setCell(mem[fp-1+ci.OkSlot].ref.Interface().(*Value), v)
					case ci.Local:
						mem[fp-1+ci.OkSlot] = v
					default:
						m.assignSlot(&m.globals[ci.OkSlot], v)
					}
				}
			}
			mem[sp] = Value{num: uint64(chosen), ref: zint}

		case Print:
			n := int(c.A)
			args := make([]any, n)
			for i := range n {
				args[i] = mem[sp-n+1+i].Interface()
			}
			_, _ = fmt.Fprint(m.out, args...)
			sp -= n

		case Println:
			n := int(c.A)
			args := make([]any, n)
			for i := range n {
				args[i] = mem[sp-n+1+i].Interface()
			}
			_, _ = fmt.Fprintln(m.out, args...)
			sp -= n

		case Complex:
			var (
				cnv = func(v Value) float64 {
					if v.CanInt() {
						return float64(v.Int())
					}
					return v.Float()
				}
				vimag = cnv(mem[sp])
				vreal = cnv(mem[sp-1])
				kind  = reflect.Kind(c.A)
				out   Value
			)
			switch kind {
			case reflect.Complex64:
				out = ValueOf(complex(
					float32(vreal),
					float32(vimag),
				))
			case reflect.Complex128:
				out = ValueOf(complex(
					float64(vreal),
					float64(vimag),
				))
			default:
				panic(fmt.Errorf("impossible: complex-kind: %v", out.ref.Kind()))
			}
			sp--
			mem[sp] = out

		case Real:
			var (
				ref  = mem[sp].ref
				kind = reflect.Kind(c.A)
				v    Value
			)
			switch kind {
			case reflect.Float32:
				v = ValueOf(float32(real(ref.Complex())))
			case reflect.Float64:
				v = ValueOf(float64(real(ref.Complex())))
			default:
				panic("impossible")
			}
			mem[sp] = v

		case Imag:
			var (
				ref  = mem[sp].ref
				kind = reflect.Kind(c.A)
				v    Value
			)
			switch kind {
			case reflect.Float32:
				v = ValueOf(float32(imag(ref.Complex())))
			case reflect.Float64:
				v = ValueOf(float64(imag(ref.Complex())))
			default:
				panic("impossible")
			}
			mem[sp] = v

		case AddComplex, SubComplex, MulComplex, DivComplex:
			x, y := mem[sp-1].ref.Complex(), mem[sp].ref.Complex()
			sp--
			if reflect.Kind(c.A) == reflect.Complex64 {
				// Compute in complex64 so precision matches gc.
				a, b := complex64(x), complex64(y)
				var r complex64
				switch c.Op {
				case AddComplex:
					r = a + b
				case SubComplex:
					r = a - b
				case MulComplex:
					r = a * b
				case DivComplex:
					r = a / b
				}
				mem[sp] = ValueOf(r)
				break
			}
			var r complex128
			switch c.Op {
			case AddComplex:
				r = x + y
			case SubComplex:
				r = x - y
			case MulComplex:
				r = x * y
			case DivComplex:
				r = x / y
			}
			mem[sp] = ValueOf(r)

		case NegComplex:
			if reflect.Kind(c.A) == reflect.Complex64 {
				mem[sp] = ValueOf(-complex64(mem[sp].ref.Complex()))
				break
			}
			mem[sp] = ValueOf(-mem[sp].ref.Complex())

		case Min:
			sp = minMax(mem, sp, int(c.A), reflect.Kind(c.B), false)

		case Max:
			sp = minMax(mem, sp, int(c.A), reflect.Kind(c.B), true)

		case WrapFunc:
			// Wrap the mvm func value on the stack in a reflect.MakeFunc for native Go callbacks.
			// The original mvm func is preserved in MvmFunc.Val for fast in-VM dispatch.
			// The trampoline dispatches each invocation on a pooled runner with pointer-shared
			// parent state, so concurrent native callers (e.g. parallel sort.Slice with an mvm
			// less func) execute safely; user-visible package vars follow Go's memory model.
			typ := m.globals[int(c.A)].ref.Interface().(*Type)
			fval := mem[sp-int(c.B)]
			mem[sp-int(c.B)] = Value{ref: reflect.ValueOf(MvmFunc{Val: fval, GF: m.wrapForFunc(fval, typ.Rtype)})}

		case MkMethodExpr:
			codeAddr := int(m.globals[int(c.A)].num)
			exprType := m.globals[int(c.B)].ref.Interface().(*Type)
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			// Materialize lazily: *T may only resolve after generate (post-attach).
			mem[sp] = Value{ref: m.makeMethodExprFunc(codeAddr, MaterializeRtype(exprType))}

		case Trap:
			ip, fp, sp, mem = m.handleTrap(ip, fp, sp, mem)
			continue

		case Panic:
			m.panicking = true
			m.panicVal = m.effectivePanicVal(mem[sp])
			sp-- // pop the panic argument
			ip = m.stageUnwind(ip, fp, mem)
			continue

		case Recover:
			sp, mem = m.handleRecover(fp, sp, mem, deferRetAddr)

		case MarkNamedRet:
			mem[fp-2].num |= namedRetFlag

		case Return:
			// Read nret and frameBase from the packed retIP slot.
			retIPInfo := mem[fp-2].num
			nret := int((retIPInfo >> 32) & retNretMask)
			frameBase := int(retIPInfo >> 48)
			// If there are pending defers in this frame, dispatch the top one (LIFO).
			dh := int(mem[fp-3].num)
			if dh != 0 {
				packed := mem[dh-2].num
				narg := deferNarg(packed)
				isX := deferIsX(packed)
				prevHead := int(mem[dh-1].num)
				funcVal := mem[dh-narg-3]
				retBase := dh - narg - 3
				// Defer site, seeded by deferPush; read before the VM branch overwrites it.
				deferIP := int(int32(mem[dh].num))
				if isX == 2 {
					if Op(funcVal.num) == Panic {
						// defer panic(arg): begin panicking, drop the pending return
						// values, then unwind the remaining defers of this frame.
						m.panicVal = m.effectivePanicVal(mem[dh-narg-2])
						m.panicking = true
						mem[fp-3].num = uint64(prevHead)
						clear(mem[retBase : sp+1])
						sp = retBase - 1
						ip = m.stageUnwind(ip, fp, mem)
						continue
					}
					m.execBuiltinDeferred(Op(funcVal.num), dh-narg-2, narg, mem)
					clear(mem[retBase+nret : sp+1])
					sp = retBase + nret - 1
					mem[fp-3].num = uint64(prevHead)
					continue
				}
				if isX == 1 {
					// Native function: call via reflect, discard results.
					rv := Exportable(unwrapIface(funcVal.ref))
					if rv.Kind() != reflect.Func || rv.IsNil() {
						// Nil deferred call panics; flag started so panicUnwind pops it.
						mem[dh-2].num |= deferStartedFlag
						m.raiseNilDeref()
						ip = m.stageUnwindAt(deferIP, fp, mem)
						continue
					}
					rin := make([]reflect.Value, narg)
					for i := range rin {
						rin[i] = mem[dh-narg-2+i].Reflect()
					}
					coerceInterfaceArgs(rin, rv.Type())
					m.wrapFuncArgs(rin, mem[dh-narg-2:dh-2], rv.Type())
					// Flag started so a panic inside isn't re-run by panicUnwind.
					// invokeNative makes the panic catchable by an enclosing
					// recover() (Go semantics) instead of escaping the VM raw.
					mem[dh-2].num |= deferStartedFlag
					m.mem, m.fp, m.ip = mem, fp, deferIP+1
					if _, panicked := m.invokeNative(nil, reflect.Value{}, rv, rin, deferSpread(packed)); panicked {
						ip = m.stageUnwindAt(deferIP, fp, mem)
						continue
					}
					// Move return values (at dh+1..dh+nret) down over the defer entry.
					for i := range nret {
						mem[retBase+i] = mem[dh+1+i]
					}
					clear(mem[retBase+nret : sp+1])
					sp = retBase + nret - 1
					mem[fp-3].num = uint64(prevHead)
					continue // re-check for more defers
				}
				// VM function: pack ip and nret into the returnIP slot, then call.
				mem[dh].num = uint64(ip) | uint64(nret)<<32
				mem[dh-2].num |= deferStartedFlag
				prevHeap := m.heap
				nip := m.resolveIPAndHeap(funcVal)
				if nip == nilFuncAddr {
					// Nil deferred call panics; the entry stays on the chain (flagged
					// started above) for panicUnwind's started guard to pop.
					m.raiseNilDeref()
					ip = m.stageUnwindAt(deferIP, fp, mem)
					continue
				}
				// Push func+args copy and 3-slot call frame (retIP, prevFP, deferHead=0).
				base := sp
				if sp+1 >= len(mem) {
					mem = growStack(mem, sp, 1)
				}
				sp++
				mem[sp] = funcVal
				{
					n := (dh - 2) - (dh - narg - 2)
					if sp+n >= len(mem) {
						mem = growStack(mem, sp, n)
					}
					copy(mem[sp+1:], mem[dh-narg-2:dh-2])
					sp += n
				}
				defFPVal := uint64(fp)
				if prevHeap != nil {
					m.heapFrames = append(m.heapFrames, prevHeap)
					defFPVal |= heapSavedFlag
				}
				if sp+3 >= len(mem) {
					mem = growStack(mem, sp, 3)
				}
				mem[sp+1] = Value{}
				mem[sp+2] = Value{num: deferRetBits}
				mem[sp+3] = Value{num: defFPVal}
				sp += 3
				fp = base + 1 + narg + 3 + 1
				ip = nip
				continue
			}
			// No pending defers: normal frame teardown.
			ip = int(int32(retIPInfo))
			ofp := fp
			if len(m.iterStack) != 0 {
				m.dropIterFrames(ofp) // early return out of a range loop skips Stop
			}
			fpVal := mem[fp-1].num
			if fpVal&heapSavedFlag != 0 {
				fp = int(fpVal &^ heapSavedFlag)
				top := len(m.heapFrames) - 1
				m.heap = m.heapFrames[top]
				m.heapFrames[top] = nil // clear for GC
				m.heapFrames = m.heapFrames[:top]
			} else {
				fp = int(fpVal)
				m.heap = nil
			}
			newBase := ofp - frameBase
			if retIPInfo&namedRetFlag != 0 {
				// Captured named returns: finalize results from the fixed
				// named-return slots (mem[ofp+0..nret-1]) AFTER defers, so a
				// deferred closure's write to a named return is reflected.
				finalizeReturns(mem, ofp, newBase, nret)
			} else {
				// Fast path: results are the pushed values at the stack top.
				// Inline copy for common small nret to avoid runtime.typedslicecopy.
				switch nret {
				case 0:
					// nothing to copy
				case 1:
					mem[newBase] = mem[sp]
				default:
					copy(mem[newBase:], mem[sp-nret+1:sp+1])
				}
			}
			sp = newBase + nret - 1
			continue
		case Slice:
			low := int(mem[sp-1].num)
			high := int(mem[sp].num)
			mem[sp-2] = Value{ref: derefArray(mem[sp-2].ref).Slice(low, high)}
			sp -= 2
		case Slice3:
			low := int(mem[sp-2].num)
			high := int(mem[sp-1].num)
			hi := int(mem[sp].num)
			mem[sp-3] = Value{ref: derefArray(mem[sp-3].ref).Slice3(low, high, hi)}
			sp -= 3
		case Stop:
			m.popIterStop()
		// Generic bitwise.
		case BitAnd:
			mem[sp-1].num &= mem[sp].num
			resetNumRef(&mem[sp-1])
			sp--
		case BitOr:
			mem[sp-1].num |= mem[sp].num
			resetNumRef(&mem[sp-1])
			sp--
		case BitXor:
			mem[sp-1].num ^= mem[sp].num
			resetNumRef(&mem[sp-1])
			sp--
		case BitAndNot:
			mem[sp-1].num &^= mem[sp].num
			resetNumRef(&mem[sp-1])
			sp--
		case BitShl:
			// Truncate to LHS width: Go's `<<` yields the LHS type. The uint64
			// shift can overflow narrower types and leave .num out of sync with
			// the value's reflect-backed representation, breaking subsequent
			// comparisons and Equal which read .num directly.
			k := mem[sp-1].ref.Kind()
			mem[sp-1].num = truncToKind(mem[sp-1].num<<mem[sp].num, k)
			resetNumRef(&mem[sp-1])
			sp--
		case BitShr:
			k := mem[sp-1].ref.Kind()
			switch {
			case k >= reflect.Uint && k <= reflect.Uintptr:
				mem[sp-1].num = truncToKind(mem[sp-1].num, k) >> mem[sp].num
			case k == reflect.Int8:
				mem[sp-1].num = uint64(int64(int8(mem[sp-1].num)) >> mem[sp].num)
			case k == reflect.Int16:
				mem[sp-1].num = uint64(int64(int16(mem[sp-1].num)) >> mem[sp].num)
			case k == reflect.Int32:
				mem[sp-1].num = uint64(int64(int32(mem[sp-1].num)) >> mem[sp].num)
			default:
				mem[sp-1].num = uint64(int64(mem[sp-1].num) >> mem[sp].num)
			}
			resetNumRef(&mem[sp-1])
			sp--
		case BitComp:
			mem[sp].num = truncToKind(^mem[sp].num, mem[sp].ref.Kind())
			resetNumRef(&mem[sp])

		// Bit manipulation.
		case Clz32:
			mem[sp].num = uint64(bits.LeadingZeros32(uint32(mem[sp].num)))
			mem[sp].ref = zint
		case Clz64:
			mem[sp].num = uint64(bits.LeadingZeros64(mem[sp].num))
			mem[sp].ref = zint
		case Ctz32:
			mem[sp].num = uint64(bits.TrailingZeros32(uint32(mem[sp].num)))
			mem[sp].ref = zint
		case Ctz64:
			mem[sp].num = uint64(bits.TrailingZeros64(mem[sp].num))
			mem[sp].ref = zint
		case Popcnt32:
			mem[sp].num = uint64(bits.OnesCount32(uint32(mem[sp].num)))
			mem[sp].ref = zint
		case Popcnt64:
			mem[sp].num = uint64(bits.OnesCount64(mem[sp].num))
			mem[sp].ref = zint
		case Rotl32:
			k := int(mem[sp].num)
			sp--
			mem[sp].num = uint64(bits.RotateLeft32(uint32(mem[sp].num), k))
			resetNumRef(&mem[sp])
		case Rotl64:
			k := int(mem[sp].num)
			sp--
			mem[sp].num = bits.RotateLeft64(mem[sp].num, k)
			resetNumRef(&mem[sp])
		case Rotr32:
			k := int(mem[sp].num)
			sp--
			mem[sp].num = uint64(bits.RotateLeft32(uint32(mem[sp].num), -k))
			resetNumRef(&mem[sp])
		case Rotr64:
			k := int(mem[sp].num)
			sp--
			mem[sp].num = bits.RotateLeft64(mem[sp].num, -k)
			resetNumRef(&mem[sp])

		// Float math (unary).
		case AbsFloat32:
			mem[sp].num = putf32(float32(math.Abs(float64(getf32(mem[sp].num)))))
			mem[sp].ref = zfloat32
		case AbsFloat64:
			mem[sp].num = math.Float64bits(math.Abs(math.Float64frombits(mem[sp].num)))
			mem[sp].ref = zfloat64
		case SqrtFloat32:
			mem[sp].num = putf32(float32(math.Sqrt(float64(getf32(mem[sp].num)))))
			mem[sp].ref = zfloat32
		case SqrtFloat64:
			mem[sp].num = math.Float64bits(math.Sqrt(math.Float64frombits(mem[sp].num)))
			mem[sp].ref = zfloat64
		case CeilFloat32:
			mem[sp].num = putf32(float32(math.Ceil(float64(getf32(mem[sp].num)))))
			mem[sp].ref = zfloat32
		case CeilFloat64:
			mem[sp].num = math.Float64bits(math.Ceil(math.Float64frombits(mem[sp].num)))
			mem[sp].ref = zfloat64
		case FloorFloat32:
			mem[sp].num = putf32(float32(math.Floor(float64(getf32(mem[sp].num)))))
			mem[sp].ref = zfloat32
		case FloorFloat64:
			mem[sp].num = math.Float64bits(math.Floor(math.Float64frombits(mem[sp].num)))
			mem[sp].ref = zfloat64
		case TruncFloat32:
			mem[sp].num = putf32(float32(math.Trunc(float64(getf32(mem[sp].num)))))
			mem[sp].ref = zfloat32
		case TruncFloat64:
			mem[sp].num = math.Float64bits(math.Trunc(math.Float64frombits(mem[sp].num)))
			mem[sp].ref = zfloat64
		case NearestFloat32:
			mem[sp].num = putf32(float32(math.RoundToEven(float64(getf32(mem[sp].num)))))
			mem[sp].ref = zfloat32
		case NearestFloat64:
			mem[sp].num = math.Float64bits(math.RoundToEven(math.Float64frombits(mem[sp].num)))
			mem[sp].ref = zfloat64

		// Float math (binary).
		case MinFloat32:
			mem[sp-1].num = putf32(float32(math.Min(float64(getf32(mem[sp-1].num)), float64(getf32(mem[sp].num)))))
			mem[sp-1].ref = zfloat32
			sp--
		case MinFloat64:
			mem[sp-1].num = math.Float64bits(math.Min(math.Float64frombits(mem[sp-1].num), math.Float64frombits(mem[sp].num)))
			mem[sp-1].ref = zfloat64
			sp--
		case MaxFloat32:
			mem[sp-1].num = putf32(float32(math.Max(float64(getf32(mem[sp-1].num)), float64(getf32(mem[sp].num)))))
			mem[sp-1].ref = zfloat32
			sp--
		case MaxFloat64:
			mem[sp-1].num = math.Float64bits(math.Max(math.Float64frombits(mem[sp-1].num), math.Float64frombits(mem[sp].num)))
			mem[sp-1].ref = zfloat64
			sp--
		case CopysignFloat32:
			mem[sp-1].num = putf32(float32(math.Copysign(float64(getf32(mem[sp-1].num)), float64(getf32(mem[sp].num)))))
			mem[sp-1].ref = zfloat32
			sp--
		case CopysignFloat64:
			mem[sp-1].num = math.Float64bits(math.Copysign(math.Float64frombits(mem[sp-1].num), math.Float64frombits(mem[sp].num)))
			mem[sp-1].ref = zfloat64
			sp--

		case Swap:
			a, b := sp-int(c.A), sp-int(c.B)
			mem[a], mem[b] = mem[b], mem[a]
		case HeapAlloc:
			cell := new(Value)
			*cell = mem[sp] // initialise cell with top-of-stack value
			if k := cell.ref.Kind(); isNum(k) {
				rv := reflect.New(cell.ref.Type()).Elem()
				setNumReflect(rv, cell.num)
				cell.ref = rv
			} else if cell.ref.CanAddr() {
				rv := reflect.New(cell.ref.Type()).Elem()
				rv.Set(cell.ref)
				cell.ref = rv
			} else {
				// A non-addressable reference value still needs its own addressable cell storage,
				// or a later write through &cell is lost vs a CellGet read.
				switch cell.ref.Kind() {
				case reflect.Slice, reflect.Map, reflect.Chan, reflect.Pointer:
					rv := reflect.New(cell.ref.Type()).Elem()
					rv.Set(Exportable(cell.ref))
					cell.ref = rv
				}
			}
			mem[sp] = ValueOf(cell) // replace value with cell pointer
		case HeapGet:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			v := *m.heap[int(c.A)]
			if isNum(v.ref.Kind()) && v.ref.CanAddr() {
				v.num = numBits(v.ref) // a native write through &var (Addr) updates ref, not num
			}
			mem[sp] = v
		case HeapSet:
			setCell(m.heap[int(c.A)], mem[sp])
			sp--
		case CellGet:
			sp++
			v := *mem[int(c.A)+fp-1].ref.Interface().(*Value)
			if isNum(v.ref.Kind()) && v.ref.CanAddr() {
				v.num = numBits(v.ref) // a native write through &var (Addr) updates ref, not num
			}
			mem[sp] = v
		case CellSet:
			setCell(mem[int(c.A)+fp-1].ref.Interface().(*Value), mem[sp])
			sp--
		case HeapPtr:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = ValueOf(m.heap[int(c.A)])
		case MkClosure:
			n := int(c.A)
			codeAddr := int(mem[sp-n].num)
			heap := make([]*Value, n)
			for i := range n {
				heap[i] = mem[sp-n+1+i].ref.Interface().(*Value)
			}
			clo := ValueOf(Closure{Code: codeAddr, Heap: heap})
			clear(mem[sp-n : sp+1]) // clear code addr + cell ptr slots
			sp -= n
			mem[sp] = clo
		case MkSlice:
			n := int(c.A)
			var sliceType reflect.Type
			if b := int(c.B); b < 0 {
				// make(NamedSlice, ...): whole type passed, read live like MkMap.
				sliceType = m.globals[-b-1].ref.Type()
			} else {
				// Derive, not reflect.SliceOf: a synth elem must match materialize's slice identity.
				sliceType = runtype.DeriveSliceOf(m.globals[b].ref.Type())
			}
			switch {
			case n < 0:
				// make([]T, len[, cap]): size args are on the stack.
				nSizeArgs := -n
				sLen := int(mem[sp-nSizeArgs+1].num)
				sCap := sLen
				if nSizeArgs == 2 {
					sCap = int(mem[sp].num)
				}
				sp -= nSizeArgs - 1
				mem[sp] = Value{ref: reflect.MakeSlice(sliceType, sLen, sCap)}
			case n == 0:
				if sp+1 >= len(mem) {
					mem = growStack(mem, sp, 1)
				}
				sp++
				mem[sp] = Value{ref: reflect.Zero(sliceType)}
			default:
				slice := reflect.MakeSlice(sliceType, n, n)
				for i := range n {
					m.setFuncField(slice.Index(i), mem[sp-n+1+i])
				}
				mem[sp-n+1] = Value{ref: slice}
				sp -= n - 1
			}
		case MkMap:
			mapIdx, size := int(c.A), 0
			if mapIdx < 0 {
				// make(map, size): negated type index flags a size hint on the stack.
				mapIdx = -mapIdx - 1
				size = int(mem[sp].num)
				sp--
			}
			mapType := m.globals[mapIdx].ref.Type()
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = Value{ref: reflect.MakeMapWithSize(mapType, size)}
		case Append:
			n := int(c.A)
			m.appendValues(mem, sp, n)
			sp -= n
		case AppendSlice:
			n := int(c.A)
			if n == 0 {
				// Spread mode: append(a, b...)
				src := mem[sp].ref
				if src.Kind() == reflect.String {
					// append([]byte, string...) special case.
					src = reflect.ValueOf([]byte(src.String()))
				}
				result := reflect.AppendSlice(mem[sp-1].ref, src)
				sp--
				mem[sp] = Value{ref: result}
				break
			}
			m.appendValues(mem, sp, n)
			sp -= n
		case CopySlice:
			dst := mem[sp-1].ref
			src := mem[sp].ref
			n := reflect.Copy(dst, src)
			mem[sp-1] = ValueOf(n)
			sp--
		case DeleteMap:
			mapVal := mem[sp-1].ref
			mapVal.SetMapIndex(m.mapKey(mapVal.Type().Key(), mem[sp]), reflect.Value{})
			sp--
		case Clear:
			clearValue(mem[sp].Reflect())
			sp--
		case Cap:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = ValueOf(mem[sp-1-int(c.A)].ref.Cap())
		case PtrNew:
			typ := m.globals[int(c.A)].ref.Type()
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = Value{ref: reflect.New(typ)}
		case Index:
			idx := int(mem[sp].num)
			ref := reflect.Indirect(mem[sp-1].ref)
			if ref.Kind() == reflect.String {
				mem[sp-1] = Value{num: uint64(ref.String()[idx]), ref: zuint8}
			} else {
				mem[sp-1] = FromReflect(ref.Index(idx))
			}
			sp--
		case IndexAddr:
			idx := int(mem[sp].num)
			ref := reflect.Indirect(mem[sp-1].ref)
			mem[sp-1] = Value{ref: ref.Index(idx).Addr()}
			sp--
		case IndexSet:
			idx := int(mem[sp-1].num)
			slot := reflect.Indirect(mem[sp-2].ref).Index(idx)
			m.setFuncField(slot, mem[sp])
			sp -= 2
		case MapIndex:
			mapVal := mem[sp-1].ref
			rv := mapVal.MapIndex(m.mapKey(mapVal.Type().Key(), mem[sp]))
			if !rv.IsValid() {
				rv = reflect.Zero(mapVal.Type().Elem())
			}
			mem[sp-1] = FromReflect(rv)
			sp--
		case MapIndexOk:
			mapVal := mem[sp-1].ref
			rv := mapVal.MapIndex(m.mapKey(mapVal.Type().Key(), mem[sp]))
			ok := rv.IsValid()
			if !ok {
				rv = reflect.Zero(mapVal.Type().Elem())
			}
			mem[sp-1] = FromReflect(rv)
			mem[sp] = boolVal(ok)
		case MapSet:
			mapVal := mem[sp-2].ref
			if mapVal.Kind() == reflect.Map && mapVal.IsNil() {
				// Writing to a nil map is a recoverable runtime panic in Go.
				m.raiseNilMapAssign()
				ip = m.stageUnwind(ip, fp, mem)
				continue
			}
			mt := mapVal.Type()
			// adoptNamedType: an untyped constant value keeps its base type. Adopt it.
			mapVal.SetMapIndex(Exportable(m.mapKey(mt.Key(), mem[sp-1])),
				Exportable(adoptNamedType(m.wrapForFunc(mem[sp], mt.Elem()), mt.Elem())))
			sp -= 2
		case SetS:
			n := int(c.A)
			for i := range n {
				m.assignSlot(&mem[sp-2*n+1+i], mem[sp-n+1+i])
			}
			sp -= 2 * n

		// Per-type Add.
		case AddStr:
			mem[sp-1] = Value{ref: reflect.ValueOf(mem[sp-1].ref.String() + mem[sp].ref.String())}
			sp--
		case AddInt:
			mem[sp-1].num = add[int](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint
			sp--
		case AddInt8:
			mem[sp-1].num = add[int8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint8
			sp--
		case AddInt16:
			mem[sp-1].num = add[int16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint16
			sp--
		case AddInt32:
			mem[sp-1].num = add[int32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint32
			sp--
		case AddInt64:
			mem[sp-1].num = add[int64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint64
			sp--
		case AddUint:
			mem[sp-1].num = add[uint](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = uintOrUintptr(mem[sp-1].ref)
			sp--
		case AddUint8:
			mem[sp-1].num = add[uint8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint8
			sp--
		case AddUint16:
			mem[sp-1].num = add[uint16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint16
			sp--
		case AddUint32:
			mem[sp-1].num = add[uint32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint32
			sp--
		case AddUint64:
			mem[sp-1].num = add[uint64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint64
			sp--
		case AddFloat32:
			mem[sp-1].num = addf[float32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zfloat32
			sp--
		case AddFloat64:
			mem[sp-1].num = addf[float64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zfloat64
			sp--

		// Per-type Sub.
		case SubInt:
			mem[sp-1].num = sub[int](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint
			sp--
		case SubInt8:
			mem[sp-1].num = sub[int8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint8
			sp--
		case SubInt16:
			mem[sp-1].num = sub[int16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint16
			sp--
		case SubInt32:
			mem[sp-1].num = sub[int32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint32
			sp--
		case SubInt64:
			mem[sp-1].num = sub[int64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint64
			sp--
		case SubUint:
			mem[sp-1].num = sub[uint](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = uintOrUintptr(mem[sp-1].ref)
			sp--
		case SubUint8:
			mem[sp-1].num = sub[uint8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint8
			sp--
		case SubUint16:
			mem[sp-1].num = sub[uint16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint16
			sp--
		case SubUint32:
			mem[sp-1].num = sub[uint32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint32
			sp--
		case SubUint64:
			mem[sp-1].num = sub[uint64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint64
			sp--
		case SubFloat32:
			mem[sp-1].num = subf[float32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zfloat32
			sp--
		case SubFloat64:
			mem[sp-1].num = subf[float64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zfloat64
			sp--

		// Per-type Mul.
		case MulInt:
			mem[sp-1].num = mul[int](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint
			sp--
		case MulInt8:
			mem[sp-1].num = mul[int8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint8
			sp--
		case MulInt16:
			mem[sp-1].num = mul[int16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint16
			sp--
		case MulInt32:
			mem[sp-1].num = mul[int32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint32
			sp--
		case MulInt64:
			mem[sp-1].num = mul[int64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint64
			sp--
		case MulUint:
			mem[sp-1].num = mul[uint](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = uintOrUintptr(mem[sp-1].ref)
			sp--
		case MulUint8:
			mem[sp-1].num = mul[uint8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint8
			sp--
		case MulUint16:
			mem[sp-1].num = mul[uint16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint16
			sp--
		case MulUint32:
			mem[sp-1].num = mul[uint32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint32
			sp--
		case MulUint64:
			mem[sp-1].num = mul[uint64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint64
			sp--
		case MulFloat32:
			mem[sp-1].num = mulf[float32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zfloat32
			sp--
		case MulFloat64:
			mem[sp-1].num = mulf[float64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zfloat64
			sp--

		// Per-type Div.
		case DivInt:
			mem[sp-1].num = div[int](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint
			sp--
		case DivInt8:
			mem[sp-1].num = div[int8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint8
			sp--
		case DivInt16:
			mem[sp-1].num = div[int16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint16
			sp--
		case DivInt32:
			mem[sp-1].num = div[int32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint32
			sp--
		case DivInt64:
			mem[sp-1].num = div[int64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint64
			sp--
		case DivUint:
			mem[sp-1].num = div[uint](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = uintOrUintptr(mem[sp-1].ref)
			sp--
		case DivUint8:
			mem[sp-1].num = div[uint8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint8
			sp--
		case DivUint16:
			mem[sp-1].num = div[uint16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint16
			sp--
		case DivUint32:
			mem[sp-1].num = div[uint32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint32
			sp--
		case DivUint64:
			mem[sp-1].num = div[uint64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint64
			sp--
		case DivFloat32:
			mem[sp-1].num = divf[float32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zfloat32
			sp--
		case DivFloat64:
			mem[sp-1].num = divf[float64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zfloat64
			sp--

		// Per-type Rem (integer only).
		case RemInt:
			mem[sp-1].num = rem[int](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint
			sp--
		case RemInt8:
			mem[sp-1].num = rem[int8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint8
			sp--
		case RemInt16:
			mem[sp-1].num = rem[int16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint16
			sp--
		case RemInt32:
			mem[sp-1].num = rem[int32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint32
			sp--
		case RemInt64:
			mem[sp-1].num = rem[int64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zint64
			sp--
		case RemUint:
			mem[sp-1].num = rem[uint](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = uintOrUintptr(mem[sp-1].ref)
			sp--
		case RemUint8:
			mem[sp-1].num = rem[uint8](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint8
			sp--
		case RemUint16:
			mem[sp-1].num = rem[uint16](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint16
			sp--
		case RemUint32:
			mem[sp-1].num = rem[uint32](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint32
			sp--
		case RemUint64:
			mem[sp-1].num = rem[uint64](mem[sp-1].num, mem[sp].num)
			mem[sp-1].ref = zuint64
			sp--

		// Per-type Neg.
		case NegInt:
			mem[sp].num = neg[int](mem[sp].num)
			mem[sp].ref = zint
		case NegInt8:
			mem[sp].num = neg[int8](mem[sp].num)
			mem[sp].ref = zint8
		case NegInt16:
			mem[sp].num = neg[int16](mem[sp].num)
			mem[sp].ref = zint16
		case NegInt32:
			mem[sp].num = neg[int32](mem[sp].num)
			mem[sp].ref = zint32
		case NegInt64:
			mem[sp].num = neg[int64](mem[sp].num)
			mem[sp].ref = zint64
		case NegUint:
			mem[sp].num = neg[uint](mem[sp].num)
			mem[sp].ref = uintOrUintptr(mem[sp].ref)
		case NegUint8:
			mem[sp].num = neg[uint8](mem[sp].num)
			mem[sp].ref = zuint8
		case NegUint16:
			mem[sp].num = neg[uint16](mem[sp].num)
			mem[sp].ref = zuint16
		case NegUint32:
			mem[sp].num = neg[uint32](mem[sp].num)
			mem[sp].ref = zuint32
		case NegUint64:
			mem[sp].num = neg[uint64](mem[sp].num)
			mem[sp].ref = zuint64
		case NegFloat32:
			mem[sp].num = negf[float32](mem[sp].num)
			mem[sp].ref = zfloat32
		case NegFloat64:
			mem[sp].num = negf[float64](mem[sp].num)
			mem[sp].ref = zfloat64

		// String Greater / Lower.
		case GreaterStr:
			mem[sp-1] = boolVal(mem[sp-1].ref.String() > mem[sp].ref.String())
			sp--
		case LowerStr:
			mem[sp-1] = boolVal(mem[sp-1].ref.String() < mem[sp].ref.String())
			sp--

		// Per-type Greater.
		case GreaterInt, GreaterInt8, GreaterInt16, GreaterInt32, GreaterInt64:
			mem[sp-1] = boolVal(int64(mem[sp-1].num) > int64(mem[sp].num))
			sp--
		case GreaterUint, GreaterUint8, GreaterUint16, GreaterUint32, GreaterUint64:
			mem[sp-1] = boolVal(mem[sp-1].num > mem[sp].num)
			sp--
		case GreaterFloat32, GreaterFloat64:
			mem[sp-1] = boolVal(math.Float64frombits(mem[sp-1].num) > math.Float64frombits(mem[sp].num))
			sp--

		// Per-type Lower.
		case LowerInt, LowerInt8, LowerInt16, LowerInt32, LowerInt64:
			mem[sp-1] = boolVal(int64(mem[sp-1].num) < int64(mem[sp].num))
			sp--
		case LowerUint, LowerUint8, LowerUint16, LowerUint32, LowerUint64:
			mem[sp-1] = boolVal(mem[sp-1].num < mem[sp].num)
			sp--
		case LowerFloat32, LowerFloat64:
			mem[sp-1] = boolVal(math.Float64frombits(mem[sp-1].num) < math.Float64frombits(mem[sp].num))
			sp--

		// Immediate operand ops: right-hand constant is in Arg[0].
		case AddIntImm:
			mem[sp].num = uint64(int(mem[sp].num) + int(c.A))
			mem[sp].ref = zint
		case SubIntImm:
			mem[sp].num = uint64(int(mem[sp].num) - int(c.A))
			mem[sp].ref = zint
		case MulIntImm:
			mem[sp].num = uint64(int(mem[sp].num) * int(c.A))
			mem[sp].ref = zint
		case GreaterIntImm:
			mem[sp] = boolVal(int(mem[sp].num) > int(c.A))
		case GreaterUintImm:
			mem[sp] = boolVal(uint(mem[sp].num) > uint(int(c.A)))
		case LowerIntImm:
			mem[sp] = boolVal(int(mem[sp].num) < int(c.A))
		case LowerUintImm:
			mem[sp] = boolVal(uint(mem[sp].num) < uint(int(c.A)))

		case DeferRet:
			mem, sp, ip = m.deferRet(mem, fp, sp)
			continue

		case PanicUnwind:
			if done, uerr := m.panicUnwind(&mem, &fp, &sp, &ip, panicAddr); done {
				return true, uerr
			}
			continue
		}
		ip++
	}
}

func (m *Machine) restoreFP(fpVal uint64) int {
	if fpVal&heapSavedFlag != 0 {
		fp := int(fpVal &^ heapSavedFlag)
		top := len(m.heapFrames) - 1
		m.heap = m.heapFrames[top]
		m.heapFrames[top] = nil // clear for GC
		m.heapFrames = m.heapFrames[:top]
		return fp
	}
	m.heap = nil
	return int(fpVal)
}

func unwrapIface(rv reflect.Value) reflect.Value {
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		return rv.Elem()
	}
	return rv
}

// interfaceToInterface coerces a reflect.Value of interface kind to target,
// which must also be an interface type. reflect.Convert(interface{}, error)
// panics, so unwrap to the concrete element first. Returns reflect.Zero(target)
// for nil. src.Kind() must be reflect.Interface.
func interfaceToInterface(src reflect.Value, target reflect.Type) reflect.Value {
	if src.IsNil() {
		return reflect.Zero(target)
	}
	return src.Elem().Convert(target)
}

func isNativeFunc(rv reflect.Value) bool {
	return unwrapIface(rv).Kind() == reflect.Func
}

func (m *Machine) resolveIPAndHeap(funcVal Value) int {
	if isNum(funcVal.ref.Kind()) {
		m.heap = nil
		return int(funcVal.num)
	}
	if clo, ok := funcVal.ref.Interface().(Closure); ok {
		m.heap = clo.Heap
		return clo.Code
	}
	m.heap = nil
	if iv, ok := funcVal.ref.Interface().(int); ok {
		return iv
	}
	return int(funcVal.num)
}

func (m *Machine) deferPush(c *Instruction, ip int, mem []Value, fp, sp int) ([]Value, int) {
	narg := int(c.A)
	isX := int(c.B) & 3
	spread := 0
	if c.B&DeferSpreadFlag != 0 {
		spread = 1
	}
	if isX == 2 {
		// Builtin opcode defer: funcVal (opcode number) is on top of stack,
		// args are below it. Rotate to standard layout: funcVal at sp-narg,
		// args at sp-narg+1..sp.
		funcVal := mem[sp]
		copy(mem[sp-narg+1:sp+1], mem[sp-narg:sp])
		mem[sp-narg] = funcVal
	} else if isX == 0 && isNativeFunc(mem[sp-narg].ref) {
		// Compile-time couldn't tell a variable holding a native Go func from
		// one holding a VM func; detect native at runtime so Return dispatches
		// via reflect.Call instead of jumping to a bogus code address.
		isX = 1
	}
	// Capture the func value at the defer statement (args below via snapshotArg).
	if isX != 2 {
		mem[sp-narg] = snapshotFuncVal(mem[sp-narg])
	}
	for i := sp - narg + 1; i <= sp; i++ {
		mem[i] = snapshotArg(mem[i])
	}
	// Push 3-slot header: packed(narg/isX), prevHead link, returnIP placeholder.
	// isX uses 2 bits: 0=VM func, 1=native reflect func, 2=builtin opcode.
	prevHead := int(mem[fp-3].num)
	if sp+3 >= len(mem) {
		mem = growStack(mem, sp, 3)
	}
	mem[sp+1] = Value{num: packDefer(narg, isX, spread)}
	mem[sp+2] = Value{num: uint64(prevHead)}
	// returnIP placeholder, seeded with the defer-site IP so a panic dispatching
	// the call reports the defer site; Return overwrites it for VM defers.
	mem[sp+3] = Value{num: uint64(uint32(ip))}
	sp += 3
	mem[fp-3].num = uint64(sp) // dh = index of returnIP slot
	return mem, sp
}

func (m *Machine) deferRet(mem []Value, fp, sp int) ([]Value, int, int) {
	mem = mem[:sp+1]
	dh := int(mem[fp-3].num)
	narg := deferNarg(mem[dh-2].num)
	val := mem[dh].num
	returnIP := int(int32(val & 0xFFFFFFFF))
	nret := int(val >> 32)
	prevHead := int(mem[dh-1].num)
	retBase := dh - narg - 3
	copy(mem[retBase:], mem[dh+1:dh+1+nret]) // move return values down
	clear(mem[retBase+nret:])                // clear stale slots
	mem = mem[:retBase+nret]
	mem[fp-3].num = uint64(prevHead)
	sp = len(mem) - 1
	mem = mem[:cap(mem)]
	return mem, sp, returnIP
}

func (m *Machine) panicUnwind(mem *[]Value, fp, sp, ip *int, panicAddr int) (bool, error) {
	deferRetBits := uint64(panicAddr - 1)
	*mem = (*mem)[:*sp+1]
	if *fp == 0 {
		// Top-level panic: no call frame to unwind.
		m.mem, m.ip, m.fp = *mem, 0, 0
		return true, m.escapeErr()
	}
	dh := int((*mem)[*fp-3].num)
	if dh != 0 {
		packed := (*mem)[dh-2].num
		narg := deferNarg(packed)
		isX := deferIsX(packed)
		prevHead := int((*mem)[dh-1].num)
		funcVal := (*mem)[dh-narg-3]
		retBase := dh - narg - 3
		deferIP := int(int32((*mem)[dh].num)) // defer site, seeded by deferPush
		popDefer := func() (bool, error) {
			clear((*mem)[retBase:])
			*mem = (*mem)[:retBase]
			(*mem)[*fp-3].num = uint64(prevHead)
			*sp = len(*mem) - 1
			*mem = (*mem)[:cap(*mem)]
			return false, nil
		}
		if packed&deferStartedFlag != 0 {
			// Body already ran and panicked; skip rather than loop re-running it.
			return popDefer()
		}
		if isX == 2 {
			if Op(funcVal.num) == Panic {
				// defer panic(arg) reached while already unwinding: the new panic
				// value replaces the current one; unwinding then resumes.
				m.panicVal = m.effectivePanicVal((*mem)[dh-narg-2])
				return popDefer()
			}
			m.execBuiltinDeferred(Op(funcVal.num), dh-narg-2, narg, *mem)
			return popDefer()
		}
		if isX == 1 {
			// Native defer: call via reflect, discard results.
			rv := Exportable(unwrapIface(funcVal.ref))
			if rv.Kind() != reflect.Func || rv.IsNil() {
				// Nil deferred call supersedes the in-flight panic (see VM case below).
				m.raiseNilDeref()
				m.panicInfo = m.capturePanicAt(deferIP, *fp, *mem, m.panicRaw())
				return popDefer()
			}
			rin := make([]reflect.Value, narg)
			for i := range rin {
				rin[i] = (*mem)[dh-narg-2+i].Reflect()
			}
			coerceInterfaceArgs(rin, rv.Type())
			m.wrapFuncArgs(rin, (*mem)[dh-narg-2:dh-2], rv.Type())
			// A panic here supersedes the in-flight one (Go); invokeNative
			// captures it as an mvm panic rather than letting it escape raw.
			m.mem, m.fp, m.ip = *mem, *fp, deferIP+1
			if _, panicked := m.invokeNative(nil, reflect.Value{}, rv, rin, deferSpread(packed)); panicked {
				if !m.panicReraised {
					m.panicInfo = m.capturePanicAt(deferIP, *fp, *mem, m.panicRaw())
				}
				m.panicReraised = false
			}
			return popDefer()
		}
		// VM defer: store panicAddr as return address, push frame.
		retIPInfo := (*mem)[*fp-2].num
		nret := int((retIPInfo >> 32) & retNretMask)
		(*mem)[dh].num = uint64(uint32(panicAddr)) | uint64(nret)<<32
		(*mem)[dh-2].num |= deferStartedFlag
		prevHeap := m.heap
		nip := m.resolveIPAndHeap(funcVal)
		if nip == nilFuncAddr {
			// Nil deferred call supersedes the in-flight panic with a nil-deref
			// pointing at the defer site, then unwinding continues.
			m.raiseNilDeref()
			m.panicInfo = m.capturePanicAt(deferIP, *fp, *mem, m.panicRaw())
			return popDefer()
		}
		base := len(*mem)
		*mem = append(*mem, funcVal)
		*mem = append(*mem, (*mem)[dh-narg-2:dh-2]...)
		defFPVal := uint64(*fp)
		if prevHeap != nil {
			m.heapFrames = append(m.heapFrames, prevHeap)
			defFPVal |= heapSavedFlag
		}
		*mem = append(*mem, Value{}, Value{num: deferRetBits}, Value{num: defFPVal})
		*fp = base + 1 + narg + 3
		*ip = nip
		*sp = len(*mem) - 1
		*mem = (*mem)[:cap(*mem)]
		return false, nil
	}
	// No more defers in this frame.
	if !m.panicking {
		// Recovered: produce the result values and tear down the frame.
		retIPInfo := (*mem)[*fp-2].num
		nret := int((retIPInfo >> 32) & retNretMask)
		frameBase := int(retIPInfo >> 48)
		*ip = int(int32(retIPInfo))
		ofp := *fp
		m.dropIterFrames(ofp)
		*fp = m.restoreFP((*mem)[*fp-1].num)
		newBase := ofp - frameBase
		newSP := newBase + nret
		if retIPInfo&namedRetFlag != 0 {
			// Captured named returns: finalize from the fixed slots (deref
			// cells) so a deferred recover that set a named return propagates.
			finalizeReturns(*mem, ofp, newBase, nret)
		} else {
			clear((*mem)[newBase:newSP]) // no named returns: Go returns zeros after recover
		}
		clear((*mem)[newSP:]) // clear stale slots (incl. the source slots)
		*mem = (*mem)[:newSP]
		*sp = len(*mem) - 1
		*mem = (*mem)[:cap(*mem)]
		return false, nil
	}
	// Still panicking: tear down frame, continue unwinding parent.
	frameBase := int((*mem)[*fp-2].num >> 48)
	ofp := *fp
	m.dropIterFrames(ofp)
	*fp = m.restoreFP((*mem)[*fp-1].num)
	if *fp == 0 {
		// Top of stack: return panic as error.
		m.mem, m.ip, m.fp = *mem, 0, 0
		return true, m.escapeErr()
	}
	newBase := ofp - frameBase
	clear((*mem)[newBase:])
	*mem = (*mem)[:newBase]
	*sp = len(*mem) - 1
	*mem = (*mem)[:cap(*mem)]
	return false, nil
}

// fieldByABC reconstructs a FieldByIndex path from fixed A, B, C args.
// B < 0 means single-level; C < 0 means two-level; otherwise three-level.
// fieldByAB accesses a struct field using the A, B encoding:
//
//	B == -1: single field at index A
//	B >= 0:  two-level path [A, B]
func fieldByAB(v reflect.Value, a, b int) reflect.Value {
	if b < 0 {
		return v.Field(a)
	}
	return v.FieldByIndex([]int{a, b})
}

// nativeMethodLookup resolves a method by name, unwrapping interface/pointer
// indirection. Returns invalid on miss; never reaches reflect's "method on
// zero Value" panic path. Kind() on invalid returns Invalid so the Interface
// branch is skipped naturally; only the post-Elem check is load-bearing
// (a non-nil interface containing a typed-nil value Elem()s to invalid).
//
// m is the calling Machine; reflectValueShim captures it into MakeFunc
// closures so concurrent Run() invocations on different goroutines don't
// race through a shared global.
func nativeMethodLookup(m *Machine, rv reflect.Value, name string) reflect.Value {
	if rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return reflect.Value{}
	}
	if shim := runtimeFuncShim(rv, name); shim.IsValid() {
		return shim
	}
	if shim := reflectValueShim(m, rv, name); shim.IsValid() {
		return shim
	}
	if shim := reflectTypeShim(m, rv, name); shim.IsValid() {
		return shim
	}
	if mv := rv.MethodByName(name); mv.IsValid() {
		return mv
	}
	return reflect.Indirect(rv).MethodByName(name)
}

// isSynthOrSynthPtr reports a runtype-built rtype (or pointer to one).
func isSynthOrSynthPtr(rt reflect.Type) bool {
	if runtype.IsSynth(rt) {
		return true
	}
	return rt.Kind() == reflect.Pointer && runtype.IsSynth(rt.Elem())
}

func hasNativeMethod(rt reflect.Type, name string) bool {
	if _, ok := rt.MethodByName(name); ok {
		return true
	}
	if rt.Kind() != reflect.Pointer {
		if _, ok := reflect.PointerTo(rt).MethodByName(name); ok {
			return true
		}
	}
	return false
}

var nilPointerPanicValue = func() (out any) {
	defer func() { out = recover() }()
	var p *byte
	_ = *p
	return nil
}()

func isNilReceiver(rv reflect.Value) bool {
	return !rv.IsValid() || (rv.Kind() == reflect.Interface && rv.IsNil())
}

func (m *Machine) raiseNilDeref() {
	m.panicking = true
	m.panicVal = Value{ref: reflect.ValueOf(nilPointerPanicValue)}
}

// nilMapAssignPanicValue is the runtime.Error for a write to a nil map,
// captured by triggering it (as for nilPointerPanicValue).
var nilMapAssignPanicValue = func() (out any) {
	defer func() { out = recover() }()
	var nm map[int]int
	nm[0] = 0 //nolint:staticcheck // intentional nil-map write to capture the panic value
	return nil
}()

func (m *Machine) raiseNilMapAssign() {
	m.panicking = true
	m.panicVal = Value{ref: reflect.ValueOf(nilMapAssignPanicValue)}
}

// nilInterfacePanic reports whether a panic argument is a nil interface (which
// Go 1.21+ replaces with *runtime.PanicNilError), as opposed to a typed nil
// like (*int)(nil), which is a non-nil interface and kept as-is.
func nilInterfacePanic(v Value) bool {
	if !v.IsValid() {
		return true
	}
	if v.IsIface() {
		return v.IfaceVal().Typ == nil
	}
	return v.ref.Kind() == reflect.Interface && v.ref.IsNil()
}

// panicNilErr is the value Go 1.21+ panics with for panic(nil). Shared since
// recover only reads it.
var panicNilErr = Value{ref: reflect.ValueOf(&runtime.PanicNilError{})}

// panicRaw returns the panic value as a plain any; an untyped-nil panic
// (PanicNilCompat) yields nil.
func (m *Machine) panicRaw() any {
	if !m.panicVal.IsValid() {
		return nil
	}
	return m.panicVal.Interface()
}

// effectivePanicVal substitutes *runtime.PanicNilError for a nil panic value
// (Go 1.21+), or a clean nil interface in PanicNilCompat mode.
func (m *Machine) effectivePanicVal(v Value) Value {
	if !nilInterfacePanic(v) {
		return v
	}
	if m.PanicNilCompat {
		return Value{} // recovers as untyped nil (pre-1.21 semantics)
	}
	return panicNilErr
}

func (m *Machine) panicAddr() int { return m.baseCodeLen + 1 }

func (m *Machine) stageUnwind(ip, fp int, mem []Value) int {
	return m.stageUnwindAt(ip, fp, mem)
}

// stageUnwindAt captures the panic snapshot at panicIP rather than the current
// ip, so a panic dispatching a deferred call points at the defer site, not Return.
func (m *Machine) stageUnwindAt(panicIP, fp int, mem []Value) int {
	if m.panicReraised {
		// invokeNative already adopted the inner run's snapshot (which has the
		// real panic site); the boundary frame here would only point at the
		// native call. Consume the flag and keep the inner snapshot.
		m.panicReraised = false
	} else {
		m.panicInfo = m.capturePanicAt(panicIP, fp, mem, m.panicRaw())
	}
	return m.panicAddr()
}

func (m *Machine) escapeErr() error {
	if m.panicInfo != nil {
		return m.panicInfo
	}
	return fmt.Errorf("panic: %v", m.panicRaw())
}

var typePtrRtype = reflect.TypeFor[*Type]()

func (m *Machine) typeByRtype(rt reflect.Type) *Type {
	if m.typesByRtype == nil {
		m.typesByRtype = &typesIndex{}
	}
	if t := m.typesByRtype.lookup(m.globals, rt); t != nil {
		return t
	}
	// A value-only interpreted type (its *Type never globalized) is absent from the
	// globals index; recover it from the reservation registry. Gate on a synth rtype
	// so genuine native-rtype misses (the hot path) skip the scan.
	if rt != nil && isSynthOrSynthPtr(rt) {
		return typeForReservedRtype(rt)
	}
	return nil
}

func (m *Machine) ifaceMethodFuncType(name string) reflect.Type {
	for id, n := range m.MethodNames {
		if n == name && id < len(m.MethodFuncTypes) {
			return m.MethodFuncTypes[id]
		}
	}
	return nil
}

// PushCode adds instructions to the machine code (with zero source positions).
func (m *Machine) PushCode(code ...Instruction) (p int) {
	p = len(m.code)
	m.code = append(m.code, code...)
	return p
}

// CodeLen returns the current length of the loaded code.
func (m *Machine) CodeLen() int { return len(m.code) }

// PatchJump sets the Jump instruction at pos to transfer control to target.
func (m *Machine) PatchJump(pos, target int) { m.code[pos].A = int32(target - pos) }

// SetIP sets the value of machine instruction pointer to given index.
func (m *Machine) SetIP(ip int) { m.ip = ip }

// Push pushes data values into the machine's global storage.
// Globals are always loaded via Push before Run is called.
func (m *Machine) Push(v ...Value) (l int) {
	l = len(m.globals)
	m.globals = append(m.globals, v...)
	return l
}

func minMax(mem []Value, sp, n int, kind reflect.Kind, isMax bool) int {
	best := sp - n + 1
	switch {
	case kind >= reflect.Int && kind <= reflect.Int64:
		for i := best + 1; i <= sp; i++ {
			if isMax {
				if int64(mem[i].num) > int64(mem[best].num) {
					best = i
				}
			} else {
				if int64(mem[i].num) < int64(mem[best].num) {
					best = i
				}
			}
		}
	case kind >= reflect.Uint && kind <= reflect.Uint64:
		for i := best + 1; i <= sp; i++ {
			if isMax {
				if mem[i].num > mem[best].num {
					best = i
				}
			} else {
				if mem[i].num < mem[best].num {
					best = i
				}
			}
		}
	case kind == reflect.Float32 || kind == reflect.Float64:
		for i := best + 1; i <= sp; i++ {
			fi := math.Float64frombits(mem[i].num)
			fb := math.Float64frombits(mem[best].num)
			switch {
			case math.IsNaN(fi):
				best = i
			case isMax && fi > fb:
				best = i
			case !isMax && fi < fb:
				best = i
			case fi == fb:
				// +0 and -0 compare equal under </>; Go's max prefers +0, min -0.
				if isMax && math.Signbit(fb) && !math.Signbit(fi) {
					best = i
				} else if !isMax && !math.Signbit(fb) && math.Signbit(fi) {
					best = i
				}
			}
		}
	case kind == reflect.String:
		for i := best + 1; i <= sp; i++ {
			if isMax {
				if mem[i].ref.String() > mem[best].ref.String() {
					best = i
				}
			} else {
				if mem[i].ref.String() < mem[best].ref.String() {
					best = i
				}
			}
		}
	default:
		panic(fmt.Sprintf("minMax: unorderable type %v", kind))
	}
	mem[sp-n+1] = mem[best]
	return sp - n + 1
}

// appendValues appends n values from mem[sp-n+1..sp] to the slice at mem[sp-n].
func (m *Machine) appendValues(mem []Value, sp, n int) {
	result := mem[sp-n].ref
	elemType := result.Type().Elem()
	for i := range n {
		val := mem[sp-n+1+i]
		var v reflect.Value
		if val.ref.IsValid() {
			v = m.reflectForSend(val, elemType)
		}
		if !v.IsValid() {
			v = reflect.Zero(elemType)
		}
		result = reflect.Append(result, v)
	}
	mem[sp-n] = Value{ref: result}
}

func (m *Machine) reflectForSend(val Value, elemType reflect.Type) reflect.Value {
	if !val.IsValid() {
		return reflect.Zero(elemType)
	}
	if elemType.Kind() == reflect.Func {
		return m.wrapForFunc(val, elemType)
	}
	if elemType.Kind() == reflect.Interface && val.IsIface() {
		if elemType == AnyRtype {
			return val.Reflect()
		}
		return m.bridgeIface(val.IfaceVal(), elemType)
	}
	rv := val.Reflect()
	if rv.Type() == elemType {
		return rv
	}
	if rv.Kind() == reflect.Interface && elemType.Kind() == reflect.Interface {
		return interfaceToInterface(rv, elemType)
	}
	return rv.Convert(elemType)
}

func (m *Machine) bridgeIface(ifc Iface, targetType reflect.Type) reflect.Value {
	val := ifc.Val.Reflect()
	if ifc.Typ != nil && (!val.IsValid() || (val.Kind() == reflect.Interface && val.IsNil())) {
		return reflect.Zero(ifc.Typ.Rtype)
	}
	if ifc.Typ != nil && ifc.Typ.Rtype == nil {
		MaterializeRtype(ifc.Typ)
	}
	if ifc.Typ != nil && ifc.Typ.Rtype.Kind() == reflect.Func {
		if gf := m.wrapForFunc(ifc.Val, ifc.Typ.Rtype); gf.IsValid() {
			return gf
		}
	}
	if ifc.Typ == nil {
		return val
	}
	if rt := ifc.Typ.Rtype; rt != nil && isNum(rt.Kind()) && isNum(ifc.Val.ref.Kind()) {
		return numReflect(rt, ifc.Val)
	}
	// Restore a named string type the value lost the same way.
	if rt := ifc.Typ.Rtype; rt != nil && val.IsValid() &&
		rt.Kind() == reflect.String && val.Kind() == reflect.String &&
		val.Type() != rt && val.Type().ConvertibleTo(rt) {
		return val.Convert(rt)
	}
	// A struct embedding an interface satisfies a native interface param via
	// methods promoted from the embedded field; reflect.StructOf cannot build
	// those callably, so install a synth rtype that forwards them.
	if targetType != nil && targetType.Kind() == reflect.Interface && targetType.NumMethod() > 0 &&
		val.IsValid() && val.Kind() == reflect.Struct {
		if sv := m.bridgeStructEmbedIface(val, targetType); sv.IsValid() {
			return sv
		}
	}
	return val
}

func (m *Machine) wrapForFunc(val Value, funcType reflect.Type) reflect.Value {
	if funcType.Kind() != reflect.Func {
		if !val.ref.IsValid() {
			return reflect.Zero(funcType)
		}
		// When storing into interface{} (e.g. map[string]interface{}), unwrap
		// MvmFunc to the native Go function so native code sees a real func.
		if pf, ok := val.ref.Interface().(MvmFunc); ok {
			return pf.GF
		}
		// Unbox an mvm Iface for an interface destination so native code sees
		// raw Go values (e.g. a map[string]any literal passed to json.Marshal).
		if w, ok := m.unboxIfaceFor(val, funcType); ok {
			return w
		}
		// Storing into an interface slot narrower than interface{} (e.g.
		// image.Image): bridge an mvm Iface to the concrete it wraps, or unwrap
		// an interface{}-boxed value through its element -- both to satisfy
		// reflect assignability. Mirrors reflectForSend.
		if funcType.Kind() == reflect.Interface && funcType != AnyRtype {
			if val.IsIface() {
				return m.bridgeIface(val.IfaceVal(), funcType)
			}
			if rv := val.Reflect(); rv.IsValid() && rv.Kind() == reflect.Interface && rv.Type() != funcType {
				return interfaceToInterface(rv, funcType)
			}
		}
		return numReflect(funcType, val)
	}
	rv := val.Reflect()
	if !rv.IsValid() {
		return reflect.Zero(funcType)
	}
	// A nil interface slot is a zero func, not a callback: do not wrap it.
	if rv.Kind() == reflect.Interface && rv.IsNil() {
		return reflect.Zero(funcType)
	}
	// Unwrap an interface-boxed func: Elem() also detaches it from addressable
	// storage (e.g. a loop-var slot), so the wrapper can't alias the live slot.
	if rv.Kind() == reflect.Interface && rv.Elem().Kind() == reflect.Func {
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Func {
		if pf, ok := rv.Interface().(MvmFunc); ok {
			return m.mvmFuncFor(pf, funcType)
		}
		// Retype a native func for a named destination; Convert keeps the funcval pointer.
		if rv.Type() != funcType && rv.Type().ConvertibleTo(funcType) {
			rv = rv.Convert(funcType)
		}
		return rv // already a proper Go func
	}
	// Already wrapped by WrapFunc: extract the Go func wrapper.
	if pf, ok := val.ref.Interface().(MvmFunc); ok {
		return m.mvmFuncFor(pf, funcType)
	}
	return m.makeCallFunc(val, funcType)
}

func (m *Machine) mvmFuncFor(pf MvmFunc, funcType reflect.Type) reflect.Value {
	if pf.GF.IsValid() && pf.GF.Type() != funcType {
		return m.makeCallFunc(pf.Val, funcType)
	}
	return pf.GF
}

// funcFieldsTable maps the underlying Go func pointer of a wrapped
// reflect.MakeFunc value back to the mvm func Value it represents.
type funcRef struct{ val Value }

// methodExpr marks a funcFields entry as a method-expression wrapper carrying
// its body code address, so the Call opcode dispatches it through the
// interpreter (binding the first arg as receiver) instead of reflect.Call.
// Going via reflect.Call would require boxing the args into the wrapper's
// (possibly synthetic) interface param types, which reflect rejects when an
// interpreted concrete does not carry the interface's method set in its rtype.
type methodExpr struct{ code int }

type funcFieldEntry struct {
	strong Value
	weak   weak.Pointer[funcRef]
	isWeak bool
	gen    uint64 // registration generation; lets a cleanup skip a successor at a recycled address
}

type funcFieldsTable struct {
	mu            sync.RWMutex
	m             map[uintptr]funcFieldEntry
	gen           uint64
	hasMethodExpr atomic.Bool // any methodExpr entry registered; gates the Call-opcode lookup
}

func newFuncFieldsTable() *funcFieldsTable {
	return &funcFieldsTable{m: make(map[uintptr]funcFieldEntry)}
}

func (t *funcFieldsTable) get(p uintptr) (Value, bool) {
	t.mu.RLock()
	e, ok := t.m[p]
	t.mu.RUnlock()
	if !ok {
		return Value{}, false
	}
	if e.isWeak {
		if r := e.weak.Value(); r != nil {
			return r.val, true
		}
		return Value{}, false // receiver graph collected; entry is stale
	}
	return e.strong, true
}

// setStrongKeep registers v at p unless a LIVE weak entry already maps the
// address: that entry belongs to the funcval currently alive at p (an address
// cannot be recycled under a live referent) and self-prunes, which a strong
// overwrite would forfeit. A strong or dead-weak entry is replaced: the
// address was recycled, and keeping the old mapping resolves the new funcval
// to a dead wrapper's closure (observed as quicktest's checkParams.fail
// calling a finished test's t.Fatal: "Fail in goroutine after X completed").
// Returns the stored generation, or 0 when the weak entry was kept.
func (t *funcFieldsTable) setStrongKeep(p uintptr, v Value) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.m[p]; ok && e.isWeak && e.weak.Value() != nil {
		return 0
	}
	t.gen++
	t.m[p] = funcFieldEntry{strong: v, gen: t.gen}
	return t.gen
}

// pruneStrong deletes the strong entry at p if it is still generation gen;
// a later registration at a recycled p must survive its predecessor's cleanup.
func (t *funcFieldsTable) pruneStrong(p uintptr, gen uint64) {
	t.mu.Lock()
	if e, ok := t.m[p]; ok && !e.isWeak && e.gen == gen {
		delete(t.m, p)
	}
	t.mu.Unlock()
}

// cleanupStrong ties the entry's lifetime to the funcval fp: when it is
// collected the entry is pruned, so a recycled address can never resolve to a
// dead wrapper's closure, and the entry stops pinning the closure graph. A
// static funcval is not a heap object and makes AddCleanup panic; recover and
// keep the entry -- a static address is never recycled, so it cannot go stale.
// When the entry's own value graph reaches back to the funcval (field ->
// wrapper -> mvm func -> receiver -> field), the funcval stays reachable and
// the cleanup never fires: same lifetime as before, not a regression.
func (t *funcFieldsTable) cleanupStrong(fp unsafe.Pointer, gen uint64) {
	defer func() { _ = recover() }()
	p := uintptr(fp)
	runtime.AddCleanup((*byte)(fp), func(g uint64) { t.pruneStrong(p, g) }, gen)
}

func (t *funcFieldsTable) setWeak(p uintptr, ref *funcRef) {
	t.mu.Lock()
	t.m[p] = funcFieldEntry{weak: weak.Make(ref), isWeak: true}
	t.mu.Unlock()
}

func (t *funcFieldsTable) prune(p uintptr) {
	t.mu.Lock()
	if e, ok := t.m[p]; ok && e.isWeak && e.weak.Value() == nil {
		delete(t.m, p)
	}
	t.mu.Unlock()
}

// setMethodExpr records p as a method-expression wrapper for body code.
// The entry is strong and permanent: makeMethodExprFunc caches the wrapper, so
// its address is stable and the entry count is bounded by distinct method exprs.
func (t *funcFieldsTable) setMethodExpr(p uintptr, code int) {
	t.mu.Lock()
	t.m[p] = funcFieldEntry{strong: Value{ref: reflect.ValueOf(methodExpr{code})}}
	t.mu.Unlock()
	t.hasMethodExpr.Store(true)
}

// methodExprCode returns the body code address if p is a method-expression wrapper.
// Cheaply gated by hasMethodExpr so non-method-expr programs skip it.
func (t *funcFieldsTable) methodExprCode(p uintptr) (int, bool) {
	if !t.hasMethodExpr.Load() {
		return 0, false
	}
	t.mu.RLock()
	e, ok := t.m[p]
	t.mu.RUnlock()
	if !ok || e.isWeak || !e.strong.ref.IsValid() || !e.strong.ref.CanInterface() {
		return 0, false
	}
	me, ok := e.strong.ref.Interface().(methodExpr)
	return me.code, ok
}

// funcWrapTable memoises the MakeFunc wrapper for a heap-less mvm func, keyed by
// code address + func type, so repeated bridges share one *funcval and compare
// equal (reflect.ValueOf(f) == reflect.ValueOf(f)). Shared like funcFields.
type funcWrapTable struct {
	mu sync.RWMutex
	m  map[funcWrapKey]reflect.Value
}

type funcWrapKey struct {
	code  int
	rtype reflect.Type
}

func newFuncWrapTable() *funcWrapTable {
	return &funcWrapTable{m: make(map[funcWrapKey]reflect.Value)}
}

func (t *funcWrapTable) getOrBuild(k funcWrapKey, build func() reflect.Value) reflect.Value {
	t.mu.RLock()
	v, ok := t.m[k]
	t.mu.RUnlock()
	if ok {
		return v
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if v, ok := t.m[k]; ok {
		return v
	}
	v = build()
	t.m[k] = v
	return v
}

func staticFuncCode(val Value) (int, bool) {
	if !val.ref.IsValid() {
		return 0, false
	}
	if isNum(val.ref.Kind()) {
		return int(val.num), true
	}
	if !val.ref.CanInterface() {
		return 0, false
	}
	switch f := val.ref.Interface().(type) {
	case Closure:
		if len(f.Heap) == 0 {
			return f.Code, true
		}
	case int:
		return f, true
	}
	return 0, false
}

// typesIndex memoises a reflect.Type -> *Type lookup over a Machine's
// globals. Populated once on first lookup under sync.Once (which provides
// happens-before for all later readers) and immutable afterward, so the
// steady-state lookup is a plain Go map read.
type typesIndex struct {
	once sync.Once
	m    map[reflect.Type]*Type
}

func (t *typesIndex) lookup(globals []Value, rt reflect.Type) *Type {
	t.once.Do(func() {
		t.m = map[reflect.Type]*Type{}
		// Rtypes mapping to two genuinely different *Types are ambiguous: reflect
		// cannot mint a distinct named rtype for a defined type, so it reuses the
		// base's rtype -- `type A int`/`type B int` both resolve to int's rtype, and
		// `type Y X` (X a named struct) reuses X's reflect.StructOf rtype. A value
		// round-tripped through native reflect has lost which one it was, so
		// recovering "the" *Type would dispatch the wrong type's methods; declaring
		// even an unused sibling thus makes the original decline. Decline (return
		// nil) for such rtypes rather than guess. SameAs clones (same type, e.g.
		// struct-field shallow copies) are not ambiguous.
		ambiguous := map[reflect.Type]bool{}
		visited := map[*Type]bool{}
		// register indexes v by its rtype (with the ambiguity rule) and recurses
		// into its component types. The recursion makes types reachable only
		// through a func signature resolvable -- e.g. a struct used solely as a
		// closure parameter: its func *Type is in globals (WrapFunc typeIndexes it)
		// but the param type itself was never separately typeIndexed. Recurse only
		// into Params/Returns/ElemType/KeyType, NOT Fields/Base, whose clones can
		// carry a field/base name distinct from the type name and falsely trip the
		// SameAs ambiguity check.
		var register func(v *Type)
		index := func(rt reflect.Type, v *Type) {
			if rt == nil || ambiguous[rt] {
				return
			}
			if prev, ok := t.m[rt]; ok {
				if prev != v && !prev.SameAs(v) {
					delete(t.m, rt)
					ambiguous[rt] = true
				}
			} else {
				t.m[rt] = v
			}
		}
		register = func(v *Type) {
			if v == nil || visited[v] {
				return
			}
			visited[v] = true
			index(v.Rtype, v)
			for _, p := range v.Params {
				register(p)
			}
			for _, r := range v.Returns {
				register(r)
			}
			register(v.ElemType)
			register(v.KeyType)
		}
		for _, g := range globals {
			if !g.ref.IsValid() || g.ref.Type() != typePtrRtype {
				continue
			}
			if v, _ := g.ref.Interface().(*Type); v != nil {
				register(v)
			}
		}
	})
	return t.m[rt]
}

// runnerState captures the Machine fields needed to create lightweight runner
// Machines for re-entrant execution (bridge callbacks, MakeFunc adapters).
// The pool itself lives on the parent Machine and is shared across all
// runnerStates rooted there, so a high bridge-construction count doesn't
// multiply retained memory.
type runnerState struct {
	globals         []Value
	code            []Instruction // shared pooledCode (prefix + sentinels + trampolines)
	baseCodeLen     int
	trampBase       int
	out, err        io.Writer
	methodNames     []string
	methodFuncTypes []reflect.Type
	funcFields      *funcFieldsTable // parent-owned; pointer-shared with runner
	funcWrappers    *funcWrapTable   // parent-owned; pointer-shared with runner
	typesByRtype    *typesIndex      // parent-owned; pointer-shared with runner
	debugInfoFn     func() *DebugInfo
	pool            *sync.Pool      // shared pool on the parent Machine
	fault           *goroutineFault // shared goroutine-panic sink
	faultContinue   bool
	panicNilCompat  bool
}

// ensureSharedTables lazy-allocates the parent-owned funcFields and
// typesByRtype tables so they can be pointer-shared with runner Machines
// and spawned goroutines. Callers run on the parent before any runner
// exists, making the nil-check race-free.
func (m *Machine) ensureSharedTables() {
	if m.funcFields == nil {
		m.funcFields = newFuncFieldsTable()
	}
	if m.funcWrappers == nil {
		m.funcWrappers = newFuncWrapTable()
	}
	if m.typesByRtype == nil {
		m.typesByRtype = &typesIndex{}
	}
}

// Trampoline-table arity bounds; calls above them fall back to the append path.
const trampMaxArgs, trampMaxRets = 16, 16

// ensurePooledCode (re)builds the shared runner code: the immutable compiled
// prefix, the three Run sentinels at the same offsets Run would append them
// (so panicAddr/exitAddr conventions hold), and a static Call/Exit trampoline
// per (narg, nret) pair. Runners and goroutine machines share it read-only, so
// pooled re-entry never appends -- an append on the previous capacity-capped
// slice copied the whole program per call (the cast OOM). Rebuilt when
// baseCodeLen changes (new Eval, TrimCode); same single-threaded contract as
// ensureSharedTables.
func (m *Machine) ensurePooledCode() {
	if m.pooledCode != nil && m.pooledCodeBase == m.baseCodeLen && m.pooledCodeEpoch == m.codeEpoch {
		return
	}
	base := m.baseCodeLen
	code := make([]Instruction, base, base+3+(trampMaxArgs+1)*(trampMaxRets+1)*2)
	copy(code, m.code[:base])
	code = append(code, Instruction{Op: DeferRet}, Instruction{Op: PanicUnwind}, Instruction{Op: Exit})
	for a := 0; a <= trampMaxArgs; a++ {
		for r := 0; r <= trampMaxRets; r++ {
			code = append(code, Instruction{Op: Call, A: int32(a), B: int32(r)}, Instruction{Op: Exit})
		}
	}
	m.pooledCode = code
	m.pooledCodeBase = base
	m.pooledCodeEpoch = m.codeEpoch
}

// trampolineIP returns the static Call trampoline address for an arity pair,
// or ok=false when this machine has no table or the arity is out of bounds.
func (m *Machine) trampolineIP(narg, nret int) (int, bool) {
	if m.trampBase == 0 || narg > trampMaxArgs || nret > trampMaxRets {
		return 0, false
	}
	return m.trampBase + 3 + (narg*(trampMaxRets+1)+nret)*2, true
}

func (m *Machine) captureRunnerState() *runnerState {
	m.ensureSharedTables()
	m.ensurePooledCode()
	return &runnerState{
		globals:         m.globals,
		code:            m.pooledCode,
		baseCodeLen:     m.pooledCodeBase,
		trampBase:       m.pooledCodeBase,
		out:             m.out,
		err:             m.err,
		methodNames:     m.MethodNames,
		methodFuncTypes: m.MethodFuncTypes,
		funcFields:      m.funcFields,
		funcWrappers:    m.funcWrappers,
		typesByRtype:    m.typesByRtype,
		debugInfoFn:     m.debugInfoFn,
		pool:            &m.runnerPool,
		fault:           m.fault,
		faultContinue:   m.faultContinue,
		panicNilCompat:  m.PanicNilCompat,
	}
}

// acquireRunner gets a runner Machine from the shared pool and applies rs
// to it. The parent may have mutated shared state (globals, code) since the
// runnerState was captured, so we re-sync on every acquire. Pair with
// releaseRunner.
func (rs *runnerState) acquireRunner() *Machine {
	m, _ := rs.pool.Get().(*Machine)
	if m == nil {
		m = &Machine{}
	}
	m.globals = rs.globals
	m.code = rs.code
	m.baseCodeLen = rs.baseCodeLen
	m.trampBase = rs.trampBase
	m.pooledCode = rs.code
	m.pooledCodeBase = rs.baseCodeLen
	m.codeEpoch, m.pooledCodeEpoch = 0, 0 // runners never trim; keep the cache hit-condition true
	m.out = rs.out
	m.err = rs.err
	m.MethodNames = rs.methodNames
	m.MethodFuncTypes = rs.methodFuncTypes
	m.funcFields = rs.funcFields
	m.funcWrappers = rs.funcWrappers
	m.typesByRtype = rs.typesByRtype
	m.debugInfoFn = rs.debugInfoFn
	m.fault = rs.fault
	m.faultContinue = rs.faultContinue
	m.PanicNilCompat = rs.panicNilCompat
	return m
}

// releaseRunner trims per-call execution state and returns the Machine to
// the shared pool. Keeps the mem and code backing arrays so the next
// acquire can reuse them.
func (rs *runnerState) releaseRunner(m *Machine) {
	m.mem = m.mem[:0]
	m.code = m.code[:rs.baseCodeLen]
	m.heap = nil
	m.heapFrames = nil
	m.ip = 0
	m.fp = 0
	m.panicking = false
	m.panicVal = Value{}
	m.iterStack = m.iterStack[:0]
	rs.pool.Put(m)
}

// unwrapVariadicIface unwraps Iface elements in a packed variadic slice so
// reflect.CallSlice passes the native callee concrete values.
func unwrapVariadicIface(last reflect.Value) reflect.Value {
	if last.Kind() == reflect.Interface && !last.IsNil() {
		last = last.Elem()
	}
	if last.Kind() != reflect.Slice {
		return last
	}
	for i := range last.Len() {
		elem := last.Index(i)
		if elem.Kind() == reflect.Interface && !elem.IsNil() {
			if ifc, ok := elem.Interface().(Iface); ok {
				elem.Set(ifc.Val.Reflect())
			}
		}
	}
	return last
}

// invokeNative runs a native func/method call, recovering any Go panic.
// On a recoverable panic it sets the machine's panic state and returns panicked=true.
// A clean-exit signal is re-panicked so Run's recoverPanic terminates the program.
func (m *Machine) invokeNative(hook NativeMethodHook, hookRecv, rv reflect.Value, in []reflect.Value, spread bool) (out []reflect.Value, panicked bool) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		// An mvm panic that escaped a nested re-entrant Run via a native callback:
		// re-establish it with its original value so an outer interpreted recover() sees it.
		if pe, ok := asPanicError(r); ok {
			m.panicking = true
			m.panicVal = FromReflect(reflect.ValueOf(pe.Raw))
			// Stitch this parent's frames onto the inner run's snapshot across
			// the native boundary, so the mvm stack spans interp -> native -> interp.
			// Composes across nesting: each invokeNative level on the
			// unwind path appends its own segment.
			m.stitchBoundary(pe, rv)
			m.panicInfo = pe // keep the inner run's source pos + mvm stack
			m.panicReraised = true
			panicked = true
			return
		}
		// Clean-exit signal must not be interceptable by recover(); re-panic.
		// A genuine native panic(err) is NOT a clean exit and must stay catchable.
		if _, ok := r.(CleanExit); ok {
			panic(r)
		}
		// A goroutine-fault abort is not a recoverable; re-panic so it reaches Run's recoverPanic.
		if e, ok := r.(error); ok && errors.Is(e, ErrGoroutineFault) {
			panic(r)
		}
		// Genuine native panic; make it catchable by interpreted recover().
		m.panicking = true
		m.panicVal = FromReflect(reflect.ValueOf(m.nativePanicValue(rv, in, r)))
		panicked = true
	}()
	switch {
	case hook != nil && !spread:
		return hook(m, hookRecv, in), false
	case spread:
		if err := checkNativeCall(rv, in, true); err != nil {
			panic(err)
		}
		// For spread calls (f(s...)), unwrap Iface values inside the variadic slice and use CallSlice.
		in[len(in)-1] = unwrapVariadicIface(in[len(in)-1])
		return rv.CallSlice(in), false
	default:
		if out, ok := interceptReflectCtor(rv, in); ok {
			return out, false
		}
		if err := checkNativeCall(rv, in, false); err != nil {
			panic(err)
		}
		return rv.Call(in), false
	}
}

func checkNativeCall(rv reflect.Value, in []reflect.Value, slice bool) error {
	if rv.Kind() != reflect.Func {
		return fmt.Errorf("call of non-function value of kind %s", rv.Kind())
	}
	ft := rv.Type()
	want, variadic := ft.NumIn(), ft.IsVariadic()
	switch {
	case variadic && !slice:
		if len(in) < want-1 {
			return fmt.Errorf("call to %v: got %d arguments, want at least %d", ft, len(in), want-1)
		}
	default:
		if len(in) != want {
			return fmt.Errorf("call to %v: got %d arguments, want %d", ft, len(in), want)
		}
	}
	return nil
}

func describeNativePanic(rv reflect.Value, in []reflect.Value, r any) any {
	s, ok := r.(string)
	if !ok || !strings.HasPrefix(s, "reflect") || rv.Kind() != reflect.Func {
		return r
	}
	args := make([]string, len(in))
	for i, a := range in {
		if a.IsValid() {
			args[i] = a.Type().String()
		} else {
			args[i] = "<invalid>"
		}
	}
	return fmt.Sprintf("%s (calling %v with args [%s])", s, rv.Type(), strings.Join(args, ", "))
}

func (m *Machine) nativePanicValue(rv reflect.Value, in []reflect.Value, r any) any {
	desc := describeNativePanic(rv, in, r)
	s, ok := r.(string)
	if !ok || !strings.HasPrefix(s, "reflect") {
		return desc
	}
	ds, _ := desc.(string)
	var pos Pos
	if i := m.ip - 1; i >= 0 && i < len(m.code) { // m.ip was advanced past the call opcode
		pos = m.code[i].Pos
	}
	return m.posPrefix(pos) + ds
}

func (m *Machine) makeCallFunc(fval Value, fnType reflect.Type) reflect.Value {
	if code, ok := staticFuncCode(fval); ok {
		m.ensureSharedTables()
		return m.funcWrappers.getOrBuild(funcWrapKey{code: code, rtype: fnType},
			func() reflect.Value { return m.buildCallFunc(fval, fnType) })
	}
	return m.buildCallFunc(fval, fnType)
}

func (m *Machine) buildCallFunc(fval Value, fnType reflect.Type) reflect.Value {
	rs := m.captureRunnerState() // also ensures m.funcFields is non-nil
	// Snapshot addressable storage: the closure below captures fval for the
	// wrapper's lifetime, and a live slot may be overwritten after the wrap.
	if fval.ref.IsValid() && fval.ref.CanAddr() {
		cp := reflect.New(fval.ref.Type()).Elem()
		cp.Set(Exportable(fval.ref))
		fval.ref = cp
	}
	ref := &funcRef{val: fval}
	w := reflect.MakeFunc(fnType, func(args []reflect.Value) []reflect.Value {
		runner := rs.acquireRunner()
		defer rs.releaseRunner(runner)
		defer runtime.KeepAlive(ref)
		out, err := runner.callPooled(fval, fnType, args)
		if err != nil {
			var pe *PanicError
			if errors.As(err, &pe) {
				panic(reraisedPanic{pe}) // raw value for native recover; see reraisedPanic
			}
			panic(err)
		}
		return out
	})
	key := funcDataPtr(w)
	m.funcFields.setWeak(key, ref)
	runtime.AddCleanup(ref, m.funcFields.prune, key)
	return w
}

// makeMethodExprFunc returns the func value for a method expression T.M.
// The wrapper is cached (heap-less, keyed by code+type) and registered in
// funcFields so the Call opcode can dispatch it through the interpreter; see
// methodExpr. Registration happens only on the build (cache-miss) path.
func (m *Machine) makeMethodExprFunc(codeAddr int, exprType reflect.Type) reflect.Value {
	m.ensureSharedTables()
	return m.funcWrappers.getOrBuild(funcWrapKey{code: codeAddr, rtype: exprType},
		func() reflect.Value {
			w := m.buildMethodExprFunc(codeAddr, exprType)
			m.funcFields.setMethodExpr(funcDataPtr(w), codeAddr)
			return w
		})
}

func (m *Machine) buildMethodExprFunc(codeAddr int, exprType reflect.Type) reflect.Value {
	ins := make([]reflect.Type, exprType.NumIn()-1)
	for i := range ins {
		ins[i] = exprType.In(i + 1)
	}
	outs := make([]reflect.Type, exprType.NumOut())
	for i := range outs {
		outs[i] = exprType.Out(i)
	}
	innerType := reflect.FuncOf(ins, outs, exprType.IsVariadic())
	rs := m.captureRunnerState()
	return reflect.MakeFunc(exprType, func(args []reflect.Value) []reflect.Value {
		cell := new(Value)
		*cell = FromReflect(args[0])
		clo := Value{ref: reflect.ValueOf(Closure{Code: codeAddr, Heap: []*Value{cell}})}
		runner := rs.acquireRunner()
		defer rs.releaseRunner(runner)
		out, err := runner.callPooled(clo, innerType, args[1:])
		if err != nil {
			panic(err)
		}
		return out
	})
}

// TrimStack removes leftover stack values from a previous Run.
// Call before pushing new global data on re-entry.
func (m *Machine) TrimStack() {
	m.mem = m.mem[:0]
}

// CallFunc executes a mvm function value with the given arguments and returns the results.
// It saves and restores per-frame execution state so it can be called from native Go
// callbacks (reflect.MakeFunc wrappers) even while Run is in progress
// (single-threaded re-entrancy). Globals are NOT isolated: a callback's package-var
// write is visible to the outer Run, matching Go callback semantics and the
// goroutine model documented in ADR-008.
func (m *Machine) CallFunc(fval Value, funcType reflect.Type, args []reflect.Value) ([]reflect.Value, error) {
	// Save volatile per-frame state (globals are intentionally shared).
	savedMem := m.mem
	savedIP := m.ip
	savedFP := m.fp
	savedHeap := m.heap
	savedFrames := m.heapFrames
	savedPanicking := m.panicking
	savedPanicVal := m.panicVal
	savedPanicInfo := m.panicInfo
	savedPanicReraised := m.panicReraised
	savedCodeLen := len(m.code)

	defer func() {
		m.mem = savedMem
		m.ip = savedIP
		m.fp = savedFP
		m.heap = savedHeap
		m.heapFrames = savedFrames
		m.panicking = savedPanicking
		m.panicVal = savedPanicVal
		m.panicInfo = savedPanicInfo
		m.panicReraised = savedPanicReraised
		m.code = m.code[:savedCodeLen]
	}()

	// Reset per-call state.
	m.heap = nil
	m.heapFrames = nil
	m.panicking = false
	m.panicVal = Value{}
	m.panicInfo = nil
	m.panicReraised = false

	// Fresh stack with func value and args.
	m.mem = nil
	m.mem = append(m.mem, fval)
	for _, a := range args {
		m.mem = append(m.mem, FromReflect(a))
	}

	// Drive the function to completion via a static trampoline when one
	// exists, else temporarily append Call + Exit.
	narg := funcType.NumIn()
	nret := funcType.NumOut()
	if ip, ok := m.trampolineIP(narg, nret); ok {
		m.ip = ip
	} else {
		callIP := len(m.code)
		m.code = append(m.code, Instruction{Op: Call, A: int32(narg), B: int32(nret)})
		m.code = append(m.code, Instruction{Op: Exit})
		m.ip = callIP
	}
	m.fp = 0

	if err := m.Run(); err != nil {
		return nil, m.reentrantRunErr(err)
	}
	return m.collectReturns(funcType, nret), nil
}

// callPooled runs a mvm function on a runner Machine that has just been
// acquired from a pool. Skips the outer-state save/restore done by
// CallFunc, which would be wasted on a clean Machine. Caller must release
// the Machine back to the pool.
func (m *Machine) callPooled(fval Value, funcType reflect.Type, args []reflect.Value) ([]reflect.Value, error) {
	m.mem = append(m.mem, fval)
	for _, a := range args {
		m.mem = append(m.mem, FromReflect(a))
	}
	narg := funcType.NumIn()
	nret := funcType.NumOut()
	if ip, ok := m.trampolineIP(narg, nret); ok {
		m.ip = ip
	} else {
		callIP := len(m.code)
		m.code = append(m.code, Instruction{Op: Call, A: int32(narg), B: int32(nret)})
		m.code = append(m.code, Instruction{Op: Exit})
		m.ip = callIP
	}
	m.fp = 0

	if err := m.Run(); err != nil {
		return nil, m.reentrantRunErr(err)
	}
	return m.collectReturns(funcType, nret), nil
}

// reentrantRunErr maps the error from a re-entrant Run (one driven by CallFunc
// or callPooled from a native callback) to the value the native caller should
// re-panic. An unrecovered mvm panic leaves m.panicking set after the frame is
// torn down; surface it as a *PanicError so the caller's invokeNative recover
// re-establishes it as an mvm panic that an interpreted recover() can catch.
// Prefer the diagnostic snapshot stageUnwind captured before unwinding (source
// pos + mvm stack); fall back to a raw-only wrapper if none was captured.
func (m *Machine) reentrantRunErr(runErr error) error {
	if m.panicking {
		if m.panicInfo != nil {
			return m.panicInfo
		}
		return &PanicError{Raw: m.panicRaw()}
	}
	return runErr
}

// collectReturns coerces the top nret stack values to funcType's return
// types for delivery to a reflect.MakeFunc caller. Shared by CallFunc and
// callPooled.
func (m *Machine) collectReturns(funcType reflect.Type, nret int) []reflect.Value {
	if nret == 0 {
		return nil
	}
	out := make([]reflect.Value, nret)
	for i := range out {
		rv := m.mem[i].Reflect()
		if !rv.IsValid() {
			// A nil/zero value (e.g. nil error) must be typed for MakeFunc callers.
			rv = reflect.Zero(funcType.Out(i))
		} else if m.mem[i].IsIface() {
			// Unwrap Iface return values so MakeFunc callers see the concrete value.
			// IsIface handles both direct Iface and Iface inside an interface{} slot.
			rv = m.bridgeIface(m.mem[i].IfaceVal(), funcType.Out(i))
		} else if outType := funcType.Out(i); rv.Type() != outType {
			switch {
			case outType.Kind() == reflect.Interface:
				// Interface locals use interface{} internally; convert to the
				// expected interface type (e.g. interface{} -> error) for
				// MakeFunc callers.
				rv = interfaceToInterface(rv, outType)
			case outType.Kind() == reflect.Func && rv.Kind() != reflect.Func:
				// mvm func value (code or Closure) returned where a native func is
				// expected: wrap it callable for the MakeFunc caller.
				rv = m.makeCallFunc(m.mem[i], outType)
			case rv.Type().ConvertibleTo(outType):
				// A loosely-typed numeric return (e.g. an untyped int constant
				// returned from a func(rune) rune callback) must be converted
				// to the declared return type -- which the Go compiler would
				// have done at the return statement -- before delivery to a
				// reflect.MakeFunc caller.
				rv = rv.Convert(outType)
			}
		}
		out[i] = rv
	}
	return out
}

// newGoroutine spawns fval on a new goroutine. It returns panicked=true if the
// spawn raised a (synchronous) panic in the caller -- a nil func value, matching
// Go's "go of nil func value" -- in which case the caller must jump to panicAddr.
func (m *Machine) newGoroutine(fval Value, args []Value, spread bool) (panicked bool) {
	// Inline fast path: resolve addressable struct func fields (mirrors Call opcode).
	if fval.ref.Kind() == reflect.Func && fval.ref.CanAddr() {
		fval = m.resolveFuncField(fval)
	}
	rv := fval.ref
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Func {
		// Native Go function: call via reflection in a plain goroutine.
		rv = Exportable(rv)
		in := make([]reflect.Value, len(args))
		for i, a := range args {
			in[i] = a.Reflect()
		}
		coerceInterfaceArgs(in, rv.Type())
		m.wrapFuncArgs(in, args, rv.Type())
		if spread {
			in[len(in)-1] = unwrapVariadicIface(in[len(in)-1])
			go func() { rv.CallSlice(in) }()
		} else {
			go func() { rv.Call(in) }()
		}
		return false
	}

	// Resolve VM function address and closure heap.
	var nip int
	var heap []*Value
	if isNum(fval.ref.Kind()) {
		nip = int(fval.num)
	} else if clo, ok := fval.ref.Interface().(Closure); ok {
		nip, heap = clo.Code, clo.Heap
	} else if iv, ok := fval.ref.Interface().(int); ok {
		nip = iv
	} else {
		nip = int(fval.num)
	}
	if nip == nilFuncAddr {
		// go of a nil func: panic synchronously, else the spawned goroutine
		// jumps to 0 and re-runs main, spawning ever more goroutines.
		m.raiseNilDeref()
		return true
	}

	m.ensureSharedTables()
	m.ensurePooledCode()

	// Pre-build the call frame: [fval, args..., deferHead, retIP+info, prevFP].
	// The return address targets the Exit sentinel baked in at trampBase+2.
	narg := len(args)
	frameBase := narg + 4
	exitAddr := m.pooledCodeBase + 2
	mem := make([]Value, frameBase, frameBase+16)
	mem[0] = fval
	copy(mem[1:], args)
	// mem[narg+1] is zero (deferHead)
	mem[narg+2] = Value{num: packRetIP(exitAddr, 0, frameBase)}
	mem[narg+3] = Value{num: 0} // prevFP = 0

	// Arm the sink if EnableGoroutineFaults didn't (raw vm.Machine.Run); flag that
	// a goroutine now exists so channel waits start watching for a fault.
	if m.fault == nil {
		m.fault = newGoroutineFault(m.err, m.faultContinue)
	}
	m.fault.spawned.Store(true)
	child := &Machine{
		globals:         m.globals,
		code:            m.pooledCode,
		baseCodeLen:     m.pooledCodeBase,
		trampBase:       m.pooledCodeBase,
		pooledCode:      m.pooledCode,
		pooledCodeBase:  m.pooledCodeBase,
		heap:            heap,
		ip:              nip,
		fp:              frameBase,
		mem:             mem,
		in:              m.in,
		out:             m.out,
		err:             m.err,
		debugIn:         m.debugIn,
		debugOut:        m.debugOut,
		MethodNames:     m.MethodNames,
		MethodFuncTypes: m.MethodFuncTypes,
		funcFields:      m.funcFields,
		funcWrappers:    m.funcWrappers,
		typesByRtype:    m.typesByRtype,
		fault:           m.fault,
		debugInfoFn:     m.debugInfoFn,
		PanicNilCompat:  m.PanicNilCompat,
	}
	go func() {
		// An unrecovered panic in an interpreted goroutine returns from Run as an
		// error. Go would crash the process; record it so main surfaces it.
		if err := child.Run(); err != nil && m.fault != nil {
			m.fault.record(err)
		}
	}()
	return false
}

func clearValue(rv reflect.Value) {
	if rv = unwrapIface(rv); rv.IsValid() {
		rv.Clear()
	}
}

func (m *Machine) execBuiltinDeferred(op Op, base, narg int, mem []Value) {
	switch op {
	case Println, Print:
		args := make([]any, narg)
		for i := range narg {
			args[i] = mem[base+i].Interface()
		}
		if op == Println {
			_, _ = fmt.Fprintln(m.out, args...)
		} else {
			_, _ = fmt.Fprint(m.out, args...)
		}
	case ChanClose:
		mem[base].ref.Close()
	case DeleteMap:
		mem[base].ref.SetMapIndex(m.mapKey(mem[base].ref.Type().Key(), mem[base+1]), reflect.Value{})
	case CopySlice:
		reflect.Copy(mem[base].ref, mem[base+1].ref)
	case Clear:
		clearValue(mem[base].Reflect())
	default:
		panic(fmt.Sprintf("unsupported deferred builtin opcode: %v", op))
	}
}

func snapshotArg(v Value) Value {
	if v.ref.CanAddr() {
		if isNum(v.ref.Kind()) {
			v.ref = reflect.Zero(v.ref.Type())
		} else {
			v.ref = reflect.ValueOf(v.ref.Interface())
		}
	}
	return v
}

// snapshotFuncVal captures a deferred func value at the defer statement by
// detaching it from an aliased slot (a func field or func var), so a later write
// that nils the slot is not seen when the call runs. The copy preserves type
// (New().Set, not ValueOf(.Interface())) to keep a VM closure's interface{int}
// encoding intact; Exportable allows copying from an unexported field.
func snapshotFuncVal(v Value) Value {
	if k := v.ref.Kind(); (k == reflect.Func || k == reflect.Interface) && v.ref.CanAddr() {
		nv := reflect.New(v.ref.Type()).Elem()
		nv.Set(Exportable(v.ref))
		v.ref = nv
	}
	return v
}

func detachByValueArgs(args []Value) {
	for i := range args {
		r := args[i].ref
		if !r.IsValid() {
			continue
		}
		k := r.Kind()
		if k != reflect.Struct && k != reflect.Array {
			continue
		}
		nv := reflect.New(r.Type()).Elem()
		nv.Set(Exportable(r))
		args[i].ref = nv
	}
}

// HeapSize returns the number of heap-allocated cells currently held by
// the machine's active closure context. Typically 0 between Run() calls;
// nonzero only mid-execution. Reported by FormatStats when nonzero.
func (m *Machine) HeapSize() int { return len(m.heap) }

// Top returns (but not remove)  the value on the top of machine stack.
func (m *Machine) Top() (v Value) {
	if l := len(m.mem); l > 0 {
		v = m.mem[l-1]
	} else if l := len(m.globals); l > 0 {
		// When the stack is empty, return the last global.
		v = m.globals[l-1]
	}
	return v
}

// StackLen returns the number of values left on the data stack.
func (m *Machine) StackLen() int { return len(m.mem) }

// PopExit removes the last machine code instruction if is Exit.
func (m *Machine) PopExit() {
	if l := len(m.code); l > 0 && m.code[l-1].Op == Exit {
		m.code = m.code[:l-1]
	}
}

// TrimCode drops m.code back to length n (a no-op if it is already <= n).
func (m *Machine) TrimCode(n int) {
	if n >= 0 && n < len(m.code) {
		m.code = m.code[:n]
		m.codeEpoch++
	}
}

// Vstring returns the string representation of a list of values.
func Vstring(lv []Value) string {
	var sb strings.Builder
	sb.WriteByte('[')
	appendValues(&sb, lv)
	sb.WriteByte(']')
	return sb.String()
}

func appendValues(sb *strings.Builder, lv []Value) {
	for i, v := range lv {
		if i > 0 {
			sb.WriteByte(' ')
		}
		if !v.ref.IsValid() {
			fmt.Fprintf(sb, "<%d>", v.num)
		} else {
			fmt.Fprintf(sb, "%v", v.Interface())
		}
	}
}

// funcValueUnsafe reads the funcval pointer out of an addressable func slot.
func funcValueUnsafe(fv reflect.Value) unsafe.Pointer {
	return *(*unsafe.Pointer)(fv.Addr().UnsafePointer())
}

func funcValuePtr(fv reflect.Value) uintptr { return uintptr(funcValueUnsafe(fv)) }

func funcDataPtr(w reflect.Value) uintptr {
	h := reflect.New(w.Type()).Elem()
	h.Set(w)
	return funcValuePtr(h)
}

func forceSettable(fv reflect.Value) reflect.Value {
	if !fv.CanSet() && fv.CanAddr() {
		fv = reflect.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem()
	}
	return fv
}

// methodExprBypass reports whether rv is one of this Machine's method-expression
// wrappers (and dispatch is allowed), returning the method body code address.
// The atomic gate keeps the cost at one load for programs with no method exprs.
func (m *Machine) methodExprBypass(rv reflect.Value, allowed bool) (int, bool) {
	if !allowed || m.funcFields == nil || !m.funcFields.hasMethodExpr.Load() {
		return 0, false
	}
	if rv.Kind() != reflect.Func || rv.IsNil() {
		return 0, false
	}
	return m.funcFields.methodExprCode(funcDataPtr(rv))
}

func (m *Machine) resolveFuncField(v Value) Value {
	if v.ref.Kind() == reflect.Func && v.ref.CanAddr() && !v.ref.IsNil() && m.funcFields != nil {
		if pf, ok := m.funcFields.get(funcValuePtr(v.ref)); ok {
			if m.funcFields.hasMethodExpr.Load() && pf.ref.IsValid() && pf.ref.CanInterface() {
				if _, isME := pf.ref.Interface().(methodExpr); isME {
					return v // keep the wrapper; the Call opcode handles method-expr dispatch
				}
			}
			return pf
		}
	}
	return v
}

func (m *Machine) setGoFuncField(fv, gf reflect.Value, val Value) {
	fv.Set(gf)
	fp := funcValueUnsafe(fv)
	if fp == nil {
		return
	}
	if m.funcFields == nil {
		m.funcFields = newFuncFieldsTable()
	}
	if gen := m.funcFields.setStrongKeep(uintptr(fp), val); gen != 0 {
		m.funcFields.cleanupStrong(fp, gen)
	}
}

func (m *Machine) setFuncField(fv reflect.Value, val Value) {
	if !val.ref.IsValid() {
		fv.Set(reflect.Zero(fv.Type()))
		return
	}
	val.ref = Exportable(val.ref)
	if pf, ok := val.ref.Interface().(MvmFunc); ok && fv.CanAddr() {
		m.setGoFuncField(fv, pf.GF, pf.Val)
		return
	}
	if fv.Kind() == reflect.Func && fv.CanAddr() {
		if gf := m.wrapForFunc(val, fv.Type()); gf.IsValid() {
			m.setGoFuncField(fv, gf, val)
		}
		return
	}
	if isNum(fv.Kind()) && isNum(val.ref.Kind()) {
		// Avoid reflect.Set type-mismatch when field and value are different numeric kinds.
		setNumReflect(fv, val.num)
		return
	}
	if fv.Kind() == reflect.Interface {
		if w, ok := m.unboxIfaceFor(val, fv.Type()); ok {
			fv.Set(w)
			return
		}
	}
	src := val.Reflect()
	if fv.Kind() == reflect.Interface && src.Kind() == reflect.Interface && src.Type() != fv.Type() {
		src = interfaceToInterface(src, fv.Type())
	}
	// Before AssignableTo below, which infinite-loops on recursive cross-universe rtypes.
	if src.IsValid() && src.Type() != fv.Type() {
		if r, ok := reinterpretSameLayout(src, fv.Type()); ok {
			fv.Set(r)
			return
		}
	}
	if src.IsValid() && src.Kind() == fv.Kind() && src.Type() != fv.Type() &&
		!src.Type().AssignableTo(fv.Type()) && src.Type().ConvertibleTo(fv.Type()) {
		src = src.Convert(fv.Type())
	}
	fv.Set(src)
}

// reinterpretSameLayout coerces src to dst when file-by-file compilation built one
// Go type as two distinct rtypes (a methodless forward clone vs the canonical).
// Same kind+size+String means identical layout, so the reinterpret is sound.
// Only reached on the path that would otherwise panic in reflect.Set.
func reinterpretSameLayout(src reflect.Value, dst reflect.Type) (reflect.Value, bool) {
	st := src.Type()
	if st.Kind() != dst.Kind() || st.Size() != dst.Size() || st.String() != dst.String() {
		return reflect.Value{}, false
	}
	tmp := reflect.New(st)
	tmp.Elem().Set(src)
	return reflect.NewAt(dst, tmp.UnsafePointer()).Elem(), true
}

// unboxIfaceFor converts a boxed mvm Iface to a reflect value assignable to
// the interface type dst.
// Native concretes unwrap to raw Go values; interpreted concretes bridge for
// non-empty targets.
// ok is false when val is not a boxed Iface for an interface dst, or when the
// value must stay Iface-boxed to keep its interpreted type info.
func (m *Machine) unboxIfaceFor(val Value, dst reflect.Type) (reflect.Value, bool) {
	if dst.Kind() != reflect.Interface || !val.IsIface() {
		return reflect.Value{}, false
	}
	iv := val.IfaceVal()
	// Unwrap Iface for native types so reflect-based code sees raw Go values.
	keepInterpreted := iv.Typ.Name != "" && iv.Typ.Name != iv.Typ.Rtype.Name()
	// Also keep a pointer to an interpreted interface.
	if !keepInterpreted && iv.Typ.Rtype.Kind() == reflect.Pointer &&
		iv.Typ.ElemType != nil && iv.Typ.ElemType.Rtype.Kind() == reflect.Interface &&
		len(iv.Typ.ElemType.IfaceMethods) > 0 {
		keepInterpreted = true
	}
	if iv.Typ.Rtype.Kind() == reflect.Func && !keepInterpreted {
		// Wrap as a callable typed with the named func rtype: method-bearing
		// ones carry their (synth-attached) methods natively, and a nil value
		// stays a TYPED nil so fmt still dispatches String (gc semantics).
		return m.wrapForFunc(iv.Val, iv.Typ.Rtype), true
	}
	if len(iv.Typ.Methods) == 0 && !keepInterpreted {
		return numReflect(iv.Typ.Rtype, iv.Val), true
	}
	// Native interface target: bridge so the mvm-typed concrete value is assignable.
	if dst.NumMethod() > 0 {
		if w := m.bridgeIface(iv, dst); w.IsValid() {
			return w, true
		}
	} else if !keepInterpreted {
		// Empty-interface target: store the raw synth-typed concrete (a real eface),
		// not the boxed Iface struct, so native reflect/unsafe reads see a Go value.
		// An empty interface has no methods to bridge; the synth rtype carries the
		// concrete's methods and typeByRtype recovers the interpreted *Type.
		return numReflect(iv.Typ.Rtype, iv.Val), true
	}
	return reflect.Value{}, false
}

func (m *Machine) retypeFuncSlot(slot *Value, funcType reflect.Type) {
	if slot.ref.IsValid() && slot.ref.Type() == funcType && slot.ref.CanAddr() {
		return
	}
	orig := *slot
	slot.ref = reflect.New(funcType).Elem()
	slot.num = 0
	if !nilEqual(orig) {
		m.assignSlot(slot, orig)
	}
}

// setCell writes into a closure heap cell, coercing an untyped nil to the cell's
// typed zero (as assignSlot does for locals) so a later append/index on the cell
// does not read a zero Value.
// The cell always gets fresh addressable cell-owned storage (as HeapAlloc):
// it detaches a src owned elsewhere, and a non-addressable ref would make the
// next &var alias a detached copy and lose writes through it.
// Fresh (not in-place): Values already read from the cell alias its old
// storage and must keep snapshot semantics.
// Known gap: &var taken BEFORE a reassignment misses later writes.
func setCell(cell *Value, src Value) {
	if !src.ref.IsValid() && cell.ref.IsValid() {
		src.ref = reflect.Zero(cell.ref.Type())
	}
	if src.ref.IsValid() {
		rv := reflect.New(src.ref.Type()).Elem()
		if isNum(src.ref.Kind()) {
			setNumReflect(rv, src.num)
		} else {
			rv.Set(Exportable(src.ref))
		}
		src.ref = rv
	}
	*cell = src
}

func (m *Machine) assignSlot(dst *Value, src Value) {
	if pf := m.resolveFuncField(src); pf != src {
		*dst = pf
		return
	}
	// Promoting a captured local to a heap cell stores the cell pointer into its slot.
	// That is a raw overwrite, never a reflect.Set into the slot's old typed value.
	if src.ref.Kind() == reflect.Pointer {
		if _, ok := src.ref.Interface().(*Value); ok {
			*dst = src
			return
		}
	}
	if dst.ref.Kind() == reflect.Func && dst.ref.CanAddr() {
		dst.num = src.num
		if gf := m.wrapForFunc(src, dst.ref.Type()); gf.IsValid() {
			m.setGoFuncField(dst.ref, gf, src)
		}
		return
	}
	if isNum(src.ref.Kind()) {
		dst.num = src.num
		switch {
		case !dst.ref.CanSet():
			if dst.ref.IsValid() && isNum(dst.ref.Kind()) {
				break
			}
			dst.ref = src.ref
		case isNum(dst.ref.Kind()):
			setNumReflect(dst.ref, src.num)
		default:
			dst.ref.Set(src.Reflect())
		}
		return
	}
	if !src.ref.IsValid() && dst.ref.IsValid() && isNum(dst.ref.Kind()) {
		dst.num = src.num
		if dst.ref.CanSet() {
			setNumReflect(dst.ref, src.num)
		}
		return
	}
	if !dst.ref.CanSet() {
		// Adopting a settable ref would alias storage owned elsewhere (e.g. a
		// global whose value was returned by a callee); a later assignment to
		// this slot would then write through it. Detach into fresh storage.
		// Reached when the slot was never materialized by New, e.g. a named
		// return whose first executed assignment is not the first textual one.
		if src.ref.CanSet() {
			r := reflect.New(src.ref.Type()).Elem()
			r.Set(src.ref)
			src.ref = r
		}
		dst.ref = src.ref
		return
	}
	s := src.ref
	if !s.IsValid() {
		s = reflect.Zero(dst.ref.Type())
	} else if dst.ref.Kind() == reflect.Interface && isNilable(s) && s.IsNil() {
		// Avoid creating a typed nil inside an interface{} slot.
		s = reflect.Zero(dst.ref.Type())
	}
	// A value read from an unexported field carries reflect's read-only flag, strip it.
	s = Exportable(s)
	dst.ref.Set(adoptNamedType(s, dst.ref.Type()))
}

// adoptNamedType converts v to dt when only the named identity differs:
// an untyped-const load carries the native layout type, the destination a
// defined type's carrier rtype (`v = "lit"`, a map[NamedString]V literal key).
func adoptNamedType(v reflect.Value, dt reflect.Type) reflect.Value {
	if !v.IsValid() {
		return v
	}
	if vt := v.Type(); vt != dt && v.Kind() == dt.Kind() &&
		dt.Kind() != reflect.Interface && vt.ConvertibleTo(dt) {
		return v.Convert(dt)
	}
	return v
}

func setNumReflect(rv reflect.Value, num uint64) {
	switch rv.Kind() {
	case reflect.Bool:
		rv.SetBool(num != 0)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		rv.SetInt(int64(num))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		rv.SetUint(num)
	case reflect.Float32, reflect.Float64:
		rv.SetFloat(math.Float64frombits(num))
	}
}

func numSet(dst reflect.Value, src Value) {
	if isNum(dst.Kind()) && isNum(src.ref.Kind()) {
		setNumReflect(dst, src.num)
		return
	}
	s := src.Reflect()
	if !s.IsValid() {
		s = reflect.Zero(dst.Type())
	}
	dst.Set(adoptNamedType(s, dst.Type()))
}

func derefArray(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Pointer && v.Elem().Kind() == reflect.Array {
		return v.Elem()
	}
	return v
}

func numReflect(t reflect.Type, src Value) reflect.Value {
	if isNum(t.Kind()) && isNum(src.ref.Kind()) {
		r := reflect.New(t).Elem()
		setNumReflect(r, src.num)
		return r
	}
	return src.Reflect()
}

func mapKeyReflect(t reflect.Type, src Value) reflect.Value {
	rv := adoptNamedType(numReflect(t, src), t)
	if !rv.IsValid() {
		// Bare nil key literal: use the key type's zero value.
		return reflect.Zero(t)
	}
	if rv.IsValid() && rv.Kind() == reflect.Interface && t.Kind() == reflect.Interface &&
		rv.Type() != t && !rv.Type().AssignableTo(t) {
		if rv.IsNil() {
			return reflect.Zero(t)
		}
		if dv := rv.Elem(); dv.Type().AssignableTo(t) {
			return dv
		}
	}
	return rv
}

// mapKey prepares src as a reflect map key. An Iface key into an interface-typed
// map is unwrapped to its native concrete; the boxed struct would never collide.
func (m *Machine) mapKey(t reflect.Type, src Value) reflect.Value {
	if t.Kind() == reflect.Interface && src.IsIface() {
		if rv, ok := m.unboxIfaceFor(src, t); ok {
			return rv
		}
	}
	return mapKeyReflect(t, src)
}

func (m *Machine) bridgeArgs(in []reflect.Value, funcType reflect.Type, fn reflect.Value) []ifaceWriteback {
	var wb []ifaceWriteback
	var retyped []int // args relabeled to *synthIface; excluded from the unbox walk
	for i, rv := range in {
		if !rv.IsValid() || rv.Type() != ifaceRtype {
			if rv.IsValid() && rv.Kind() == reflect.Interface && !rv.IsNil() &&
				rv.Elem().Type() == ifaceRtype {
				rv = rv.Elem()
			} else {
				continue
			}
		}
		ifc := rv.Interface().(Iface)
		if st := m.bridgePtrToIface(ifc, ifc.Val.Reflect(), fn); st.IsValid() {
			in[i] = st
			retyped = append(retyped, i)
			if !st.IsNil() && isSynthIfaceWriteTargetFunc(fn) {
				ptr := st.UnsafePointer()
				wb = append(wb, ifaceWriteback{ptr: ptr, st: st.Type().Elem(), before: *(*[2]uintptr)(ptr)})
			}
			continue
		}
		targetType := paramTypeFor(funcType, i)
		if targetType == nil {
			targetType = AnyRtype
		}
		in[i] = m.bridgeIface(ifc, targetType)
	}
	// Iface boxes nested in a composite arg leak into native reflect walks.
	// Skip args relabeled to *synthIface: their pointee is the caller's interface
	// cell in mvm form, which deepUnboxIface would misread as Go iface layout.
	for i, rv := range in {
		if slices.Contains(retyped, i) {
			continue
		}
		if w, ch := m.deepUnboxIface(rv, 0, 0, nil); ch {
			in[i] = w
		}
	}
	return wb
}

func paramTypeFor(funcType reflect.Type, i int) reflect.Type {
	numIn := funcType.NumIn()
	switch {
	case funcType.IsVariadic() && i >= numIn-1:
		return funcType.In(numIn - 1).Elem()
	case i < numIn:
		return funcType.In(i)
	default:
		return nil
	}
}

func coerceInterfaceArgs(in []reflect.Value, funcType reflect.Type) {
	for i, rv := range in {
		if rv.IsValid() && !rv.CanInterface() {
			rv = Exportable(rv)
			in[i] = rv
		}
		paramType := paramTypeFor(funcType, i)
		if paramType == nil {
			continue
		}
		if !rv.IsValid() {
			in[i] = reflect.Zero(paramType)
			continue
		}
		if rv.Type() == paramType {
			continue
		}
		if rv.Kind() == reflect.Interface && rv.IsNil() && paramType.Kind() == reflect.Interface {
			in[i] = reflect.Zero(paramType)
			continue
		}
		if rv.Kind() == reflect.Interface && !rv.IsNil() {
			in[i] = rv.Elem()
		} else if rv.Kind() == paramType.Kind() || (isNum(rv.Kind()) && isNum(paramType.Kind())) {
			// Convert named types or across numeric kinds (e.g. int to time.Duration).
			in[i] = rv.Convert(paramType)
		}
	}
}

func (m *Machine) wrapFuncArgs(in []reflect.Value, args []Value, funcType reflect.Type) {
	for i := range in {
		paramType := paramTypeFor(funcType, i)
		if paramType == nil || paramType.Kind() != reflect.Func {
			continue
		}
		if in[i].IsValid() && in[i].Type() == paramType {
			continue
		}
		if gf := m.wrapForFunc(args[i], paramType); gf.IsValid() {
			in[i] = gf
		}
	}
}

func (m *Machine) makeMethodCell(ifc Iface, method Method) (*Value, Value) {
	codeAddr := int(m.globals[method.Index].num)
	cell := new(Value)
	*cell = ifc.Val
	if path := method.Path; path != nil {
		rv := reflect.Indirect(ifc.Val.Reflect())
		for _, idx := range path {
			if rv.Kind() == reflect.Pointer {
				rv = rv.Elem()
			}
			rv = rv.Field(idx)
		}
		if method.PtrRecv && rv.CanAddr() {
			rv = Exportable(rv).Addr()
		}
		*cell = FromReflect(rv)
	}
	return cell, Value{ref: reflect.ValueOf(Closure{Code: codeAddr, Heap: []*Value{cell}})}
}

// MakeMethodCallable returns a mvm func Value suitable for Machine.CallFunc.
func (m *Machine) MakeMethodCallable(ifc Iface, method Method) Value {
	_, fval := m.makeMethodCell(ifc, method)
	return fval
}

// MethodByName returns the first resolved method named `name` reachable from t.
// For pointer types, methods declared on the element type are
// also searched. Returns (Method, true) on hit.
func (m *Machine) MethodByName(t *Type, name string) (Method, bool) {
	// ifaceMethodTypes already walks the Base chain.
	types, n := ifaceMethodTypes(t)
	for _, mt := range types[:n] {
		for id, method := range mt.Methods {
			if method.Index < 0 || id >= len(m.MethodNames) {
				continue
			}
			if m.MethodNames[id] == name {
				return method, true
			}
		}
	}
	return Method{}, false
}
