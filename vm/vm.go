// Package vm implement a stack based virtual machine.
package vm

import (
	"fmt" // for tracing only
	"io"
	"iter"
	"log"  // for tracing only
	"math" // for float arithmetic
	"math/bits"
	"os"
	"reflect"
	"strings"
	"unsafe" // to allow setting unexported struct fields //nolint:depguard
)

const debug = false

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
	AddrLocal              // -- &local ; push pointer to mem[fp-1+$1]; promotes slot to addressable storage on first use so writes via the pointer propagate back
	Append                 // slice [v0..vn-1] -- slice' ; append $0 values to slice
	AppendSlice            // slice [v0..vn-1] -- slice' ; pack $0 values into []T, reflect.AppendSlice; elem type at mem[$1]; $0=0 means spread mode: append(a, b...)
	Call                   // f [a1 .. ai] -- [r1 .. rj] ; r1, ... = prog[f](a1, ...); B bit 15 = spread flag
	CallImm                // [a1 .. ai] -- [r1 .. rj] ; $1=dataIdx of func, $2=narg<<16|nret
	Cap                    // -- x ; x = cap(mem[sp-$0])
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
	MkMap                  // -- map ; create map[K]V, key type at mem[$0], val type at mem[$1]
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

	GetGlobal  // -- value ; value = mem[$1] (global variable, syncs num from ref if needed)
	GetLocal   // -- value ; value = mem[$1+fp-1] (local variable, no scope check)
	NextLocal  // -- ; iterator next, set K (local scope); like Next but scope is always Local
	Next2Local // -- ; iterator next, set K V (local scope); like Next2 but scope is always Local

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
	GetLocalLowerIntImmJumpFalse // -- ; if local >= imm { ip += $1 } ; $2 = localOff<<16 | imm&0xFFFF
	GetLocalLowerIntImmJumpTrue  // -- ; if local < imm { ip += $1 } ; $2 = localOff<<16 | imm&0xFFFF
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

	panicking bool  // true while unwinding due to panic
	panicVal  Value // value passed to panic()

	baseCodeLen int // len(code) before Run() appends sentinel instructions

	funcFields          map[uintptr]Value
	funcFieldsByFuncPtr map[uintptr]Value

	in       io.Reader // machine standard input (nil = os.Stdin)
	out, err io.Writer // machine standard output and error

	MethodNames []string // names by global method ID

	debugInfoFn func() *DebugInfo // builds DebugInfo on demand (breaks vm->comp cycle)
	debugIn     io.Reader         // debug command input (nil = os.Stdin)
	debugOut    io.Writer         // debug output (nil = os.Stderr)
	stepping    bool              //nolint:unused // when true, trap after every instruction (planned)
	trapOrig    int               // ip to resume after Trap
}

// NewMachine returns a pointer on a new Machine.
func NewMachine() *Machine { return &Machine{in: os.Stdin, out: os.Stdout, err: os.Stderr} }

// SetIO sets the I/O streams for the machine.
func (m *Machine) SetIO(in io.Reader, out, err io.Writer) { m.in = in; m.out = out; m.err = err }

// Out returns the machine's standard output writer.
func (m *Machine) Out() io.Writer { return m.out }

// SetDebugInfo registers a function that builds DebugInfo on demand.
func (m *Machine) SetDebugInfo(fn func() *DebugInfo) { m.debugInfoFn = fn }

// SetDebugIO sets the I/O streams for the interactive debug mode.
func (m *Machine) SetDebugIO(in io.Reader, out io.Writer) {
	m.debugIn = in
	m.debugOut = out
}

// posPrefix returns a "file:line:col: " string for the given source position,
// or "" if debug info is unavailable.
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

func packRetIP(retIP, nret, frameBase int) uint64 {
	return uint64(uint32(retIP)) | uint64(nret)<<32 | uint64(frameBase)<<48 //nolint:gosec
}

func growStack(mem []Value, sp, need int) []Value {
	n := max(len(mem)*2, sp+1+need+256)
	newMem := make([]Value, n)
	copy(newMem, mem[:sp+1])
	return newMem
}

