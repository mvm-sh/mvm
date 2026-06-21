package interptest

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// A blank const (`_ = -iota`) must not register a symbol under the "_" key.
// It did, so the blank ident in a later tuple-assign LHS (`_, n = f()`)
// resolved to that const and compiled to a field-set on a non-addressable
// const value -> "reflect.Value.SetInt using unaddressable value".
// Was protobuf encoding/protowire consumeFieldValueD (TestGroup).
func TestBlankConstNoShadow(t *testing.T) {
	run(t, []etest{
		{
			n: "iota_blank_then_tuple_assign",
			src: `
				const ( _ = -iota; errA; errB )
				func two() (uint32, int) { return 7, 4 }
				func f() (n int) { _, n = two(); return n }
				f()
			`,
			res: "4",
		},
		{
			n:   "blank_const_advances_iota",
			src: `const ( _ = iota; a; b ); a + b`,
			res: "3",
		},
		{
			n: "two_blank_consts",
			src: `
				const _ = 1
				const _ = 2
				func two() (int, int) { return 1, 9 }
				func g() (n int) { _, n = two(); return n }
				g()
			`,
			res: "9",
		},
	})
}

// A multi-RHS `:=` must evaluate every RHS before assigning any LHS.
// Was govalidator IsCreditCard: `number, lastDigit := number/10, number%10`
// updated number before lastDigit's RHS read it.
func TestDefineMultiRHSEvalOrder(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"redeclare_reads_old", `f := func(n int64) (int64, int64) { n, d := n/10, n%10; return n, d }; a, b := f(129); fmt.Sprint(a, b)`, "12 9"},
		{"both_reuse", `x, y := 3, 4; x, y = y, x; fmt.Sprint(x, y)`, "4 3"},
		{"mixed_new_old", `f := func() string { x := 10; x, y := x%3, x*2; return fmt.Sprint(x, y) }; f()`, "1 20"},
		{"shadow_outer", `x := 5; r := ""; { x, y := x+1, x+2; r = fmt.Sprint(x, y) }; r`, "6 7"},
		{"blank_lhs", `f := func() int { x := 10; x, _ := x+1, x+2; return x }; f()`, "11"},
		{"global_redeclare", `x := 10; x, y := x%3, x*2; fmt.Sprint(x, y)`, "1 20"},
		{"global_blank", `x := 10; x, _ := x+1, x+2; x`, "11"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			r, err := i.Eval(c.n, c.src)
			if err != nil {
				t.Fatalf("eval %q: %v", c.src, err)
			}
			if got := fmt.Sprintf("%v", r); got != c.res {
				t.Errorf("got %q, want %q", got, c.res)
			}
		})
	}
}

// A non-name on the left side of := must be a compile error, as in gc
// (a misaligned Define used to nil-panic at runtime).
func TestDefineNonNameLHS(t *testing.T) {
	// A `*p, b :=` statement fails earlier in the parser; only assert it errors.
	cases := []struct {
		n, src string
		msg    string
	}{
		{"indexed", `a := []int{0}; a[0], b := 1, 2; _ = b`, "non-name"},
		{"field", `type s struct{ f int }; v := s{}; v.f, b := 1, 2; _ = b`, "non-name"},
		{"deref", `x := 1; p := &x; *p, b := 1, 2; _ = b`, ""},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			_, err := i.Eval(c.n, c.src)
			if err == nil || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("want compile error containing %q, got %v", c.msg, err)
			}
		})
	}
}

// A spread call (f(s...)) through a func value that crosses the native
// boundary must use reflect.CallSlice, including when the callee symbol is a
// declared local (the flag was only set for non-spread variadic calls).
// Was govalidator typeCheck: validatefunc(field, ps[1:]...) on a
// ParamValidator pulled from a map panicked in reflect.Call.
func TestSpreadCallNativeBoundary(t *testing.T) {
	src := `
package main

import "fmt"

type pv func(s string, params ...string) bool

var m = map[string]pv{"k": func(s string, params ...string) bool {
	return fmt.Sprint(s, params) == "x[2 3]"
}}

func main() {
	ps := []string{"a", "2", "3"}
	vf := m["k"]
	if !vf("x", ps[1:]...) {
		panic("spread args mismatch")
	}
	fmt.Println("ok")
}
`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("spread_native", src); err != nil {
		t.Fatalf("eval: %v", err)
	}
}

