package vm

import (
	"fmt"
	"log"
	"testing"
)

func init() {
	log.SetFlags(log.Lshortfile)
}

func TestVM(t *testing.T) {
	for _, test := range tests {
		test := test
		t.Run("", func(t *testing.T) {
			m := &Machine{}
			for _, v := range test.sym {
				m.Push(v)
			}
			m.PushCode(test.code...)
			if err := m.Run(); err != nil {
				t.Errorf("run error: %v", err)
			}
			t.Log(Vstring(m.mem))
			if r := Vstring(m.mem[test.start:test.end]); r != test.mem {
				t.Errorf("got %v, want %v", r, test.mem)
			}
		})
	}
}

func BenchmarkVM(b *testing.B) {
	for _, test := range tests {
		test := test
		b.Run("", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				m := &Machine{}
				m.PushCode(test.code...)
				b.StartTimer()

				if err := m.Run(); err != nil {
					b.Errorf("run error: %v", err)
				}
			}
		})
	}
}

// fibTypedCode is fib(20) hand-written with per-type opcodes (no Imm).
// fib function at addr 1, call site at addr 19.
var fibTypedCode = []Instruction{
	{Op: Jump, A: 19},        // 0: skip to call site
	{Op: GetLocal, A: -3},    // 1: push i
	{Op: Push, A: 2},         // 2
	{Op: LowerInt},           // 3: i < 2
	{Op: JumpTrue, A: 13},    // 4: if i<2 goto 17
	{Op: Push, A: 1},         // 5: fib addr
	{Op: GetLocal, A: -3},    // 6: push i
	{Op: Push, A: 2},         // 7
	{Op: SubInt},             // 8: i-2
	{Op: Call, A: 1, B: 1},   // 9: fib(i-2)
	{Op: Push, A: 1},         // 10: fib addr
	{Op: GetLocal, A: -3},    // 11: push i
	{Op: Push, A: 1},         // 12
	{Op: SubInt},             // 13: i-1
	{Op: Call, A: 1, B: 1},   // 14: fib(i-1)
	{Op: AddInt},             // 15: sum
	{Op: Return, A: 1, B: 1}, // 16: return (recursive)
	{Op: GetLocal, A: -3},    // 17: base case
	{Op: Return, A: 1, B: 1}, // 18: return i
	{Op: Push, A: 1},         // 19: call site
	{Op: Push, A: 20},        // 20: n=20
	{Op: Call, A: 1, B: 1},   // 21
	{Op: Exit},               // 22
}

// fibImmCode is fib(20) rewritten with immediate-operand opcodes
// including CallImm. Data slot 0 holds the fib code address (1).
// fib function at addr 1, call site at addr 14.
var fibImmCode = []Instruction{
	{Op: Jump, A: 14},                 // 0: skip to call site
	{Op: GetLocal, A: -3},             // 1: push i
	{Op: LowerIntImm, A: 2},           // 2: i < 2
	{Op: JumpTrue, A: 9},              // 3: if i<2 goto 12
	{Op: GetLocal, A: -3},             // 4: push i
	{Op: SubIntImm, A: 2},             // 5: i-2
	{Op: CallImm, A: 0, B: 1<<16 | 1}, // 6: fib(i-2)
	{Op: GetLocal, A: -3},             // 7: push i
	{Op: SubIntImm, A: 1},             // 8: i-1
	{Op: CallImm, A: 0, B: 1<<16 | 1}, // 9: fib(i-1)
	{Op: AddInt},                      // 10: sum
	{Op: Return},                      // 11: return (recursive)
	{Op: GetLocal, A: -3},             // 12: base case
	{Op: Return},                      // 13: return i
	{Op: Push, A: 20},                 // 14: call site, push n=20
	{Op: CallImm, A: 0, B: 1<<16 | 1}, // 15: fib(20)
	{Op: Exit},                        // 16
}