// Run runs a program.
func (m *Machine) Run() (err error) {
	// Append sentinel instructions so negative-IP handlers become normal opcodes.
	sentBase := len(m.code)
	m.baseCodeLen = sentBase
	m.code = append(m.code, Instruction{Op: DeferRet}, Instruction{Op: PanicUnwind}, Instruction{Op: Exit})
	deferRetAddr := sentBase
	panicAddr := sentBase + 1
	deferRetBits := uint64(deferRetAddr) //nolint:gosec

	mem, ip, fp := m.mem, m.ip, m.fp
	sp := len(mem) - 1
	// Extend mem to full capacity so all writes up to cap are in bounds.
	mem = mem[:cap(mem)]

	defer func() {
		m.mem, m.ip, m.fp = mem[:sp+1], ip, fp
		m.code = m.code[:sentBase]
	}()

	for {
		c := m.code[ip] // current instruction
		if debug {
			log.Printf("ip:%-3d sp:%-3d fp:%-3d op:[%-20v] mem:%v\n", ip, sp, fp, c, Vstring(mem[:sp+1]))
		}
		switch c.Op {
		case Addr:
			v := mem[sp]
			switch {
			case v.ref.CanAddr():
				mem[sp] = Value{ref: v.ref.Addr()}
			case isNum(v.ref.Kind()):
				// Materialize via Reflect() to get an addressable value, then take its address.
				mem[sp] = Value{ref: v.Reflect().Addr()}
			case v.IsIface():
				// Iface wrapper: allocate *interface{} and store the unwrapped value.
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
			narg := int(c.A)
			fval := mem[sp-narg]
			// Inline fast path: only call resolveFuncField for addressable Func fields.
			if fval.ref.Kind() == reflect.Func && fval.ref.CanAddr() {
				fval = m.resolveFuncField(fval)
			}
			prevHeap := m.heap
			var nip int
			if isNum(fval.ref.Kind()) {
				// Plain int code address stored inline in num.
				nip = int(fval.num) //nolint:gosec
				m.heap = nil
			} else if clo, ok := fval.ref.Interface().(Closure); ok {
				nip = clo.Code
				m.heap = clo.Heap
			} else if iv, ok := fval.ref.Interface().(int); ok {
				// Function variable slot holds a plain code address boxed as interface{}.
				nip = iv
				m.heap = nil
			} else {
				rv := fval.ref
				// Method-call sentinel: IfaceCall placed a boundProxyCall on
				// the stack because the target method has registered arg
				// proxies. Unwrap it and thread the method identity to
				// bridgeArgs so RegisterArgProxyMethod factories can fire.
				var proxyRecvType reflect.Type
				var proxyMethod string
				if rv.IsValid() && rv.Type() == boundProxyCallRtype {
					bpc := rv.Interface().(boundProxyCall)
					rv = bpc.Fn
					proxyRecvType = bpc.RecvType
					proxyMethod = bpc.Method
				}
				if rv.Kind() == reflect.Interface && !rv.IsNil() {
					rv = rv.Elem()
				}
				if rv.Kind() == reflect.Func {
					funcType := rv.Type()
					in := make([]reflect.Value, narg)
					for i := range in {
						in[i] = mem[sp-narg+1+i].Reflect()
					}
					m.bridgeArgs(in, funcType, rv.Pointer(), proxyRecvType, proxyMethod)
					coerceInterfaceArgs(in, funcType)
					m.wrapFuncArgs(in, mem[sp-narg+1:sp+1], funcType)
					sp -= narg + 1
					// For spread calls (f(s...)), unwrap Iface values inside
					// the variadic slice and use CallSlice.
					var out []reflect.Value
					if c.B&CallSpreadFlag != 0 {
						last := in[narg-1]
						if last.Kind() == reflect.Interface && !last.IsNil() {
							last = last.Elem()
						}
						for i := range last.Len() {
							elem := last.Index(i)
							if elem.Kind() == reflect.Interface && !elem.IsNil() {
								if ifc, ok := elem.Interface().(Iface); ok {
									elem.Set(ifc.Val.Reflect())
								}
							}
						}
						in[narg-1] = last
						out = rv.CallSlice(in)
					} else {
						out = rv.Call(in)
					}
					for _, v := range out {
						if sp+1 >= len(mem) {
							mem = growStack(mem, sp, 1)
						}
						sp++
						mem[sp] = FromReflect(v)
					}
					break
				}
				nip = int(fval.num) //nolint:gosec
				m.heap = nil
			}
			nret := int(c.B &^ CallSpreadFlag)
			fpVal := uint64(fp) //nolint:gosec
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
			ip = nip
			continue
		case CallImm:
			narg := int(c.B) >> 16
			nret := int(c.B) & 0xFFFF
			fpVal := uint64(fp) //nolint:gosec
			if m.heap != nil {
				// preserve caller closure context
				m.heapFrames = append(m.heapFrames, m.heap)
				fpVal |= heapSavedFlag
				m.heap = nil
			}
			nip := int(m.globals[int(c.A)].num) //nolint:gosec
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
			ip = nip
			continue
		case Deref:
			r := mem[sp].ref.Elem()
			v := Value{ref: r}
			if isNum(r.Kind()) {
				v.num = numBits(r)
			}
			mem[sp] = v
		case DerefSet:
			ptr := mem[sp-1]
			val := mem[sp]
			elem := ptr.ref.Elem()
			numSet(elem, val)
			sp -= 2
			// Update the .num cache of any frame slot whose ref shares the
			// same underlying address, so fused GetLocal*Imm instructions and
			// num-first reads see the updated value. Scans the current frame
			// including its params below fp (frameBase = distance from fp to
			// the start of args).
			if isNum(elem.Kind()) {
				addr := elem.UnsafeAddr()
				n := numBits(elem)
				base := 0
				if fp >= 2 {
					base = fp - int(mem[fp-2].num>>48)
					if base < 0 {
						base = 0
					}
				}
				for i := base; i <= sp; i++ {
					if mem[i].ref.CanAddr() && mem[i].ref.UnsafeAddr() == addr {
						mem[i].num = n
					}
				}
			}
		case AddrLocal:
			slot := &mem[int(c.A)+fp-1]
			if !slot.ref.CanAddr() {
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
		case GetLocalAddIntImm:
			sp++
			v := mem[int(c.A)+fp-1]
			v.num = uint64(int(v.num) + int(c.B)) //nolint:gosec
			v.ref = zint
			mem[sp] = v
		case GetLocalSubIntImm:
			sp++
			v := mem[int(c.A)+fp-1]
			v.num = uint64(int(v.num) - int(c.B)) //nolint:gosec
			v.ref = zint
			mem[sp] = v
		case GetLocalMulIntImm:
			sp++
			v := mem[int(c.A)+fp-1]
			v.num = uint64(int(v.num) * int(c.B)) //nolint:gosec
			v.ref = zint
			mem[sp] = v
		case GetLocalLowerIntImm:
			sp++
			mem[sp] = boolVal(int(mem[int(c.A)+fp-1].num) < int(c.B)) //nolint:gosec
		case GetLocalLowerUintImm:
			sp++
			mem[sp] = boolVal(uint(mem[int(c.A)+fp-1].num) < uint(int(c.B))) //nolint:gosec
		case GetLocalGreaterIntImm:
			sp++
			mem[sp] = boolVal(int(mem[int(c.A)+fp-1].num) > int(c.B)) //nolint:gosec
		case GetLocalGreaterUintImm:
			sp++
			mem[sp] = boolVal(uint(mem[int(c.A)+fp-1].num) > uint(int(c.B))) //nolint:gosec
		case GetLocalReturn:
			sp++
			mem[sp] = mem[int(c.A)+fp-1]
			retIPInfo := mem[fp-2].num
			frameBase := int(retIPInfo >> 48)
			ip = int(int32(retIPInfo)) //nolint:gosec
			ofp := fp
			fpVal := mem[fp-1].num
			if fpVal&heapSavedFlag != 0 {
				fp = int(fpVal &^ heapSavedFlag) //nolint:gosec
				top := len(m.heapFrames) - 1
				m.heap = m.heapFrames[top]
				m.heapFrames[top] = nil // clear for GC
				m.heapFrames = m.heapFrames[:top]
			} else {
				fp = int(fpVal) //nolint:gosec
				m.heap = nil
			}
			newBase := ofp - frameBase
			nret := int((retIPInfo >> 32) & 0xFFFF)
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
			if int(mem[sp+1].num) >= int(c.B) { //nolint:gosec
				ip += int(c.A)
				continue
			}
		case LowerIntImmJumpTrue:
			sp--
			if int(mem[sp+1].num) < int(c.B) { //nolint:gosec
				ip += int(c.A)
				continue
			}
		case GetLocalLowerIntImmJumpFalse:
			if int(mem[int(c.B>>16)+fp-1].num) >= int(int16(c.B)) { //nolint:gosec
				ip += int(c.A)
				continue
			}
		case GetLocalLowerIntImmJumpTrue:
			if int(mem[int(c.B>>16)+fp-1].num) < int(int16(c.B)) { //nolint:gosec
				ip += int(c.A)
				continue
			}
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
			mem[int(c.A)+fp-1] = NewValue(m.globals[int(c.B)].ref.Type())
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
			idx := sp - int(c.B)
			v := mem[idx]
			dstType := m.globals[int(c.A)].ref.Type()
			dstKind := dstType.Kind()
			if !v.ref.IsValid() {
				// nil source: zero value of destination type.
				if dstKind != reflect.Interface {
					mem[idx] = FromReflect(reflect.Zero(dstType))
				}
				break
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
					// float -> int: truncate.
					f := math.Float64frombits(bits)
					bits = uint64(int64(f)) //nolint:gosec
				case isFloat(dstKind):
					// int -> float.
					if srcKind >= reflect.Uint && srcKind <= reflect.Uintptr {
						bits = math.Float64bits(float64(bits))
					} else {
						bits = math.Float64bits(float64(int64(bits))) //nolint:gosec
					}
				}
				// Truncate to target width for sub-word types.
				switch dstKind {
				case reflect.Int:
					mem[idx] = Value{num: bits, ref: zint}
				case reflect.Int8:
					mem[idx] = Value{num: uint64(int8(bits)), ref: zint8} //nolint:gosec
				case reflect.Int16:
					mem[idx] = Value{num: uint64(int16(bits)), ref: zint16} //nolint:gosec
				case reflect.Int32:
					mem[idx] = Value{num: uint64(int32(bits)), ref: zint32} //nolint:gosec
				case reflect.Int64:
					mem[idx] = Value{num: bits, ref: zint64}
				case reflect.Uint:
					mem[idx] = Value{num: bits, ref: zuint}
				case reflect.Uint8:
					mem[idx] = Value{num: uint64(uint8(bits)), ref: zuint8} //nolint:gosec
				case reflect.Uint16:
					mem[idx] = Value{num: uint64(uint16(bits)), ref: zuint16} //nolint:gosec
				case reflect.Uint32:
					mem[idx] = Value{num: uint64(uint32(bits)), ref: zuint32} //nolint:gosec
				case reflect.Uint64:
					mem[idx] = Value{num: bits, ref: zuint64}
				case reflect.Float32:
					mem[idx] = Value{num: math.Float64bits(float64(float32(math.Float64frombits(bits)))), ref: zfloat32}
				case reflect.Float64:
					mem[idx] = Value{num: bits, ref: zfloat64}
				}

			case isNum(srcKind) && dstKind == reflect.String:
				// int/rune -> string (e.g. string(65) -> "A").
				mem[idx] = Value{ref: reflect.ValueOf(string(rune(int64(v.num))))} //nolint:gosec

			case srcKind == reflect.String && dstKind == reflect.Slice && dstType.Elem().Kind() == reflect.Uint8:
				// string -> []byte.
				mem[idx] = Value{ref: reflect.ValueOf([]byte(v.ref.String()))}

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
					up = unsafe.Pointer(uintptr(v.num)) //nolint:gosec,govet
				}
				nv := reflect.New(dstType).Elem()
				nv.SetPointer(up)
				mem[idx] = Value{ref: nv}

			case srcKind == reflect.UnsafePointer &&
				(dstKind == reflect.Pointer || dstKind == reflect.Uintptr):
				// unsafe.Pointer -> *T or uintptr.
				up := v.ref.UnsafePointer()
				if dstKind == reflect.Uintptr {
					mem[idx] = Value{num: uint64(uintptr(up)), ref: reflect.Zero(dstType)} //nolint:gosec
				} else {
					mem[idx] = FromReflect(reflect.NewAt(dstType.Elem(), up))
				}

			default:
				// Fallback: use reflect.
				mem[idx] = FromReflect(v.Reflect().Convert(dstType))
			}

		case IfaceWrap:
			typ := m.globals[int(c.A)].ref.Interface().(*Type)
			idx := sp - int(c.B)
			mem[idx] = Value{ref: reflect.ValueOf(Iface{Typ: typ, Val: mem[idx]})}

		case IfaceCall:
			methodID := int(c.A)
			if !mem[sp].IsIface() {
				// Native interface value: use reflect to get the method.
				methodName := m.MethodNames[methodID]
				recvRV := mem[sp].Reflect()
				rv := nativeMethodLookup(recvRV, methodName)
				if !rv.IsValid() && c.B != 0 {
					// Numeric value lost its named type (e.g. time.Duration stored as int64).
					// Convert to the named type encoded in B-1 and retry the method lookup.
					namedType := m.globals[int(c.B)-1].ref.Type()
					rv = mem[sp].Reflect().Convert(namedType).MethodByName(methodName)
				}
				if rv.IsValid() && recvRV.IsValid() && hasMethodArgProxies(recvRV.Type(), methodName) {
					mem[sp] = Value{ref: reflect.ValueOf(boundProxyCall{Fn: rv, RecvType: recvRV.Type(), Method: methodName})}
					break
				}
				mem[sp] = Value{ref: rv}
				break
			}
			ifc := mem[sp].IfaceVal()
			// Fall back to reflect-based dispatch when the concrete type
			// has no compiled method entry (native type in a mvm interface).
			if methodID >= len(ifc.Typ.Methods) || !ifc.Typ.Methods[methodID].IsResolved() {
				mem[sp] = Value{ref: nativeMethodLookup(ifc.Val.Reflect(), m.MethodNames[methodID])}
				break
			}
			method := ifc.Typ.Methods[methodID]
			// The concrete type inside an embedded interface field is only known at runtime.
			nativeFallback := false
			for method.EmbedIface {
				rv := ifc.Val.Reflect()
				if rv.Kind() == reflect.Pointer {
					rv = rv.Elem()
				}
				for _, fi := range method.Path {
					rv = rv.Field(fi)
				}
				embedded := FromReflect(rv)
				if !embedded.IsIface() {
					// The embedded field holds a native interface (e.g. io.Writer
					// containing *os.File), not a mvm Iface. Fall back to
					// reflect-based dispatch.
					mem[sp] = Value{ref: nativeMethodLookup(rv, m.MethodNames[methodID])}
					nativeFallback = true
					break
				}
				ifc = embedded.IfaceVal()
				method = ifc.Typ.Methods[methodID]
			}
			if nativeFallback {
				break
			}
			codeAddr := int(m.globals[method.Index].num) //nolint:gosec
			// Build a closure with the concrete receiver as Heap[0], replacing the
			// interface value on the stack. Same result as HeapAlloc+Get+Swap+MkClosure.
			// For promoted methods, extract the embedded field as receiver.
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
				*cell = FromReflect(rv)
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
				matched := !isNil && (rv.Type().AssignableTo(dstTyp.Rtype) || dstTyp.NativeImplements(rv.Type()))
				// If the value is a bridge wrapper (e.g. *BridgeError wrapping an
				// interpreted value), try unwrapping to recover the original value.
				if !matched && !isNil {
					if orig := unbridgeValue(rv); orig.IsValid() &&
						(orig.Type().AssignableTo(dstTyp.Rtype) || dstTyp.NativeImplements(orig.Type())) {
						rv = orig
						matched = true
					}
				}
				if matched {
					mem[sp] = FromReflect(rv)
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
					ip = panicAddr
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
					ip = panicAddr
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
					if dtyp.IsInterface() {
						matched = rv.Elem().Type().Implements(dtyp.Rtype)
					} else {
						matched = rv.Elem().Type().AssignableTo(dtyp.Rtype)
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
			return err
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
			fv := forceSettable(fieldByAB(reflect.Indirect(mem[sp].ref), int(c.A), int(c.B)))
			switch {
			case isNum(fv.Kind()):
				// Preserve addressable ref for write-through on struct field mutations.
				mem[sp] = Value{num: numBits(fv), ref: fv}
			case fv.Kind() == reflect.Func && fv.CanAddr():
				// Always return addressable ref so SetS can update funcFields on reassignment.
				// Call checks funcFields for fast mvm dispatch.
				mem[sp] = Value{ref: fv}
			default:
				mem[sp] = Value{ref: fv}
			}
		case FieldSet:
			m.setFuncField(forceSettable(fieldByAB(mem[sp-1].ref, int(c.A), int(c.B))), mem[sp])
			sp--
		case FieldFset:
			m.setFuncField(forceSettable(mem[sp-2].ref.Field(int(mem[sp-1].num))), mem[sp]) //nolint:gosec
			sp -= 2
		case FieldRefSet:
			m.setFuncField(forceSettable(mem[sp-1].ref), mem[sp])
			sp -= 2
		case Jump:
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
			mem[sp] = ValueOf(mem[sp-1-int(c.A)].ref.Len())
		case Next:
			if k, ok := mem[sp-1].ref.Interface().(func() (reflect.Value, bool))(); ok {
				m.assignSlot(&m.globals[int(c.B)], FromReflect(k))
			} else {
				ip += int(c.A)
				continue
			}
		case NextLocal:
			if k, ok := mem[sp-1].ref.Interface().(func() (reflect.Value, bool))(); ok {
				m.assignSlot(&mem[fp-1+int(c.B)], FromReflect(k))
			} else {
				ip += int(c.A)
				continue
			}
		case Next0:
			if _, ok := mem[sp-1].ref.Interface().(func() (reflect.Value, bool))(); !ok {
				ip += int(c.A)
				continue
			}
		case Next2:
			if k, v, ok := mem[sp-1].ref.Interface().(func() (reflect.Value, reflect.Value, bool))(); ok {
				kAddr, vAddr := int(int16(c.B)), int(int16(c.B>>16)) //nolint:gosec
				m.assignSlot(&m.globals[kAddr], FromReflect(k))
				m.assignSlot(&m.globals[vAddr], FromReflect(v))
			} else {
				ip += int(c.A)
				continue
			}
		case Next2Local:
			if k, v, ok := mem[sp-1].ref.Interface().(func() (reflect.Value, reflect.Value, bool))(); ok {
				kAddr, vAddr := int(int16(c.B)), int(int16(c.B>>16)) //nolint:gosec
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
			mem[sp] = Value{num: uint64(int(c.A)), ref: zint} //nolint:gosec
		case Pull:
			v := mem[sp]
			if c.A != 0 {
				v = v.CopyArray()
			}
			if c.B != 0 {
				// Range-over-func: wrap a mvm Closure into a native Go func.
				funcType := m.globals[int(c.B)-1].ref.Type()
				v = Value{ref: m.wrapForFunc(v, funcType)}
			}
			next, stop := iter.Pull(v.Seq())
			if sp+2 >= len(mem) {
				mem = growStack(mem, sp, 2)
			}
			mem[sp+1] = ValueOf(next)
			mem[sp+2] = ValueOf(stop)
			sp += 2
		case Pull2:
			v := mem[sp]
			if c.A != 0 {
				v = v.CopyArray()
			}
			if c.B != 0 {
				funcType := m.globals[int(c.B)-1].ref.Type()
				v = Value{ref: m.wrapForFunc(v, funcType)}
			}
			next, stop := iter.Pull2(v.Seq2())
			if sp+2 >= len(mem) {
				mem = growStack(mem, sp, 2)
			}
			mem[sp+1] = ValueOf(next)
			mem[sp+2] = ValueOf(stop)
			sp += 2
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
			mem, sp = m.deferPush(c, mem, fp, sp)

		case GoCall:
			narg := int(c.A)
			fval := mem[sp-narg]
			args := make([]Value, narg)
			for i := range args {
				args[i] = snapshotArg(mem[sp-narg+1+i])
			}
			sp -= narg + 1
			m.mem = mem[:sp+1]
			m.newGoroutine(fval, args)
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
			m.newGoroutine(fval, args)
			mem = m.mem[:cap(m.mem)]

		case MkChan:
			elemType := m.globals[int(c.A)].ref.Type()
			chanType := reflect.ChanOf(reflect.BothDir, elemType)
			bufSize := int(c.B)
			if bufSize < 0 {
				bufSize = int(mem[sp].num) //nolint:gosec
				sp--
			}
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = Value{ref: reflect.MakeChan(chanType, bufSize)}

		case ChanSend:
			ch := mem[sp-1].ref
			ch.Send(m.reflectForSend(mem[sp], ch.Type().Elem()))
			sp -= 2

		case ChanRecv:
			ch := mem[sp]
			v, ok := ch.ref.Recv()
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
			cases := make([]reflect.SelectCase, ncase)
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
			chosen, recv, recvOK := reflect.Select(cases)
			sp = base
			ci := meta.Cases[chosen]
			if ci.Dir == reflect.SelectRecv {
				if ci.Slot >= 0 {
					v := FromReflect(recv)
					if ci.Local {
						mem[fp-1+ci.Slot] = v
					} else {
						m.assignSlot(&m.globals[ci.Slot], v)
					}
				}
				if ci.OkSlot >= 0 {
					v := boolVal(recvOK)
					if ci.Local {
						mem[fp-1+ci.OkSlot] = v
					} else {
						m.assignSlot(&m.globals[ci.OkSlot], v)
					}
				}
			}
			mem[sp] = Value{num: uint64(chosen), ref: zint} //nolint:gosec

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

		case Min:
			sp = minMax(mem, sp, int(c.A), reflect.Kind(c.B), false) //nolint:gosec

		case Max:
			sp = minMax(mem, sp, int(c.A), reflect.Kind(c.B), true) //nolint:gosec

		case WrapFunc:
			// Wrap the mvm func value on the stack in a reflect.MakeFunc for native Go callbacks.
			// The original mvm func is preserved in MvmFunc.Val for fast in-VM dispatch.
			// CallFunc is re-entrant for single-threaded synchronous callbacks; concurrent goroutine
			// calls to different wrapped functions on the same Machine are NOT safe.
			typ := m.globals[int(c.A)].ref.Interface().(*Type)
			fval := mem[sp-int(c.B)]
			mem[sp-int(c.B)] = Value{ref: reflect.ValueOf(MvmFunc{Val: fval, GF: m.wrapForFunc(fval, typ.Rtype)})}

		case Trap:
			m.trapOrig = ip + 1 // resume ip after Trap instruction
			mem = mem[:sp+1]
			m.mem, m.ip, m.fp = mem, m.trapOrig, fp
			m.enterDebug()
			mem, ip, fp = m.mem, m.ip, m.fp
			sp = len(mem) - 1
			mem = mem[:cap(mem)]
			continue

		case Panic:
			m.panicking = true
			m.panicVal = mem[sp]
			sp-- // pop the panic argument
			ip = panicAddr
			continue

		case Recover:
			if m.panicking && int(int32(mem[fp-2].num)) == deferRetAddr { //nolint:gosec
				m.panicking = false
				pv := m.panicVal
				// Wrap in Iface so type assertions on the recovered value work.
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
			} else {
				if sp+1 >= len(mem) {
					mem = growStack(mem, sp, 1)
				}
				sp++
				mem[sp] = Value{} // nil
			}

		case Return:
			// Read nret and frameBase from the packed retIP slot.
			retIPInfo := mem[fp-2].num
			nret := int((retIPInfo >> 32) & 0xFFFF)
			frameBase := int(retIPInfo >> 48)
			// If there are pending defers in this frame, dispatch the top one (LIFO).
			dh := int(mem[fp-3].num) //nolint:gosec
			if dh != 0 {
				packed := mem[dh-2].num
				narg := int(packed >> 2)       //nolint:gosec
				isX := int(packed & 3)         //nolint:gosec
				prevHead := int(mem[dh-1].num) //nolint:gosec
				funcVal := mem[dh-narg-3]
				retBase := dh - narg - 3
				if isX == 2 {
					m.execBuiltinDeferred(Op(funcVal.num), dh-narg-2, narg, mem) //nolint:gosec
					clear(mem[retBase+nret : sp+1])
					sp = retBase + nret - 1
					mem[fp-3].num = uint64(prevHead) //nolint:gosec
					continue
				}
				if isX == 1 {
					// Native function: call via reflect, discard results.
					rv := unwrapIface(funcVal.ref)
					rin := make([]reflect.Value, narg)
					for i := range rin {
						rin[i] = mem[dh-narg-2+i].Reflect()
					}
					coerceInterfaceArgs(rin, rv.Type())
					m.wrapFuncArgs(rin, mem[dh-narg-2:dh-2], rv.Type())
					rv.Call(rin)
					// Move return values (at dh+1..dh+nret) down over the defer entry.
					for i := 0; i < nret; i++ {
						mem[retBase+i] = mem[dh+1+i]
					}
					clear(mem[retBase+nret : sp+1])
					sp = retBase + nret - 1
					mem[fp-3].num = uint64(prevHead) //nolint:gosec
					continue                         // re-check for more defers
				}
				// VM function: pack ip and nret into the returnIP slot, then call.
				mem[dh].num = uint64(ip) | uint64(nret)<<32 //nolint:gosec
				prevHeap := m.heap
				nip := m.resolveIPAndHeap(funcVal)
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
				defFPVal := uint64(fp) //nolint:gosec
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
			ip = int(int32(retIPInfo)) //nolint:gosec
			ofp := fp
			fpVal := mem[fp-1].num
			if fpVal&heapSavedFlag != 0 {
				fp = int(fpVal &^ heapSavedFlag) //nolint:gosec
				top := len(m.heapFrames) - 1
				m.heap = m.heapFrames[top]
				m.heapFrames[top] = nil // clear for GC
				m.heapFrames = m.heapFrames[:top]
			} else {
				fp = int(fpVal) //nolint:gosec
				m.heap = nil
			}
			newBase := ofp - frameBase
			// Inline copy for common small nret to avoid runtime.typedslicecopy.
			switch nret {
			case 0:
				// nothing to copy
			case 1:
				mem[newBase] = mem[sp]
			default:
				copy(mem[newBase:], mem[sp-nret+1:sp+1])
			}
			sp = newBase + nret - 1
			continue
		case Slice:
			low := int(mem[sp-1].num) //nolint:gosec
			high := int(mem[sp].num)  //nolint:gosec
			mem[sp-2] = Value{ref: derefArray(mem[sp-2].ref).Slice(low, high)}
			sp -= 2
		case Slice3:
			low := int(mem[sp-2].num)  //nolint:gosec
			high := int(mem[sp-1].num) //nolint:gosec
			hi := int(mem[sp].num)     //nolint:gosec
			mem[sp-3] = Value{ref: derefArray(mem[sp-3].ref).Slice3(low, high, hi)}
			sp -= 3
		case Stop:
			mem[sp].ref.Interface().(func())()
			sp -= 3 + int(c.A)
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
			mem[sp-1].num <<= mem[sp].num
			resetNumRef(&mem[sp-1])
			sp--
		case BitShr:
			k := mem[sp-1].ref.Kind()
			if k >= reflect.Uint && k <= reflect.Uintptr {
				mem[sp-1].num >>= mem[sp].num
			} else {
				mem[sp-1].num = uint64(int64(mem[sp-1].num) >> mem[sp].num) //nolint:gosec
			}
			resetNumRef(&mem[sp-1])
			sp--
		case BitComp:
			mem[sp].num = ^mem[sp].num
			resetNumRef(&mem[sp])

		// Bit manipulation.
		case Clz32:
			mem[sp].num = uint64(bits.LeadingZeros32(uint32(mem[sp].num))) //nolint:gosec
			mem[sp].ref = zint
		case Clz64:
			mem[sp].num = uint64(bits.LeadingZeros64(mem[sp].num)) //nolint:gosec
			mem[sp].ref = zint
		case Ctz32:
			mem[sp].num = uint64(bits.TrailingZeros32(uint32(mem[sp].num))) //nolint:gosec
			mem[sp].ref = zint
		case Ctz64:
			mem[sp].num = uint64(bits.TrailingZeros64(mem[sp].num)) //nolint:gosec
			mem[sp].ref = zint
		case Popcnt32:
			mem[sp].num = uint64(bits.OnesCount32(uint32(mem[sp].num))) //nolint:gosec
			mem[sp].ref = zint
		case Popcnt64:
			mem[sp].num = uint64(bits.OnesCount64(mem[sp].num)) //nolint:gosec
			mem[sp].ref = zint
		case Rotl32:
			k := int(mem[sp].num) //nolint:gosec
			sp--
			mem[sp].num = uint64(bits.RotateLeft32(uint32(mem[sp].num), k)) //nolint:gosec
			resetNumRef(&mem[sp])
		case Rotl64:
			k := int(mem[sp].num) //nolint:gosec
			sp--
			mem[sp].num = bits.RotateLeft64(mem[sp].num, k)
			resetNumRef(&mem[sp])
		case Rotr32:
			k := int(mem[sp].num) //nolint:gosec
			sp--
			mem[sp].num = uint64(bits.RotateLeft32(uint32(mem[sp].num), -k)) //nolint:gosec
			resetNumRef(&mem[sp])
		case Rotr64:
			k := int(mem[sp].num) //nolint:gosec
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
			// Detach addressable refs to prevent aliasing: numeric values may share
			// the underlying memory of the source frame slot via their ref field.
			// Allocate a fresh reflect.Value (not reflect.Zero) so that Reflect() returns
			// the correct captured value via cell.ref.
			if isNum(cell.ref.Kind()) && cell.ref.CanAddr() {
				rv := reflect.New(cell.ref.Type()).Elem()
				setNumReflect(rv, cell.num)
				cell.ref = rv
			}
			mem[sp] = ValueOf(cell) // replace value with cell pointer
		case HeapGet:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = *m.heap[int(c.A)]
		case HeapSet:
			*m.heap[int(c.A)] = mem[sp]
			sp--
		case CellGet:
			sp++
			mem[sp] = *mem[int(c.A)+fp-1].ref.Interface().(*Value)
		case CellSet:
			*mem[int(c.A)+fp-1].ref.Interface().(*Value) = mem[sp]
			sp--
		case HeapPtr:
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = ValueOf(m.heap[int(c.A)])
		case MkClosure:
			n := int(c.A)
			codeAddr := int(mem[sp-n].num) //nolint:gosec
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
			elemType := m.globals[int(c.B)].ref.Type()
			sliceType := reflect.SliceOf(elemType)
			switch {
			case n < 0:
				// make([]T, len[, cap]): size args are on the stack.
				nSizeArgs := -n
				sLen := int(mem[sp-nSizeArgs+1].num) //nolint:gosec
				sCap := sLen
				if nSizeArgs == 2 {
					sCap = int(mem[sp].num) //nolint:gosec
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
					numSet(slice.Index(i), mem[sp-n+1+i])
				}
				mem[sp-n+1] = Value{ref: slice}
				sp -= n - 1
			}
		case MkMap:
			keyType := m.globals[int(c.A)].ref.Type()
			valType := m.globals[int(c.B)].ref.Type()
			mapType := reflect.MapOf(keyType, valType)
			if sp+1 >= len(mem) {
				mem = growStack(mem, sp, 1)
			}
			sp++
			mem[sp] = Value{ref: reflect.MakeMap(mapType)}
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
			mem[sp-1].ref.SetMapIndex(mem[sp].Reflect(), reflect.Value{})
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
			idx := int(mem[sp].num) //nolint:gosec
			ref := reflect.Indirect(mem[sp-1].ref)
			if ref.Kind() == reflect.String {
				mem[sp-1] = Value{num: uint64(ref.String()[idx]), ref: zuint8}
			} else {
				mem[sp-1] = FromReflect(ref.Index(idx))
			}
			sp--
		case IndexAddr:
			idx := int(mem[sp].num) //nolint:gosec
			ref := reflect.Indirect(mem[sp-1].ref)
			mem[sp-1] = Value{ref: ref.Index(idx).Addr()}
			sp--
		case IndexSet:
			idx := int(mem[sp-1].num) //nolint:gosec
			slot := reflect.Indirect(mem[sp-2].ref).Index(idx)
			m.setFuncField(slot, mem[sp])
			sp -= 2
		case MapIndex:
			mapVal := mem[sp-1].ref
			rv := mapVal.MapIndex(mem[sp].Reflect())
			if !rv.IsValid() {
				rv = reflect.Zero(mapVal.Type().Elem())
			}
			mem[sp-1] = FromReflect(rv)
			sp--
		case MapIndexOk:
			mapVal := mem[sp-1].ref
			rv := mapVal.MapIndex(mem[sp].Reflect())
			ok := rv.IsValid()
			if !ok {
				rv = reflect.Zero(mapVal.Type().Elem())
			}
			mem[sp-1] = FromReflect(rv)
			mem[sp] = boolVal(ok)
		case MapSet:
			mapVal := mem[sp-2].ref
			mt := mapVal.Type()
			mapVal.SetMapIndex(numReflect(mt.Key(), mem[sp-1]), m.wrapForFunc(mem[sp], mt.Elem()))
			sp -= 2
		case SetS:
			n := int(c.A)
			for i := 0; i < n; i++ {
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
			mem[sp-1] = boolVal(int64(mem[sp-1].num) > int64(mem[sp].num)) //nolint:gosec
			sp--
		case GreaterUint, GreaterUint8, GreaterUint16, GreaterUint32, GreaterUint64:
			mem[sp-1] = boolVal(mem[sp-1].num > mem[sp].num)
			sp--
		case GreaterFloat32, GreaterFloat64:
			mem[sp-1] = boolVal(math.Float64frombits(mem[sp-1].num) > math.Float64frombits(mem[sp].num))
			sp--

		// Per-type Lower.
		case LowerInt, LowerInt8, LowerInt16, LowerInt32, LowerInt64:
			mem[sp-1] = boolVal(int64(mem[sp-1].num) < int64(mem[sp].num)) //nolint:gosec
			sp--
		case LowerUint, LowerUint8, LowerUint16, LowerUint32, LowerUint64:
			mem[sp-1] = boolVal(mem[sp-1].num < mem[sp].num)
			sp--
		case LowerFloat32, LowerFloat64:
			mem[sp-1] = boolVal(math.Float64frombits(mem[sp-1].num) < math.Float64frombits(mem[sp].num))
			sp--

		// Immediate operand ops: right-hand constant is in Arg[0].
		case AddIntImm:
			mem[sp].num = uint64(int(mem[sp].num) + int(c.A)) //nolint:gosec
			mem[sp].ref = zint
		case SubIntImm:
			mem[sp].num = uint64(int(mem[sp].num) - int(c.A)) //nolint:gosec
			mem[sp].ref = zint
		case MulIntImm:
			mem[sp].num = uint64(int(mem[sp].num) * int(c.A)) //nolint:gosec
			mem[sp].ref = zint
		case GreaterIntImm:
			mem[sp] = boolVal(int(mem[sp].num) > int(c.A)) //nolint:gosec
		case GreaterUintImm:
			mem[sp] = boolVal(uint(mem[sp].num) > uint(int(c.A))) //nolint:gosec
		case LowerIntImm:
			mem[sp] = boolVal(int(mem[sp].num) < int(c.A)) //nolint:gosec
		case LowerUintImm:
			mem[sp] = boolVal(uint(mem[sp].num) < uint(int(c.A))) //nolint:gosec

		case DeferRet:
			mem, sp, ip = m.deferRet(mem, fp, sp)
			continue

		case PanicUnwind:
			if done, err := m.panicUnwind(&mem, &fp, &sp, &ip, panicAddr); done {
				return err
			}
			continue
		}
		ip++
	}
}

func (m *Machine) restoreFP(fpVal uint64) int {
	if fpVal&heapSavedFlag != 0 {
		fp := int(fpVal &^ heapSavedFlag) //nolint:gosec
		top := len(m.heapFrames) - 1
		m.heap = m.heapFrames[top]
		m.heapFrames[top] = nil // clear for GC
		m.heapFrames = m.heapFrames[:top]
		return fp
	}
	m.heap = nil
	return int(fpVal) //nolint:gosec
}

// unwrapIface returns the element of an interface reflect.Value, or rv
// unchanged if it isn't an interface or is nil.
func unwrapIface(rv reflect.Value) reflect.Value {
	if rv.Kind() == reflect.Interface && !rv.IsNil() {
		return rv.Elem()
	}
	return rv
}

// isNativeFunc reports whether rv holds a native Go function, possibly
// wrapped in an interface.
func isNativeFunc(rv reflect.Value) bool {
	return unwrapIface(rv).Kind() == reflect.Func
}

func (m *Machine) resolveIPAndHeap(funcVal Value) int {
	if isNum(funcVal.ref.Kind()) {
		m.heap = nil
		return int(funcVal.num) //nolint:gosec
	}
	if clo, ok := funcVal.ref.Interface().(Closure); ok {
		m.heap = clo.Heap
		return clo.Code
	}
	m.heap = nil
	if iv, ok := funcVal.ref.Interface().(int); ok {
		return iv
	}
	return int(funcVal.num) //nolint:gosec
}

func (m *Machine) deferPush(c Instruction, mem []Value, fp, sp int) ([]Value, int) {
	narg := int(c.A)
	isX := int(c.B)
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
	for i := sp - narg + 1; i <= sp; i++ {
		mem[i] = snapshotArg(mem[i])
	}
	// Push 3-slot header: packed(narg/isX), prevHead link, returnIP placeholder.
	// isX uses 2 bits: 0=VM func, 1=native reflect func, 2=builtin opcode.
	prevHead := int(mem[fp-3].num) //nolint:gosec
	if sp+3 >= len(mem) {
		mem = growStack(mem, sp, 3)
	}
	mem[sp+1] = Value{num: uint64(narg<<2 | isX)} //nolint:gosec
	mem[sp+2] = Value{num: uint64(prevHead)}      //nolint:gosec
	mem[sp+3] = Value{}                           // returnIP placeholder, filled by Return
	sp += 3
	mem[fp-3].num = uint64(sp) //nolint:gosec // dh = index of returnIP slot
	return mem, sp
}

func (m *Machine) deferRet(mem []Value, fp, sp int) ([]Value, int, int) {
	mem = mem[:sp+1]
	dh := int(mem[fp-3].num)        //nolint:gosec
	narg := int(mem[dh-2].num >> 2) //nolint:gosec
	val := mem[dh].num
	returnIP := int(int32(val & 0xFFFFFFFF)) //nolint:gosec
	nret := int(val >> 32)                   //nolint:gosec
	prevHead := int(mem[dh-1].num)           //nolint:gosec
	retBase := dh - narg - 3
	copy(mem[retBase:], mem[dh+1:dh+1+nret]) // move return values down
	clear(mem[retBase+nret:])                // clear stale slots
	mem = mem[:retBase+nret]
	mem[fp-3].num = uint64(prevHead) //nolint:gosec
	sp = len(mem) - 1
	mem = mem[:cap(mem)]
	return mem, sp, returnIP
}

func (m *Machine) panicUnwind(mem *[]Value, fp, sp, ip *int, panicAddr int) (bool, error) {
	deferRetBits := uint64(panicAddr - 1) //nolint:gosec
	*mem = (*mem)[:*sp+1]
	if *fp == 0 {
		// Top-level panic: no call frame to unwind.
		m.mem, m.ip, m.fp = *mem, 0, 0
		return true, fmt.Errorf("panic: %v", m.panicVal.Interface())
	}
	dh := int((*mem)[*fp-3].num) //nolint:gosec
	if dh != 0 {
		packed := (*mem)[dh-2].num
		narg := int(packed >> 2)          //nolint:gosec
		isX := int(packed & 3)            //nolint:gosec
		prevHead := int((*mem)[dh-1].num) //nolint:gosec
		funcVal := (*mem)[dh-narg-3]
		retBase := dh - narg - 3
		popDefer := func() (bool, error) {
			clear((*mem)[retBase:])
			*mem = (*mem)[:retBase]
			(*mem)[*fp-3].num = uint64(prevHead) //nolint:gosec
			*sp = len(*mem) - 1
			*mem = (*mem)[:cap(*mem)]
			return false, nil
		}
		if isX == 2 {
			m.execBuiltinDeferred(Op(funcVal.num), dh-narg-2, narg, *mem) //nolint:gosec
			return popDefer()
		}
		if isX == 1 {
			// Native defer: call via reflect, discard results.
			rv := unwrapIface(funcVal.ref)
			rin := make([]reflect.Value, narg)
			for i := range rin {
				rin[i] = (*mem)[dh-narg-2+i].Reflect()
			}
			coerceInterfaceArgs(rin, rv.Type())
			m.wrapFuncArgs(rin, (*mem)[dh-narg-2:dh-2], rv.Type())
			rv.Call(rin)
			return popDefer()
		}
		// VM defer: store panicAddr as return address, push frame.
		retIPInfo := (*mem)[*fp-2].num
		nret := int((retIPInfo >> 32) & 0xFFFF)
		(*mem)[dh].num = uint64(uint32(panicAddr)) | uint64(nret)<<32 //nolint:gosec
		prevHeap := m.heap
		nip := m.resolveIPAndHeap(funcVal)
		base := len(*mem)
		*mem = append(*mem, funcVal)
		*mem = append(*mem, (*mem)[dh-narg-2:dh-2]...)
		defFPVal := uint64(*fp) //nolint:gosec
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
		// Recovered: tear down frame, return zero values to caller.
		retIPInfo := (*mem)[*fp-2].num
		nret := int((retIPInfo >> 32) & 0xFFFF)
		frameBase := int(retIPInfo >> 48)
		*ip = int(int32(retIPInfo)) //nolint:gosec
		ofp := *fp
		*fp = m.restoreFP((*mem)[*fp-1].num)
		newBase := ofp - frameBase
		newSP := newBase + nret
		clear((*mem)[newBase:newSP]) // clear return slots
		clear((*mem)[newSP:])        // clear stale slots
		*mem = (*mem)[:newSP]
		*sp = len(*mem) - 1
		*mem = (*mem)[:cap(*mem)]
		return false, nil
	}
	// Still panicking: tear down frame, continue unwinding parent.
	frameBase := int((*mem)[*fp-2].num >> 48)
	ofp := *fp
	*fp = m.restoreFP((*mem)[*fp-1].num)
	if *fp == 0 {
		// Top of stack: return panic as error.
		m.mem, m.ip, m.fp = *mem, 0, 0
		return true, fmt.Errorf("panic: %v", m.panicVal.Interface())
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

// nativeMethodLookup resolves a method by name, unwrapping interface/pointer indirection.
func nativeMethodLookup(rv reflect.Value, name string) reflect.Value {
	if rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	if mv := rv.MethodByName(name); mv.IsValid() {
		return mv
	}
	return reflect.Indirect(rv).MethodByName(name)
}

// PushCode adds instructions to the machine code (with zero source positions).
func (m *Machine) PushCode(code ...Instruction) (p int) {
	p = len(m.code)
	m.code = append(m.code, code...)
	return p
}

// SetIP sets the value of machine instruction pointer to given index.
func (m *Machine) SetIP(ip int) { m.ip = ip }

// Push pushes data values into the machine's global storage.
// Globals are always loaded via Push before Run is called.
func (m *Machine) Push(v ...Value) (l int) {
	l = len(m.globals)
	m.globals = append(m.globals, v...)
	return l
}

// minMax computes the min (or max if isMax) of n values on the stack.
// It returns the updated stack pointer.
func minMax(mem []Value, sp, n int, kind reflect.Kind, isMax bool) int {
	best := sp - n + 1
	switch {
	case kind >= reflect.Int && kind <= reflect.Int64:
		for i := best + 1; i <= sp; i++ {
			if isMax {
				if int64(mem[i].num) > int64(mem[best].num) { //nolint:gosec
					best = i
				}
			} else {
				if int64(mem[i].num) < int64(mem[best].num) { //nolint:gosec
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
	if elemType.Kind() == reflect.Func {
		return m.wrapForFunc(val, elemType)
	}
	// Bridge-wrap so the value satisfies the interface.
	if elemType.Kind() == reflect.Interface && val.IsIface() {
		return m.bridgeIface(val.IfaceVal(), elemType)
	}
	rv := val.Reflect()
	if rv.Type() != elemType {
		rv = rv.Convert(elemType)
	}
	return rv
}

// bridgeIface wraps an Iface value for a target interface type, trying
// InterfaceBridges, then single-method Bridges, then concrete unwrap.
func (m *Machine) bridgeIface(ifc Iface, targetType reflect.Type) reflect.Value {
	if len(m.MethodNames) > 0 {
		if bridgePtrType, ok := InterfaceBridges[targetType]; ok {
			if w := m.wrapIfaceMulti(ifc, bridgePtrType); w.IsValid() {
				return w
			}
		}
		if w := m.wrapIface(ifc, targetType); w.IsValid() {
			return w
		}
	}
	val := ifc.Val.Reflect()
	if ifc.Typ != nil && (!val.IsValid() || (val.Kind() == reflect.Interface && val.IsNil())) {
		return reflect.Zero(ifc.Typ.Rtype)
	}
	if ifc.Typ != nil && ifc.Typ.Rtype.Kind() == reflect.Func {
		if gf := m.wrapForFunc(ifc.Val, ifc.Typ.Rtype); gf.IsValid() {
			return gf
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
		return numReflect(funcType, val)
	}
	rv := val.Reflect()
	if !rv.IsValid() {
		return reflect.Zero(funcType)
	}
	if rv.Kind() == reflect.Func {
		if pf, ok := rv.Interface().(MvmFunc); ok {
			return pf.GF
		}
		return rv // already a proper Go func
	}
	// Already wrapped by WrapFunc - extract the Go func wrapper.
	if pf, ok := val.ref.Interface().(MvmFunc); ok {
		return pf.GF
	}
	return m.makeCallFunc(val, funcType)
}

// runnerState captures the Machine fields needed to create lightweight runner
// Machines for re-entrant execution (bridge callbacks, MakeFunc adapters).
// Snapshot once, reuse across closures to avoid drift between call sites.
type runnerState struct {
	globals     []Value
	code        []Instruction
	baseCodeLen int
	out, err    io.Writer
	methodNames []string
}

func (m *Machine) captureRunnerState() runnerState {
	return runnerState{
		globals:     m.globals,
		code:        m.code[:m.baseCodeLen:m.baseCodeLen],
		baseCodeLen: m.baseCodeLen,
		out:         m.out,
		err:         m.err,
		methodNames: m.MethodNames,
	}
}

func (rs *runnerState) newRunner() *Machine {
	return &Machine{
		globals:     rs.globals,
		code:        rs.code,
		baseCodeLen: rs.baseCodeLen,
		out:         rs.out,
		err:         rs.err,
		MethodNames: rs.methodNames,
	}
}

// makeCallFunc wraps a mvm function value in a reflect.MakeFunc adapter
// that creates a fresh Machine and calls CallFunc for re-entrant execution.
// Captures VM state rather than m to avoid data races with goroutines.
func (m *Machine) makeCallFunc(fval Value, fnType reflect.Type) reflect.Value {
	rs := m.captureRunnerState()
	return reflect.MakeFunc(fnType, func(args []reflect.Value) []reflect.Value {
		out, err := rs.newRunner().CallFunc(fval, fnType, args)
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
// It saves and restores all execution state so it can be called from native Go callbacks
// (reflect.MakeFunc wrappers) even while Run is in progress (single-threaded re-entrancy).
func (m *Machine) CallFunc(fval Value, funcType reflect.Type, args []reflect.Value) ([]reflect.Value, error) {
	// Save all volatile execution state.
	savedGlobals := m.globals
	savedMem := m.mem
	savedIP := m.ip
	savedFP := m.fp
	savedHeap := m.heap
	savedFrames := m.heapFrames
	savedPanicking := m.panicking
	savedPanicVal := m.panicVal
	savedCodeLen := len(m.code)

	defer func() {
		m.globals = savedGlobals
		m.mem = savedMem
		m.ip = savedIP
		m.fp = savedFP
		m.heap = savedHeap
		m.heapFrames = savedFrames
		m.panicking = savedPanicking
		m.panicVal = savedPanicVal
		m.code = m.code[:savedCodeLen]
	}()

	// Reset per-call state.
	m.heap = nil
	m.heapFrames = nil
	m.panicking = false
	m.panicVal = Value{}

	// Copy globals to a new backing array so the callback's global writes
	// don't affect the outer Run's globals.
	m.globals = append([]Value(nil), m.globals...)

	// Fresh stack with func value and args.
	m.mem = nil
	m.mem = append(m.mem, fval)
	for _, a := range args {
		m.mem = append(m.mem, FromReflect(a))
	}

	// Temporarily append Call + Exit to drive the function to completion.
	narg := funcType.NumIn()
	nret := funcType.NumOut()
	callIP := len(m.code)
	m.code = append(m.code, Instruction{Op: Call, A: int32(narg), B: int32(nret)}) //nolint:gosec
	m.code = append(m.code, Instruction{Op: Exit})
	m.ip = callIP
	m.fp = 0

	if err := m.Run(); err != nil {
		return nil, err
	}

	// Return values land at m.mem[0:nret] after the call frame tears down.
	out := make([]reflect.Value, nret)
	for i := range out {
		rv := m.mem[i].Reflect()
		if !rv.IsValid() {
			// A nil/zero value (e.g. nil error) must be typed for MakeFunc callers.
			rv = reflect.Zero(funcType.Out(i))
		} else if rv.Type() == ifaceRtype {
			// Unwrap Iface return values so MakeFunc callers see the concrete value.
			ifc := rv.Interface().(Iface)
			rv = m.bridgeIface(ifc, funcType.Out(i))
		} else if outType := funcType.Out(i); rv.Type() != outType && outType.Kind() == reflect.Interface {
			// Interface locals use interface{} internally; convert to the expected
			// interface type (e.g. interface{} -> error) for MakeFunc callers.
			if rv.Kind() == reflect.Interface && rv.IsNil() {
				rv = reflect.Zero(outType)
			} else {
				rv = rv.Elem().Convert(outType)
			}
		}
		out[i] = rv
	}
	return out, nil
}

func (m *Machine) newGoroutine(fval Value, args []Value) {
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
		in := make([]reflect.Value, len(args))
		for i, a := range args {
			in[i] = a.Reflect()
		}
		coerceInterfaceArgs(in, rv.Type())
		m.wrapFuncArgs(in, args, rv.Type())
		go func() { rv.Call(in) }()
		return
	}

	// Resolve VM function address and closure heap.
	var nip int
	var heap []*Value
	if isNum(fval.ref.Kind()) {
		nip = int(fval.num) //nolint:gosec
	} else if clo, ok := fval.ref.Interface().(Closure); ok {
		nip, heap = clo.Code, clo.Heap
	} else if iv, ok := fval.ref.Interface().(int); ok {
		nip = iv
	} else {
		nip = int(fval.num) //nolint:gosec
	}

	// Pre-build the call frame: [fval, args..., deferHead, retIP+info, prevFP].
	// The return address targets the Exit sentinel appended by Run() at baseCodeLen+2.
	narg := len(args)
	frameBase := narg + 4
	exitAddr := m.baseCodeLen + 2
	mem := make([]Value, frameBase, frameBase+16)
	mem[0] = fval
	copy(mem[1:], args)
	// mem[narg+1] is zero (deferHead)
	mem[narg+2] = Value{num: packRetIP(exitAddr, 0, frameBase)}
	mem[narg+3] = Value{num: 0} // prevFP = 0

	child := &Machine{
		globals:     m.globals,
		code:        m.code[:m.baseCodeLen:m.baseCodeLen],
		baseCodeLen: m.baseCodeLen,
		heap:        heap,
		ip:          nip,
		fp:          frameBase,
		mem:         mem,
		in:          m.in,
		out:         m.out,
		err:         m.err,
		debugIn:     m.debugIn,
		debugOut:    m.debugOut,
		MethodNames: m.MethodNames,
	}
	go func() { _ = child.Run() }()
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
		mem[base].ref.SetMapIndex(mem[base+1].Reflect(), reflect.Value{})
	case CopySlice:
		reflect.Copy(mem[base].ref, mem[base+1].ref)
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

// Top returns (but not remove)  the value on the top of machine stack.
func (m *Machine) Top() (v Value) {
	if l := len(m.mem); l > 0 {
		v = m.mem[l-1]
	} else if l := len(m.globals); l > 0 {
		// When the stack is empty (e.g. after a pure global assignment), return
		// the last global. In the pre-split layout globals were in m.mem and
		// Top() naturally returned the last one; preserve that behaviour.
		v = m.globals[l-1]
	}
	return v
}

// PopExit removes the last machine code instruction if is Exit.
func (m *Machine) PopExit() {
	if l := len(m.code); l > 0 && m.code[l-1].Op == Exit {
		m.code = m.code[:l-1]
	}
}

// Vstring returns the string representation of a list of values.
func Vstring(lv []Value) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, v := range lv {
		if i > 0 {
			sb.WriteByte(' ')
		}
		if !v.ref.IsValid() {
			fmt.Fprintf(&sb, "<%d>", v.num)
		} else {
			fmt.Fprintf(&sb, "%v", v.Interface())
		}
	}
	sb.WriteByte(']')
	return sb.String()
}

func funcValuePtr(fv reflect.Value) uintptr {
	return *(*uintptr)(fv.Addr().UnsafePointer()) //nolint:gosec
}

func forceSettable(fv reflect.Value) reflect.Value {
	if !fv.CanSet() && fv.CanAddr() {
		fv = reflect.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem()
	}
	return fv
}

func (m *Machine) resolveFuncField(v Value) Value {
	if v.ref.Kind() == reflect.Func && v.ref.CanAddr() {
		if pf, ok := m.funcFields[v.ref.UnsafeAddr()]; ok {
			return pf
		}
		if !v.ref.IsNil() && m.funcFieldsByFuncPtr != nil {
			if pf, ok := m.funcFieldsByFuncPtr[funcValuePtr(v.ref)]; ok {
				return pf
			}
		}
	}
	return v
}

func (m *Machine) setGoFuncField(fv, gf reflect.Value, val Value) {
	fv.Set(gf)
	if ptr := funcValuePtr(fv); ptr != 0 {
		if m.funcFieldsByFuncPtr == nil {
			m.funcFieldsByFuncPtr = make(map[uintptr]Value)
		}
		m.funcFieldsByFuncPtr[ptr] = val
	}
}

func (m *Machine) setFuncField(fv reflect.Value, val Value) {
	if !val.ref.IsValid() {
		fv.Set(reflect.Zero(fv.Type()))
		return
	}
	if pf, ok := val.ref.Interface().(MvmFunc); ok && fv.CanAddr() {
		if m.funcFields == nil {
			m.funcFields = make(map[uintptr]Value)
		}
		m.funcFields[fv.UnsafeAddr()] = pf.Val
		m.setGoFuncField(fv, pf.GF, pf.Val)
		return
	}
	if fv.Kind() == reflect.Func && fv.CanAddr() {
		if m.funcFields == nil {
			m.funcFields = make(map[uintptr]Value)
		}
		m.funcFields[fv.UnsafeAddr()] = val
		if gf := m.wrapForFunc(val, fv.Type()); gf.IsValid() {
			m.setGoFuncField(fv, gf, val)
		}
		return
	}
	if isNum(fv.Kind()) && isNum(val.ref.Kind()) {
		// Avoid reflect.Set type-mismatch when field and value are different numeric kinds
		// (e.g. uint field, int value from untyped const).
		setNumReflect(fv, val.num)
		return
	}
	if fv.Kind() == reflect.Interface && val.IsIface() {
		iv := val.IfaceVal()
		// Unwrap Iface for native types so reflect-based code (e.g. fmt.Println)
		// sees raw Go values. Keep Iface for interpreted types that need it for
		// method dispatch.
		if len(iv.Typ.Methods) == 0 {
			if iv.Typ.Rtype.Kind() == reflect.Func {
				// Wrap interpreted func so native method lookup works.
				fv.Set(m.wrapForFunc(iv.Val, iv.Typ.Rtype))
				return
			}
			fv.Set(numReflect(iv.Typ.Rtype, iv.Val))
			return
		}
	}
	fv.Set(val.Reflect())
}

func (m *Machine) assignSlot(dst *Value, src Value) {
	if pf := m.resolveFuncField(src); pf != src {
		*dst = pf
		return
	}
	// Struct func fields can't hold mvm func values (int code addresses or Closures)
	// via reflect.Set. Store them in a side table keyed by the field's memory address,
	// and also set the field to a non-nil wrapper so nil-checks work correctly.
	if dst.ref.Kind() == reflect.Func && dst.ref.CanAddr() {
		if m.funcFields == nil {
			m.funcFields = make(map[uintptr]Value)
		}
		m.funcFields[dst.ref.UnsafeAddr()] = src
		dst.num = src.num
		if gf := m.wrapForFunc(src, dst.ref.Type()); gf.IsValid() {
			m.setGoFuncField(dst.ref, gf, src)
		}
		return
	}
	if isNum(src.ref.Kind()) {
		dst.num = src.num
		if dst.ref.CanSet() {
			if isNum(dst.ref.Kind()) {
				setNumReflect(dst.ref, src.num)
			} else {
				dst.ref.Set(src.Reflect())
			}
		} else {
			dst.ref = src.ref
		}
	} else {
		if dst.ref.CanSet() {
			s := src.ref
			if !s.IsValid() {
				s = reflect.Zero(dst.ref.Type())
			} else if dst.ref.Kind() == reflect.Interface && isNilable(s) && s.IsNil() {
				// Avoid creating a typed nil inside an interface{} slot.
				// A typed nil (e.g. (func())(nil)) is not equal to untyped nil,
				// which would break `f == nil` checks for func variables stored in interface{} slots.
				s = reflect.Zero(dst.ref.Type())
			}
			dst.ref.Set(s)
		} else {
			dst.ref = src.ref
		}
	}
}

func setNumReflect(rv reflect.Value, num uint64) {
	switch rv.Kind() {
	case reflect.Bool:
		rv.SetBool(num != 0)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		rv.SetInt(int64(num)) //nolint:gosec
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		rv.SetUint(num)
	case reflect.Float32, reflect.Float64:
		rv.SetFloat(math.Float64frombits(num))
	}
}

func numSet(dst reflect.Value, src Value) {
	if isNum(dst.Kind()) && isNum(src.ref.Kind()) {
		setNumReflect(dst, src.num)
	} else {
		dst.Set(src.Reflect())
	}
}

// derefArray dereferences a pointer-to-array so it can be sliced.
func derefArray(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Ptr && v.Elem().Kind() == reflect.Array {
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

// bridgeArgs scans native-call arguments for Iface values and replaces them
// with wrapper instances that implement Go interfaces via registered bridges.
// Non-bridged Iface values are unwrapped to their concrete value.
//
// fnPtr (non-zero) is the code pointer of the native function, used to
// look up per-argument proxies registered via RegisterArgProxy. recvType
// and methodName (non-nil and non-empty) identify a method call and are
// used to look up proxies registered via RegisterArgProxyMethod. Proxies
// take precedence over the standard bridgeIface dispatch when matched.
func (m *Machine) bridgeArgs(in []reflect.Value, funcType reflect.Type, fnPtr uintptr, recvType reflect.Type, methodName string) {
	for i, rv := range in {
		if !rv.IsValid() || rv.Type() != ifaceRtype {
			// Also check inside interface{} wrapping.
			if rv.IsValid() && rv.Kind() == reflect.Interface && !rv.IsNil() &&
				rv.Elem().Type() == ifaceRtype {
				rv = rv.Elem()
			} else {
				continue
			}
		}
		ifc := rv.Interface().(Iface)
		var factory ProxyFactory
		if fnPtr != 0 {
			factory = lookupFuncArgProxy(fnPtr, i)
		}
		if factory == nil && recvType != nil && methodName != "" {
			factory = lookupMethodArgProxy(recvType, methodName, i)
		}
		if factory != nil {
			in[i] = factory(m, ifc)
			continue
		}
		targetType := paramTypeFor(funcType, i)
		if targetType == nil {
			targetType = AnyRtype
		}
		in[i] = m.bridgeIface(ifc, targetType)
	}
}

// paramTypeFor returns the expected parameter type for argument i of funcType.
// For variadic functions past the last fixed param, it returns the slice element type.
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

// coerceInterfaceArgs unwraps interface-typed arguments whose type does not match
// the function's expected parameter type. This handles native interface values
// (e.g. context.Context) stored in generic interface{} variable slots.
func coerceInterfaceArgs(in []reflect.Value, funcType reflect.Type) {
	for i, rv := range in {
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
		if rv.Kind() == reflect.Interface && !rv.IsNil() {
			in[i] = rv.Elem()
		} else if rv.Kind() == paramType.Kind() || (isNum(rv.Kind()) && isNum(paramType.Kind())) {
			// Convert named types or across numeric kinds (e.g. int to time.Duration).
			in[i] = rv.Convert(paramType)
		}
	}
}

// wrapFuncArgs wraps mvm function values (Closures or code addresses) into
// native Go functions when the expected parameter type is func.
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

// ifaceMethodTypes returns the types to scan for methods: the type itself
// and, for pointer types, the element type. Methods are registered on the
// base type T, not *T, so both must be checked.
func ifaceMethodTypes(typ *Type) (types [2]*Type, n int) {
	types[0] = typ
	n = 1
	if typ.Rtype.Kind() == reflect.Pointer && typ.ElemType != nil {
		types[1] = typ.ElemType
		n = 2
	}
	return
}

// wrapIface creates a bridge value that implements a Go interface.
// It first tries composite bridges (e.g. Reader+WriterTo) to preserve
// additional interface capabilities beyond the target, then falls back
// to a single-method bridge. When targetType is interface{}/any, only
// DisplayBridges are used. Methods are checked on both the type and its
// element type (methods are registered on base type T, not *T).
func (m *Machine) wrapIface(ifc Iface, targetType reflect.Type) reflect.Value {
	if ifc.Typ == nil {
		return reflect.Value{}
	}

	// For non-empty interfaces, build a set of required method names.
	// For interface{}/any, use DisplayBridges as the filter.
	nonEmpty := targetType.Kind() == reflect.Interface && targetType.NumMethod() > 0
	var required map[string]bool
	if nonEmpty {
		required = make(map[string]bool, targetType.NumMethod())
		for i := range targetType.NumMethod() {
			required[targetType.Method(i).Name] = true
		}
	} else {
		required = DisplayBridges
	}

	// Single pass: collect all methods that have registered bridges.
	type bridgedMethod struct {
		name   string
		method Method
	}
	var bridged [8]bridgedMethod
	count := 0

	methodTypes, n := ifaceMethodTypes(ifc.Typ)
	for _, mt := range methodTypes[:n] {
		for id, method := range mt.Methods {
			if method.Index < 0 || id >= len(m.MethodNames) {
				continue
			}
			name := m.MethodNames[id]
			if _, ok := Bridges[name]; !ok {
				continue
			}
			if count < len(bridged) {
				bridged[count] = bridgedMethod{name, method}
				count++
			}
		}
	}

	// Try composite bridge if 2+ bridgeable methods and target is a non-empty interface.
	if count >= 2 && nonEmpty && len(CompositeBridges) > 0 {
		for i := 0; i < count; i++ {
			for j := i + 1; j < count; j++ {
				key := [2]string{bridged[i].name, bridged[j].name}
				if key[0] > key[1] {
					key[0], key[1] = key[1], key[0]
				}
				compType, ok := CompositeBridges[key]
				if !ok {
					continue
				}
				if !compType.Implements(targetType) {
					continue
				}
				if w := m.wrapIfaceMulti(ifc, compType); w.IsValid() {
					return w
				}
			}
		}
	}

	// Fall back to single-method bridge.
	for _, bm := range bridged[:count] {
		if !required[bm.name] {
			continue
		}
		bridgePtrType := Bridges[bm.name]
		bridge := reflect.New(bridgePtrType.Elem())
		// Skip single-method bridges that don't satisfy the target interface.
		if nonEmpty && !bridge.Type().Implements(targetType) {
			continue
		}
		fnField := bridge.Elem().FieldByName("Fn")
		fnField.Set(m.makeBridgeClosure(ifc, bm.method, fnField.Type()))
		if valField := bridge.Elem().FieldByName("Val"); valField.IsValid() {
			if rv := ifc.Val.Reflect(); rv.IsValid() {
				valField.Set(reflect.ValueOf(rv.Interface()))
			}
		}
		return bridge
	}

	return reflect.Value{}
}

// wrapIfaceMulti creates a bridge that implements a multi-method interface
// (e.g. heap.Interface). The bridge struct has fields named Fn<MethodName>
// for each method. All matching methods on the interpreted type are wired up.
func (m *Machine) wrapIfaceMulti(ifc Iface, bridgePtrType reflect.Type) reflect.Value {
	if ifc.Typ == nil {
		return reflect.Value{}
	}

	bridge := reflect.New(bridgePtrType.Elem())
	elem := bridge.Elem()
	matched := false

	methodTypes, n := ifaceMethodTypes(ifc.Typ)
	isPtr := ifc.Typ.Rtype.Kind() == reflect.Pointer
	for _, mt := range methodTypes[:n] {
		for id, method := range mt.Methods {
			if method.Index < 0 || id >= len(m.MethodNames) {
				continue
			}
			fnField := elem.FieldByName("Fn" + m.MethodNames[id])
			if !fnField.IsValid() {
				continue
			}
			// Value-receiver methods called on a pointer need the pointer
			// dereferenced at each call time so mutations are visible.
			deref := isPtr && !method.PtrRecv
			fnField.Set(m.makeBridgeClosureImpl(ifc, method, fnField.Type(), deref))
			matched = true
		}
	}

	if !matched {
		return reflect.Value{}
	}
	return bridge
}

// makeBridgeClosure returns a reflect.Value of a function that, when called,
// invokes the interpreted method on the given Iface receiver.
func (m *Machine) makeBridgeClosure(ifc Iface, method Method, fnType reflect.Type) reflect.Value {
	return m.makeBridgeClosureImpl(ifc, method, fnType, false)
}

// makeBridgeClosureImpl builds the bridge closure. When deref is true, the
// receiver pointer is dereferenced at each call, which is required for
// value-receiver methods invoked on a pointer (e.g. (IntHeap).Len called
// on *IntHeap). The dereference must happen at call time so mutations from
// pointer-receiver methods (Push/Pop) are visible.
func (m *Machine) makeBridgeClosureImpl(ifc Iface, method Method, fnType reflect.Type, deref bool) reflect.Value {
	cell, fval := m.makeMethodCell(ifc, method)
	if !deref {
		return m.makeCallFunc(fval, fnType)
	}
	// For value-receiver methods: dereference the pointer at each call.
	ptrVal := ifc.Val
	rs := m.captureRunnerState()
	return reflect.MakeFunc(fnType, func(args []reflect.Value) []reflect.Value {
		*cell = FromReflect(reflect.Indirect(ptrVal.Reflect()))
		out, err := rs.newRunner().CallFunc(fval, fnType, args)
		if err != nil {
			panic(err)
		}
		return out
	})
}

// makeMethodCell builds the receiver cell and mvm func Value for a
// method invocation on ifc. Reused by makeBridgeClosureImpl and by the
// public MakeMethodCallable entry point used by mvm-native stdlib
// replacements (e.g. stdlib/jsonx).
func (m *Machine) makeMethodCell(ifc Iface, method Method) (*Value, Value) {
	codeAddr := int(m.globals[method.Index].num) //nolint:gosec
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
		*cell = FromReflect(rv)
	}
	return cell, Value{ref: reflect.ValueOf(Closure{Code: codeAddr, Heap: []*Value{cell}})}
}

// MakeMethodCallable returns a mvm func Value suitable for
// Machine.CallFunc. The receiver cell is constructed with method.Path
// applied.
func (m *Machine) MakeMethodCallable(ifc Iface, method Method) Value {
	_, fval := m.makeMethodCell(ifc, method)
	return fval
}

// MethodByName returns the first resolved method named `name` reachable
// from t. For pointer types, methods declared on the element type are
// also searched (mvm registers methods on the base type T, not *T).
// Struct-field *Type shallow copies preserve a Base back-pointer to the
// source type so methods registered after the copy was taken (typical
// when a struct is defined before its methods) remain reachable.
// Returns (Method, true) on hit.
func (m *Machine) MethodByName(t *Type, name string) (Method, bool) {
	for cur := t; cur != nil; cur = cur.Base {
		types, n := ifaceMethodTypes(cur)
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
		if cur.Base == nil {
			break
		}
	}
	return Method{}, false
}