// `a, b := x, nil` where b is an existing same-scope var is an assignment to
// the already-typed b (gonum/mat list_test.go: `wasEmpty, empty := empty,
// nil`). The Define compile path errored "undefined" on the untyped-nil RHS;
// it now keeps the rebound var's type and coerces concrete nilables.
func TestDefineNilRebind(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"iface", `f := func() bool { var e error = fmt.Errorf("x"); was, e := e, nil; return was != nil && e == nil }; f()`, "true"},
		{"map", `f := func() bool { m := map[string]int{"a": 1}; was, m := m, nil; return len(was) == 1 && m == nil && len(m) == 0 }; f()`, "true"},
		{"slice", `f := func() bool { s := []int{1, 2}; was, s := s, nil; return len(was) == 2 && s == nil && len(s) == 0 }; f()`, "true"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			r, err := i.Eval(c.n, c.src)
			if err != nil {
				t.Fatalf("eval %q: %v", c.src, err)
			}
			if got := fmt.Sprintf("%v", r); got != c.res {
				t.Errorf("got %q, want %q", got, c.res)
			}
		})
	}
}

// A bare-nil rebind to a non-nilable var stays a compile error, as in gc.
func TestDefineNilRebindNonNilable(t *testing.T) {
	src := `f := func() int { x := 1; x, y := nil, 2; return x + y }; f()`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("nonnilable", src); err == nil || !strings.Contains(err.Error(), "cannot use nil") {
		t.Errorf("got err %v, want containing %q", err, "cannot use nil")
	}
}

// An untyped-const case value must fold to the switch operand's type
// (gonum/mat normLapack: `switch norm {case 2:}` with norm float64 fell to
// default). The operand type rides on the EqualSet token; the compiler's
// stack model loses it past the first case of the chain.
func TestSwitchConstCaseOperandType(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"float64_first", `x := 1.0; s := ""; switch x { case 1: s = "one"; case 2: s = "two"; default: s = "other" }; s`, "one"},
		{"float64_second", `x := 2.0; s := ""; switch x { case 1: s = "one"; case 2: s = "two"; default: s = "other" }; s`, "two"},
		{"float64_default", `x := 3.5; s := ""; switch x { case 1: s = "one"; case 2: s = "two"; default: s = "other" }; s`, "other"},
		{"float_case_int_operand_expr", `f := func(x float64) string { switch x { case 1: return "one"; case 2.5: return "half" }; return "other" }; f(2.5)`, "half"},
		{"literal_first_binary_operand", `f := 1.75; s := ""; switch 2 * f { case 3.5: s = "hit"; default: s = "miss" }; s`, "hit"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			r, err := i.Eval(c.n, c.src)
			if err != nil {
				t.Fatalf("eval %q: %v", c.src, err)
			}
			if got := fmt.Sprintf("%v", r); got != c.res {
				t.Errorf("got %q, want %q", got, c.res)
			}
		})
	}
}

// A case value gc rejects must not silently truncate-and-match.
func TestSwitchConstCaseErrors(t *testing.T) {
	cases := []struct{ n, src, errSub string }{
		{"overflow", `var b byte = 44; s := ""; switch b { case 300: s = "hit" }; s`, "overflows"},
		{"truncated", `i := 2; s := ""; switch i { case 2.5: s = "hit" }; s`, "truncated"},
		{"mismatched_var", `i := 2; f := 2.5; s := ""; switch i { case f: s = "hit" }; s`, "mismatched types"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			if _, err := i.Eval(c.n, c.src); err == nil || !strings.Contains(err.Error(), c.errSub) {
				t.Errorf("eval %q: got err %v, want containing %q", c.src, err, c.errSub)
			}
		})
	}
}