func BenchmarkFibTyped(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := &Machine{}
		m.PushCode(fibTypedCode...)
		if err := m.Run(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFibImm(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := &Machine{}
		m.Push(Value{num: 1}) // data slot 0: fib code address
		m.PushCode(fibImmCode...)
		if err := m.Run(); err != nil {
			b.Fatal(err)
		}
	}
}

var tests = []struct {
	sym        []Value       // initial memory values
	code       []Instruction // bytecode to execute
	start, end int           //
	mem        string        // expected memory content
}{{ // #00 -- A simple addition.
	code: []Instruction{
		{Op: Push, A: 1},
		{Op: Push, A: 2},
		{Op: AddInt},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[3]",
}, { // #01 -- A simple subtraction.
	code: []Instruction{
		{Op: Push, A: 2},
		{Op: Push, A: 3},
		{Op: SubInt},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[-1]",
}, { // #02 -- A simple multiplication.
	code: []Instruction{
		{Op: Push, A: 3},
		{Op: Push, A: 2},
		{Op: MulInt},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[6]",
}, { // #03 -- lower.
	code: []Instruction{
		{Op: Push, A: 2},
		{Op: Push, A: 3},
		{Op: LowerInt},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[true]",
}, { // #04 -- greater.
	code: []Instruction{
		{Op: Push, A: 3},
		{Op: Push, A: 2},
		{Op: GreaterInt},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[true]",
}, { // #05 -- equal.
	code: []Instruction{
		{Op: Push, A: 2},
		{Op: Push, A: 3},
		{Op: Equal},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[false]",
}, { // #06 -- equalSet.
	code: []Instruction{
		{Op: Push, A: 2},
		{Op: Push, A: 3},
		{Op: EqualSet},
		{Op: Exit},
	},
	start: 0, end: 2, mem: "[2 false]",
}, { // #07 -- equalSet.
	code: []Instruction{
		{Op: Push, A: 3},
		{Op: Push, A: 3},
		{Op: EqualSet},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[true]",
}, { // #08 not.
	code: []Instruction{
		{Op: Push, A: 3},
		{Op: Push, A: 3},
		{Op: Equal},
		{Op: Not},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[false]",
}, { // #09 pop.
	code: []Instruction{
		{Op: Push, A: 3},
		{Op: Push, A: 2},
		{Op: Pop, A: 1},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[3]",
}, { // #10 -- Assign a local variable (frame-relative slot).
	sym: []Value{ValueOf(0)},
	code: []Instruction{
		{Op: Grow, A: 1},
		{Op: New, A: 2, B: 0},
		{Op: Push, A: 2},
		{Op: SetLocal, A: 2}, // mem[fp-1+2] = mem[sp]
		{Op: GetLocal, A: 2}, // push mem[fp-1+2] = mem[1] = 2
		{Op: Exit},
	},
	start: 1, end: 2, mem: "[2]",
}, { // #11 -- Calling a function defined outside the VM.
	sym: []Value{ValueOf(fmt.Println), ValueOf("Hello")},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: GetGlobal, A: 1},
		{Op: Call, A: 1},
		{Op: Exit},
	},
	start: 0, end: 2, mem: "[6 <nil>]",
}, { // #12 -- Defining and calling a function in VM.
	code: []Instruction{
		{Op: Jump, A: 3},         // 0
		{Op: Push, A: 3},         // 1
		{Op: Return, A: 1, B: 1}, // 2
		{Op: Push, A: 1},         // 3
		{Op: Push, A: 1},         // 4
		{Op: Call, A: 1, B: 1},   // 5
		{Op: Exit},               // 6
	},
	start: 0, end: 1, mem: "[3]",
}, { // #13 -- Defining and calling a function in VM.
	code: []Instruction{
		{Op: Jump, A: 5},         // 0
		{Op: Push, A: 3},         // 1
		{Op: SetLocal, A: -3},    // 2
		{Op: GetLocal, A: -3},    // 3
		{Op: Return, A: 1, B: 1}, // 4
		{Op: Push, A: 1},         // 5
		{Op: Push, A: 1},         // 6
		{Op: Call, A: 1, B: 1},   // 7
		{Op: Exit},               // 8
	},
	start: 0, end: 1, mem: "[3]",
}, { // #14 -- Fibonacci numbers, hand written. Showcase recursivity.
	code: []Instruction{
		{Op: Jump, A: 19},        // 0
		{Op: GetLocal, A: -3},    // 1  [2 i]
		{Op: Push, A: 2},         // 2  [2]
		{Op: LowerInt},           // 3  [true/false]
		{Op: JumpTrue, A: 13},    // 4  [], goto 17
		{Op: Push, A: 1},         // 5
		{Op: GetLocal, A: -3},    // 6  [i]
		{Op: Push, A: 2},         // 7  [i 2]
		{Op: SubInt},             // 8  [(i-2)]
		{Op: Call, A: 1, B: 1},   // 9  [fib(i-2)]
		{Op: Push, A: 1},         // 10
		{Op: GetLocal, A: -3},    // 11 [fib(i-2) i]
		{Op: Push, A: 1},         // 12 [(i-2) i 1]
		{Op: SubInt},             // 13 [(i-2) (i-1)]
		{Op: Call, A: 1, B: 1},   // 14 [fib(i-2) fib(i-1)]
		{Op: AddInt},             // 15 [fib(i-2)+fib(i-1)]
		{Op: Return, A: 1, B: 1}, // 16 return i
		{Op: GetLocal, A: -3},    // 17 [i]
		{Op: Return, A: 1, B: 1}, // 18 return i
		{Op: Push, A: 1},         // 19
		{Op: Push, A: 6},         // 20 [1]
		{Op: Call, A: 1, B: 1},   // 21 [fib(*1)]
		{Op: Exit},               // 22
	},
	start: 0, end: 1, mem: "[8]",
}, { // #15 -- clz32: leading zeros of 1 is 31.
	code: []Instruction{
		{Op: Push, A: 1},
		{Op: Clz32},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[31]",
}, { // #16 -- clz64: leading zeros of 1 is 63.
	code: []Instruction{
		{Op: Push, A: 1},
		{Op: Clz64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[63]",
}, { // #17 -- ctz32: trailing zeros of 8 is 3.
	code: []Instruction{
		{Op: Push, A: 8},
		{Op: Ctz32},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[3]",
}, { // #18 -- ctz64: trailing zeros of 16 is 4.
	code: []Instruction{
		{Op: Push, A: 16},
		{Op: Ctz64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[4]",
}, { // #19 -- popcnt32: popcount of 0xFF is 8.
	code: []Instruction{
		{Op: Push, A: 0xFF},
		{Op: Popcnt32},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[8]",
}, { // #20 -- popcnt64: popcount of 7 is 3.
	code: []Instruction{
		{Op: Push, A: 7},
		{Op: Popcnt64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[3]",
}, { // #21 -- rotl32: rotate left 1 by 3 = 8.
	code: []Instruction{
		{Op: Push, A: 1},
		{Op: Push, A: 3},
		{Op: Rotl32},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[8]",
}, { // #22 -- rotl64: rotate left 1 by 3 = 8.
	code: []Instruction{
		{Op: Push, A: 1},
		{Op: Push, A: 3},
		{Op: Rotl64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[8]",
}, { // #23 -- rotr32: rotate right 8 by 3 = 1.
	code: []Instruction{
		{Op: Push, A: 8},
		{Op: Push, A: 3},
		{Op: Rotr32},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[1]",
}, { // #24 -- rotr64: rotate right 8 by 3 = 1.
	code: []Instruction{
		{Op: Push, A: 8},
		{Op: Push, A: 3},
		{Op: Rotr64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[1]",
}, { // #25 -- absFloat64: abs(-3.14) = 3.14.
	sym: []Value{ValueOf(-3.14)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: AbsFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[3.14]",
}, { // #26 -- sqrtFloat64: sqrt(4) = 2.
	sym: []Value{ValueOf(4.0)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: SqrtFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[2]",
}, { // #27 -- ceilFloat64: ceil(3.2) = 4.
	sym: []Value{ValueOf(3.2)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: CeilFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[4]",
}, { // #28 -- floorFloat64: floor(3.7) = 3.
	sym: []Value{ValueOf(3.7)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: FloorFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[3]",
}, { // #29 -- truncFloat64: trunc(3.9) = 3.
	sym: []Value{ValueOf(3.9)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: TruncFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[3]",
}, { // #30 -- nearestFloat64: nearest(2.5) = 2 (round to even).
	sym: []Value{ValueOf(2.5)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: NearestFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[2]",
}, { // #31 -- minFloat64: min(1.5, 2.5) = 1.5.
	sym: []Value{ValueOf(1.5), ValueOf(2.5)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: GetGlobal, A: 1},
		{Op: MinFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[1.5]",
}, { // #32 -- maxFloat64: max(1.5, 2.5) = 2.5.
	sym: []Value{ValueOf(1.5), ValueOf(2.5)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: GetGlobal, A: 1},
		{Op: MaxFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[2.5]",
}, { // #33 -- copysignFloat64: copysign(3.0, -1.0) = -3.
	sym: []Value{ValueOf(3.0), ValueOf(-1.0)},
	code: []Instruction{
		{Op: GetGlobal, A: 0},
		{Op: GetGlobal, A: 1},
		{Op: CopysignFloat64},
		{Op: Exit},
	},
	start: 0, end: 1, mem: "[-3]",
}}