func TestSwitchOperandLeak(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"no_match_loop", `n := 0; for i := 0; i < 2000; i++ { switch i % 251 { case -1: n++ } }; n`, "0"},
		{"rare_match_loop", `n := 0; for i := 0; i < 2000; i++ { switch i % 251 { case 7: n++ } }; n`, "8"},
		{"multi_value_loop", `n := 0; for i := 0; i < 2000; i++ { switch i % 251 { case 1, 2, 3: n++ } }; n`, "24"},
		{"last_case_match", "n := 0\nfor i := 0; i < 2000; i++ {\n\tswitch 7 {\n\tcase 6:\n\tcase 7:\n\t\tn++\n\t}\n}\nn", "2000"},
		{"with_default", "n := 0\nfor i := 0; i < 2000; i++ {\n\tswitch i % 251 {\n\tcase 7:\n\tdefault:\n\t\tn++\n\t}\n}\nn", "1992"},
		{"return_bodies", "f := func(s int) string {\n\tswitch s {\n\tcase 1:\n\t\treturn \"small\"\n\tcase 2:\n\t\treturn \"large\"\n\t}\n\treturn \"other\"\n}\nout := \"\"\nfor i := 0; i < 2000; i++ {\n\tout = f(3)\n}\nout", "other"},
		{"nested_default", "n := 0\nfor s := 0; s < 3; s++ {\n\tswitch s {\n\tcase 0:\n\t\tswitch s {\n\t\tdefault:\n\t\t\tn++\n\t\t}\n\tcase 1:\n\t\tswitch s {\n\t\tdefault:\n\t\t\tn += 10\n\t\t}\n\tcase 2:\n\t\tn += 100\n\t}\n}\nn", "111"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			r, err := i.Eval(c.n, c.src)
			if err != nil {
				t.Fatalf("eval %q: %v", c.src, err)
			}
			if got := fmt.Sprintf("%v", r); got != c.res {
				t.Errorf("got %q, want %q", got, c.res)
			}
		})
	}
}

// A typed const-conversion (int8(4)) passed straight to an any param used to
// box as int: it loads via immediate Push (ref collapses to int) and the int8
// lived only in Iface.Typ, which the bridge ignored. bridgeIface now rebuilds
// the numeric value from num via Iface.Typ.
func TestTypedConstArgPreservesType(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

func main() {
	fmt.Println(reflect.ValueOf(int8(4)).Type())
	fmt.Println(reflect.ValueOf(uint16(7)).Type())
	fmt.Printf("%T %T\n", int8(4), uint32(9))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("typed_const_arg.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "int8\nuint16\nint8 uint32\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestUnaryPlusPrecedence guards unary `+` precedence: it was missing from the
// TokenProps table, so `ord == +1` mis-parsed and corrupted the compile stack.
func TestUnaryPlusPrecedence(t *testing.T) {
	run(t, []etest{
		{n: "eq_plus", src: `ord := 1; ord == +1`, res: "true"},
		{n: "eq_minus", src: `ord := 1; ord == -1`, res: "false"},
		{n: "and_eq_plus", src: `a, b, ord := 3, 1, 1; a > b && ord == +1`, res: "true"},
		{n: "unary_var", src: `ord := 5; +ord`, res: "5"},
		{n: "mixed_unary", src: `+5 * -3`, res: "-15"},
		{n: "short_circuit_chain", src: `
			t1, t2, s1, s2, ord := 2, 1, 2, 1, 1
			t1 == t2 ||
				(t1 > t2 && s1 > s2 && ord == +1) ||
				(t1 < t2 && s1 < s2 && ord == -1)`, res: "true"},
	})
}

// A spread call split across lines carries a trailing comma after the
// ellipsis (`f(a,\n b...,\n)`); spread detection must skip it or the slice
// is bound to the first variadic slot. Was gjson TestManyBasic:
// "reflect.Set: value of type []string is not assignable to type string".
func TestVariadicSpreadTrailingComma(t *testing.T) {
	src := `
f := func(prefix string, parts ...string) string {
	out := prefix
	for _, p := range parts {
		out += "," + p
	}
	return out
}
parts := []string{"a", "b"}
f(
	"p",
	parts...,
)`
	i := newAutoImportInterp(t)
	r, err := i.Eval("spread", src)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got, want := fmt.Sprintf("%v", r), "p,a,b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// `return a || b` (and `&&`) whose final operand is a bare local fuses the
// trailing GetLocal into GetLocalReturn, which elides the separate Return. But
// the short-circuit JumpSetTrue/JumpSetFalse target a merge label sitting at
// that elided Return: with it gone the jump fell through into the next
// function's body, cascading through the whole program's func-definition
// skip-jumps into main and re-running it (an infinite loop). Minimized from
// `mvm test google.golang.org/protobuf/reflect/protodesc` (isValidFieldNumber:
// `return MinValidNumber <= n && (n <= MaxValidNumber || isMessageSet)`).
func TestShortCircuitTailReturn(t *testing.T) {
	src := `package main

import "fmt"

func or(a, b bool) bool      { return a || b }
func and(a, b bool) bool     { return a && b }
func valid(n, mx int, m bool) bool { return 1 <= n && (n <= mx || m) }

func main() {
	fmt.Println(or(true, false), or(false, false))
	fmt.Println(and(true, true), and(true, false))
	fmt.Println(valid(5, 10, false), valid(20, 10, false), valid(20, 10, true))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("shortcircuit_return.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "true false\ntrue false\ntrue false true\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}

// A := inside an if/else-if body must not leak into later else-if conditions
// or sibling bodies: parseIf parses the whole chain in one scope, so each
// body gets its own sub-scope. Was tidwall/pretty appendPrettyObject (a
// shadowed `max :=` in the then-body made `max != -1` read the uninitialized
// body slot in the else-if condition).
func TestIfBodyScope(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"elseif_cond", `x := -1; r := ""; if x == 0 { x := 5; _ = x } else if x != -1 { r = "bad" } else { r = "good" }; r`, "good"},
		{"chained", `x := -1; r := ""; if x == 0 { x := 1; _ = x } else if x == 1 { x := 2; _ = x } else if x != -1 { r = "bad" } else { r = "good" }; r`, "good"},
		{"sibling_bodies", `x := 1; a := 0; if x == 1 { y := 10; a = y } else { y := 20; a = y }; a`, "10"},
		{"init_visible_in_body", `a := 0; if v := 7; v > 0 { a = v }; a`, "7"},
	}
	for _, c := range cases {
		t.Run(c.n, func(t *testing.T) {
			i := newAutoImportInterp(t)
			r, err := i.Eval(c.n, c.src)
			if err != nil {
				t.Fatalf("eval %q: %v", c.src, err)
			}
			if got := fmt.Sprintf("%v", r); got != c.res {
				t.Errorf("got %q, want %q", got, c.res)
			}
		})
	}
}

// TestInitRunsOncePerEval guards a re-entrance regression where a package init
// function re-ran on every later Eval.
func TestInitRunsOncePerEval(t *testing.T) {
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.AutoImportPackages()

	if _, err := i.Eval("m:init", "var initRuns int\nfunc init() { initRuns++ }"); err != nil {
		t.Fatalf("first eval: %v", err)
	}
	// A second, unrelated Eval must not re-run the init defined above.
	if _, err := i.Eval("m:more", "var x = 1\n_ = x"); err != nil {
		t.Fatalf("second eval: %v", err)
	}
	res, err := i.Eval("m:read", "initRuns")
	if err != nil {
		t.Fatalf("read eval: %v", err)
	}
	if got := res.Interface(); got != 1 {
		t.Fatalf("init ran %v times, want 1", got)
	}
}

// An early `return` from inside a range loop compiles to the fused GetLocalReturn
// opcode (single-local return, no defers). That opcode used to skip the iterator
// unwind that plain Return does, leaking the loop iterator on m.iterStack. The
// next outer range step then read the inner (string) iterator and tried to assign
// a rune to a string loop var: "reflect.Set: value of type int32 is not
// assignable to type string". Minimized from `mvm test unicode/utf8`
// (TestDecodeInvalidSequence -> runtimeDecodeRune).
func TestRangeEarlyReturnIteratorLeak(t *testing.T) {
	src := `package main

import "fmt"

var tests = []string{"ab", "cd", "ef"}

// firstRune ranges over a string and returns from inside the loop.
func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return -1
}

func main() {
	for _, s := range tests {
		fmt.Printf("%q %#x\n", s, firstRune(s))
	}
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("range_early_return.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "\"ab\" 0x61\n\"cd\" 0x63\n\"ef\" 0x65\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}
