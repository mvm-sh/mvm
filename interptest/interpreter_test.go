package interptest

import (
	"bytes"
	"fmt"
	"log"
	"strconv"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/goparser"
	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/vm"
)

type etest struct {
	n, src, res, err string
	skip             bool
}

func init() {
	log.SetFlags(log.Lshortfile)
}

func gen(test etest) func(*testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		if test.skip {
			t.Skip()
		}
		intp := interp.NewInterpreter(golang.GoSpec)
		intp.ImportPackageValues(stdlib.Values)
		errStr := ""
		r, e := intp.Eval("test", test.src)
		t.Log(r, e)
		if e != nil {
			errStr = e.Error()
		}
		if !strings.Contains(errStr, test.err) {
			t.Errorf("got error %#v, want error %#v", errStr, test.err)
		}
		if res := fmt.Sprintf("%v", r); test.err == "" && res != test.res {
			t.Errorf("got %#v, want %#v", res, test.res)
		}
	}
}

func run(t *testing.T, tests []etest) {
	for _, test := range tests {
		t.Run(test.n, gen(test))
	}
}

const fibSrc = `
func fib(i int) int {
	if i < 2 { return i }
	return fib(i-2) + fib(i-1)
}
`

func BenchmarkFib(b *testing.B) {
	intp := interp.NewInterpreter(golang.GoSpec)
	if _, err := intp.Eval("fib", fibSrc); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := intp.Eval("bench", "fib(20)"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAppend(b *testing.B) {
	intp := interp.NewInterpreter(golang.GoSpec)
	if _, err := intp.Eval("setup", `
		func appendN(n int) []int {
			s := []int{}
			for i := 0; i < n; i++ {
				s = append(s, i, i+1, i+2)
			}
			return s
		}
	`); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := intp.Eval("bench", "appendN(100)"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAppendLarge(b *testing.B) {
	args := make([]string, 100)
	for i := range args {
		args[i] = strconv.Itoa(i)
	}
	src := "func appendLarge() []int { s := []int{}; s = append(s, " + strings.Join(args, ", ") + "); return s }"
	intp := interp.NewInterpreter(golang.GoSpec)
	if _, err := intp.Eval("setup", src); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := intp.Eval("bench", "appendLarge()"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestExpr(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "", res: "<invalid reflect.Value>"},
		{n: "#01", src: "1+2", res: "3"},
		{n: "#02", src: "1+", err: "block not terminated"},
		{n: "#03", src: "a := 1 + 2; b := 0; a + 1", res: "4"},
		{n: "#04", src: "1+(2+3)", res: "6"},
		{n: "#05", src: "(1+2)+3", res: "6"},
		{n: "#06", src: "(6+(1+2)+3)+5", res: "17"},
		{n: "#07", src: "(6+(1+2+3)+5", err: "1:1: block not terminated"},
		{n: "#08", src: "a := 2; a = 3; a", res: "3"},
		{n: "#09", src: "2 * 3 + 1 == 7", res: "true"},
		{n: "#10", src: "7 == 2 * 3 + 1", res: "true"},
		{n: "#11", src: "1 + 3 * 2 == 2 * 3 + 1", res: "true"},
		{n: "#12", src: "a := 1 + 3 * 2 == 2 * 3 + 1; a", res: "true"},
		{n: "#13", src: "-2", res: "-2"},
		{n: "#14", src: "-2 + 5", res: "3"},
		{n: "#15", src: "5 + -2", res: "3"},
		{n: "#16", src: "!false", res: "true"},
		{n: "#17", src: `a := "hello"`, res: "hello"},
		{n: "non_ascii_ident", src: `ж := 42; ж`, res: "42"},
		{n: "map_index_slice_key", src: `m := map[string]int{"ab": 7}; s := "xabz"; m[s[1:3]]`, res: "7"},
		{n: "assign_unexported_field_from_map", src: `type T struct{ s string }; m := map[int]T{0: {s: "x"}}; var out string; out = m[0].s; out`, res: "x"},
	})
}

func TestNumericWidening(t *testing.T) {
	run(t, []etest{
		// float64 var < untyped int constant: comparison must widen the
		// constant to float64 instead of bit-reinterpreting Push(10) as float bits.
		{n: "float_lt_int_const", src: `var v float64 = 5.5; v < 10`, res: "true"},
		{n: "float_ge_int_const", src: `var v float64 = 5.5; v >= 10`, res: "false"},
		{n: "float_lt_int_const_eq", src: `var v float64 = 10.0; v < 10`, res: "false"},

		// map[float64]V composite literal with int-typed key literals: keys
		// must be float64 in the resulting map so float64 lookups hit.
		{n: "map_float_key_int_lit", src: `m := map[float64]string{-30: "q", 0: "z", 3: "k"}; m[3.0] + m[-30.0] + m[0.0]`, res: "kqz"},

		// float64 -> uint64 conversion for values above MaxInt64 must
		// not saturate through an int64 intermediate.
		{n: "uint64_from_large_float", src: `var f float64 = 1.25e19; uint64(f)`, res: "12500000000000000000"},

		// math.MaxUint64 is bound as a typed uint64 in stdlib; comparing
		// float64 against it must widen the uint64 to float64.
		{n: "float_ge_math_maxuint64", src: `import "math"; var f float64 = 42; f >= math.MaxUint64`, res: "false"},

		// float32 var compared against an untyped float const: the const
		// must narrow to float32 precision before the bit compare, otherwise
		// Float64bits(-172e12) != Float64bits(float64(float32(-172e12))).
		{n: "float32_eq_untyped_const", src: `var v float32 = -172e12; v == -172e12`, res: "true"},
		{n: "float32_neq_untyped_const", src: `var v float32 = -172e12; v != -172e12`, res: "false"},
		{n: "float32_const_eq_var", src: `var v float32 = -172e12; -172e12 == v`, res: "true"},
		// float64 var vs int untyped const: int must widen to float64.
		{n: "float64_eq_int_const", src: `var v float64 = 10.0; v == 10`, res: "true"},
		{n: "float64_neq_int_const", src: `var v float64 = 11.0; v != 10`, res: "true"},

		// Untyped const folds per use: a uint64 compare must not fix its type and
		// overflow the later int64 negation.
		{n: "untyped_const_multi_context", src: `const c = 1 << 63; func f(n uint64) int64 { if n == c { return -c }; return 0 }; f(1 << 63)`, res: "-9223372036854775808"},

		// Named untyped const (Type==nil, value carries the kind) must convert to
		// the destination type, not pass raw float/int bits.
		{n: "named_float_const_to_int_var", src: `const c = 6.0; var x int = c; x`, res: "6"},
		{n: "named_int_const_to_float_var", src: `const k = 5; var y float64 = k; y`, res: "5"},
		{n: "named_float_const_make_len", src: `const n = 6.0; len(make([]float64, n))`, res: "6"},
		{n: "named_float_const_func_arg", src: `func f(n int) int { return n }; const c = 6.0; f(c)`, res: "6"},
		{n: "named_float_const_slice_elem", src: `const c = 6.0; []int{c, 3}[0]`, res: "6"},
		{n: "named_int_const_array_elem", src: `const k = 3; [2]float64{k, 6.0}[0]`, res: "3"},
		// make with a float exponent literal length (gonum distuv pattern).
		{n: "float_exp_const_make_len", src: `len(make([]int, 3e3))`, res: "3000"},

		// #30: two differently-typed numeric vars error like gc (no implicit
		// widen); only an untyped constant operand widens.
		{n: "int_var_div_float_var", src: `a := 10; b := 12.5; a / b`, err: "mismatched types int and float64"},
		{n: "int_var_add_float_var", src: `a := 10; b := 12.5; a + b`, err: "mismatched types int and float64"},
		{n: "float_var_lt_int_var", src: `a := 10; b := 12.5; b < a`, err: "mismatched types float64 and int"},
		{n: "int_var_eq_float_var", src: `a := 10; b := 12.5; a == b`, err: "mismatched types int and float64"},
		{n: "named_int_add_int_var", src: `type I int; var x I = 1; y := 2; x + y`, err: "mismatched types main.I and int"},
		{n: "computed_int_div_float_var", src: `a := 3; b := 1.5; (a*2) / b`, err: "mismatched types int and float64"},
		// Bitwise ops reject mismatched typed operands like gc too.
		{n: "uint64_var_or_int_var", src: `var u uint64 = 6; i := 3; u | i`, err: "mismatched types uint64 and int"},
		{n: "uint64_var_and_int_var", src: `var u uint64 = 6; i := 3; u & i`, err: "mismatched types uint64 and int"},
		{n: "uint64_var_xor_int_var", src: `var u uint64 = 6; i := 3; u ^ i`, err: "mismatched types uint64 and int"},
		{n: "uint64_var_andnot_int_var", src: `var u uint64 = 6; i := 3; u &^ i`, err: "mismatched types uint64 and int"},
		// An untyped-const operand (incl. a context-typed shift result) stays accepted.
		{n: "uint64_var_or_const", src: `var u uint64 = 6; u | 1`, res: "7"},
		{n: "uint64_var_or_shift", src: `var u uint64 = 6; var s uint = 0; u | 1<<s`, res: "7"},
		{n: "float_ge_maxuint64_const", src: `import "math"; var f float64 = 42; f >= math.MaxUint64`, res: "false"},
		// Untyped const widens in arithmetic too (was NaN; x == x is false only for NaN).
		{n: "float_add_maxuint64_const", src: `import "math"; var f float64 = 42; x := f + math.MaxUint64; x == x`, res: "true"},

		// Float equality must follow IEEE, not raw bit equality: NaN != NaN and
		// +0.0 == -0.0 (cmp's TestLess/TestCompare via isNaN's `x != x`).
		{n: "nan_not_equal_self", src: `import "math"; x := math.NaN(); x != x`, res: "true"},
		{n: "nan_eq_self_false", src: `import "math"; x := math.NaN(); x == x`, res: "false"},
		{n: "neg_zero_eq_pos_zero", src: `import "math"; math.Copysign(0, -1) == 0.0`, res: "true"},
	})
}

// TestConstConvertFolding checks that an untyped-constant operand folds to its
// context type at compile time (no runtime Convert), counting Convert ops in the
// compile-only bytecode. Value tests can't catch this -- a runtime Convert is
// also correct.
func TestConstConvertFolding(t *testing.T) {
	count := func(src string) int {
		intp := interp.NewInterpreter(golang.GoSpec)
		intp.ImportPackageValues(stdlib.Values)
		if err := intp.Compile("test", src); err != nil {
			t.Fatalf("compile %q: %v", src, err)
		}
		n := 0
		for _, ins := range intp.Code {
			if ins.Op == vm.Convert {
				n++
			}
		}
		return n
	}
	cases := []struct {
		name, src string
		want      int
	}{
		{"baseline", `1`, 0},
		{"var_add_const", `b := 12.5; b + 2`, 0},
		{"const_add_var", `b := 12.5; 2 + b`, 0}, // const buried below the var load
		{"var_lt_const", `var v float64 = 5.5; v < 10`, 0},
		{"const_lt_var", `var v float64 = 5.5; 10 < v`, 0},
		{"var_eq_const", `var v float64 = 10.0; v == 10`, 0},
		{"explicit_conv", `a := 10; b := 12.5; float64(a) / b`, 1}, // the explicit float64(a)
		{"named_pkg_const", `import "math"; var f float64 = 42; f >= math.MaxUint64`, 2},
	}
	for _, c := range cases {
		if got := count(c.src); got != c.want {
			t.Errorf("%s: %q Convert count = %d, want %d", c.name, c.src, got, c.want)
		}
	}
}

func TestParseErrorPos(t *testing.T) {
	run(t, []etest{
		{n: "import_in_func", src: `func main() { import "fmt" }`, err: `test:1:15: unexpected import inside function body`},
		{n: "var_no_expr_in_func", src: `func main() { var }`, err: `test:1:15: missing expression after var`},
		{n: "var_no_name", src: `var = "x"`, err: `missing variable name in var declaration`},
		{n: "var_no_name_in_func", src: `func main() { var = "x" }`, err: `missing variable name in var declaration`},
		{n: "var_assign_mismatch", src: `var a = 1, 2`, err: `assignment mismatch: 1 variables but 2 values`},
		{n: "assign_mismatch_few", src: `func main() { a, b := "a"; _, _ = a, b }; main()`, err: `assignment mismatch: 2 variables but 1 value`},
		{n: "assign_mismatch_many", src: `func main() { a, b := "a", "b", "c"; _, _ = a, b }; main()`, err: `assignment mismatch: 2 variables but 3 values`},
		{n: "assign_mismatch_single_lhs", src: `func main() { var a int; a = 1, 2; _ = a }; main()`, err: `assignment mismatch: 1 variables but 2 values`},
		// Compiler-side ErrUndefined now carries source position too.
		{n: "undefined_method", src: `type T struct{}; var t T; t.NoSuchMethod()`, err: `test:1:28: undefined: NoSuchMethod`},
		// A missing pkg-qualified symbol now reports file:line:col so `mvm test`
		// can pinpoint (and drop) the offending bridged-stdlib test file.
		{n: "pkg_symbol_not_found", src: "import \"strings\"\nstrings.NoSuchThing(\"x\")", err: `test:2:8: symbol not found in package strings: NoSuchThing`},
	})
}

func TestAssign(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "var a int = 1; a", res: "1"},
		{n: "#01", src: "var a, b int = 1, 2; b", res: "2"},
		{n: "#02", src: "var a, b int; a, b = 1, 2; b", res: "2"},
		{n: "#03", src: "a, b := 1, 2; b", res: "2"},
		{n: "#04", src: "func f() int {return 2}; a := f(); a", res: "2"},
		{n: "#05", src: "func f() (int, int) {return 2, 3}; a, b := f(); b", res: "3"},
		{n: "#06", src: "func f() (int, int) {return 2, 3}; var a, b = f(); b", res: "3"},
		{n: "#07", src: "func f() (int, int) {return 2, 3}; _, b := f(); b", res: "3"},
		{n: "#08", src: "func f() (int, int, int) {return 1, 2, 3}; a, b, c := f(); a*100+b*10+c", res: "123"},
		{n: "#09", src: "func f(x int) (int, int) {return x, x+1}; a, b := f(5); a*10+b", res: "56"},
		{n: "#10", src: "func f() (int, int) {return 2, 3}; func g() int { a, b := f(); return a+b }; g()", res: "5"},
		{n: "#11", src: "a, b := 1, 2; a, b = b, a; 10*a+b", res: "21"},
		{n: "#12", src: "func f() int { a, b := 1, 2; a, b = b, a; return 10*a+b }; f()", res: "21"},
		{n: "#13", src: "var g int; func f() int { l := 1; g, l = l, g; return 10*g+l }; g = 2; f()", res: "12"},
		{n: "#14", src: "_ = 1+1; 42", res: "42"},
		{n: "#15", src: "func f() (int, int) {return 2, 3}; var a, b int = f(); a+b", res: "5"},
		{n: "#16", src: "func f() (int, int) {return 2, 3}; func g(i, j int) int {return i+j}; g(f())", res: "5"},
		// multi-assign to struct fields
		{n: "#17", src: "type T struct{v int}; func f() (int,error) {return 2,nil}; t:=&T{}; t.v,_=f(); t.v", res: "2"},
		{n: "#18", src: "type T struct{v,w int}; func f() (int,int) {return 1,2}; t:=&T{}; t.v,t.w=f(); 10*t.v+t.w", res: "12"},
		{n: "#19", src: "type T struct{v interface{}}; func f() (int64,error) {return 2,nil}; t:=&T{}; t.v,_=f(); t.v.(int64)", res: "2"},
		{n: "#20", src: "type T struct{v int}; func f() (int,int) {return 1,2}; t:=&T{}; var a int; a,t.v=f(); 10*a+t.v", res: "12"},
		// indexed tuple swap
		{n: "#21", src: "func f() int { s := []int{3,1,2}; s[0],s[1] = s[1],s[0]; return 100*s[0]+10*s[1]+s[2] }; f()", res: "132"},
		{n: "#22", src: "func f() int { s := []int{1,2,3}; s[0],s[1],s[2] = s[1],s[2],s[0]; return 100*s[0]+10*s[1]+s[2] }; f()", res: "231"},
		// mixed: one LHS is index, other is variable
		{n: "#23", src: "func f() int { s := []int{10,20}; a := 0; a, s[0] = s[0], a; return a*10 + s[0] }; f()", res: "100"},
		// map tuple swap
		{n: "#24", src: `func f() int { m := map[string]int{"a": 1, "b": 2}; m["a"], m["b"] = m["b"], m["a"]; return m["a"]*10 + m["b"] }; f()`, res: "21"},
		// array (not slice) tuple swap
		{n: "#25", src: "func f() int { a := [3]int{5,3,1}; a[0], a[2] = a[2], a[0]; return 100*a[0]+10*a[1]+a[2] }; f()", res: "135"},
		// pointer deref tuple swap
		{n: "#26", src: "func f() int { a, b := 1, 2; pa, pb := &a, &b; *pa, *pb = *pb, *pa; return a*10+b }; f()", res: "21"},
		{n: "#27_ptr_swap", src: "func f() int { a, b := 1, 2; p, q := &a, &b; p, q = q, p; return *p*10+*q }; f()", res: "21"},
		{n: "#28_structptr_swap", src: "func f() int { type T struct{v int}; x, y := T{1}, T{2}; p, q := &x, &y; p, q = q, p; return p.v*10+q.v }; f()", res: "21"},
		{n: "#29_ptr_rotate", src: "func f() int { a, b, c := 1, 2, 3; p, q, r := &a, &b, &c; p, q, r = r, p, q; return *p*100+*q*10+*r }; f()", res: "312"},
		{n: "#30_slice_swap", src: "func f() int { s, t := []int{1}, []int{2}; s, t = t, s; return s[0]*10+t[0] }; f()", res: "21"},
		{n: "#31_string_swap", src: `func f() string { s, t := "a", "b"; s, t = t, s; return s+t }; f()`, res: "ba"},
		{n: "#32_map_swap", src: `func f() int { m, n := map[string]int{"x":1}, map[string]int{"x":2}; m, n = n, m; return m["x"]*10+n["x"] }; f()`, res: "21"},
		{n: "#33_iface_swap", src: "func f() int { var x, y interface{} = 1, 2; x, y = y, x; return x.(int)*10+y.(int) }; f()", res: "21"},
		// Multi-assign with a bare nil RHS: a bare nil aliases no LHS and has no type
		// to define a swap temp from, so it is assigned directly (not via `_swap_i_ := nil`).
		{n: "multi_assign_bare_nil", src: "func f() int { var a, b []int; a = []int{1}; b = []int{2}; a, b = nil, nil; return len(a)+len(b) }; f()", res: "0"},
		{n: "multi_assign_nil_mixed", src: "func f() int { var a, b []int; a, b = nil, []int{7,8}; return len(a)+len(b) }; f()", res: "2"},
		{n: "multi_assign_nil_aliases_lhs", src: "func f() bool { p := 5; var a, b *int = &p, nil; a, b = nil, a; return a == nil && b == &p }; f()", res: "true"},
		// Stdlib package interface-typed vars (e.g. crypto/rand.Reader) must keep
		// their declared interface type when inferred via :=, otherwise the runtime
		// assignment panics with "io.Reader is not assignable to rand.reader".
		{n: "stdlib_iface_var", src: `import "crypto/rand"; var r = rand.Reader; r != nil`, res: "true"},
		{n: "stdlib_iface_short_decl", src: `import "crypto/rand"; r := rand.Reader; r != nil`, res: "true"},
	})
}

func TestCompare(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "a := 1; a < 2", res: "true"},
		// nil comparisons for nilable composite types
		{n: "nil_map_decl", src: "var m map[string]string; m == nil", res: "true"},
		{n: "nil_map_explicit", src: "var m map[string]string = nil; m == nil", res: "true"},
		{n: "nil_map_nonnnil", src: "m := map[string]string{}; m == nil", res: "false"},
		{n: "nil_slice_decl", src: "var s []int; s == nil", res: "true"},
		{n: "nil_slice_explicit", src: "var s []int = nil; s == nil", res: "true"},
		{n: "nil_slice_nonnil", src: "s := []int{}; s == nil", res: "false"},
		{n: "nil_ptr_decl", src: "var p *int; p == nil", res: "true"},
		{n: "nil_ptr_nonnil", src: "a := 1; p := &a; p == nil", res: "false"},
		{n: "nil_lhs", src: "var m map[string]int; nil == m", res: "true"},
		{n: "nil_neq_map", src: "var m map[string]string; m != nil", res: "false"},
		{n: "nil_neq_slice", src: "s := []int{1}; s != nil", res: "true"},
		{n: "nil_iface_conv", src: "err := error(nil); err == nil", res: "true"},
		// A native eface holding a TYPED nil (here via the reflect bridge, the
		// mapstructure decodePtr shape) is a non-nil interface: != nil.
		{n: "nil_in_native_eface", src: `import "reflect"; type S struct{ V []string }; func f() bool { x := reflect.ValueOf(&S{}).Elem().Field(0).Interface(); return x == nil }; f()`, res: "false"},
		{n: "nil_in_native_eface_lhs", src: `import "reflect"; type S struct{ V map[string]int }; func f() bool { x := reflect.ValueOf(&S{}).Elem().Field(0).Interface(); return nil == x }; f()`, res: "false"},
		{n: "nil_native_eface_isnil", src: `import "reflect"; type S struct{ V []string }; func f() bool { x := reflect.ValueOf(&S{}).Elem().Field(0).Interface(); return reflect.ValueOf(x).IsNil() }; f()`, res: "true"},
		{n: "reflect_setitervalue_iface", src: `import "reflect"; type N interface{ ID() int64 }; type node int64; func (n node) ID() int64 { return int64(n) }; func f() int64 { m := map[int64]N{1: node(10), 2: node(20)}; var cur N; v := reflect.ValueOf(&cur).Elem(); it := reflect.ValueOf(m).MapRange(); var s int64; for it.Next() { v.SetIterValue(it); s += cur.ID() }; return s }; f()`, res: "30"},
	})
}

func TestLogical(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "true && false", res: "false"},
		{n: "#01", src: "true && true", res: "true"},
		{n: "#02", src: "true && true && false", res: "false"},
		{n: "#03", src: "false || true && true", res: "true"},
		{n: "#04", src: "2 < 3 && 1 > 2 || 3 == 3", res: "true"},
		{n: "#05", src: "2 > 3 && 1 > 2 || 3 == 3", res: "true"},
		{n: "#06", src: "2 > 3 || 2 == 1+1 && 3>0", res: "true"},
		{n: "#07", src: "2 > 3 || 2 == 1+1 && 3>4 || 1<2", res: "true"},
		{n: "#08", src: "a := 1+1 < 3 && 4 == 2+2; a", res: "true"},
		{n: "#09", src: "a := 1+1 < 3 || 3 == 2+2; a", res: "true"},
		{n: "#10", src: "a := 1+1 < 3 && 4 == 2+2; a", res: "true"},
		{n: "#11", src: "a := 1+1 < 3 || 3 == 2+2; a", res: "true"},
		{n: "#12", src: "func f1() bool {return true}; func f2() bool {return false}; a := f1() && f2(); a", res: "false"},

		// `&&` short-circuit merge label sits at the position the if-statement's
		// JumpFalse would occupy; fuseCmpJump must not swallow that JumpFalse,
		// or the merge target points at the if-body instead of the false-branch.
		// Reproduces the infinite loop hit by golang.org/x/text/internal/language
		// init's parseExtension (`for scan.scan(); end < scan.end && len(scan.token) > 2; scan.scan()`).
		{n: "fused_and_cmp_falsy", src: `a, b := 2, 2; c := 0; r := "x"; if a < b && c > 0 { r = "BUG" }; r`, res: "x"},
		{n: "fused_and_cmp_truthy", src: `a, b := 1, 2; c := 5; r := "x"; if a < b && c > 0 { r = "yes" }; r`, res: "yes"},
		{n: "fused_or_cmp_truthy", src: `a, b := 2, 2; c := 5; r := "x"; if a < b || c > 0 { r = "yes" }; r`, res: "yes"},
	})
}

func TestFunc(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "func f() int {return 2}; a := f(); a", res: "2"},
		{n: "#01", src: "func f() int {return 2}; f()", res: "2"},
		{n: "#02", src: "func f(a int) int {return a+2}; f(3)", res: "5"},
		{n: "#03", src: "func f(a int) int {if a < 4 {a = 5}; return a}; f(3)", res: "5"},
		{n: "#04", src: "func f(a int) int {return a+2}; 7 - f(3)", res: "2"},
		{n: "#05", src: "func f(a int) int {return a+2}; f(5) - f(3)", res: "2"},
		{n: "#06", src: "func f(a int) int {return a+2}; f(3) - 2", res: "3"},
		{n: "#07", src: "func f(a, b, c int) int {return a+b-c} ; f(7, 1, 3)", res: "5"},
		{n: "#08", src: "var a int; func f() {a = a+2}; f(); a", res: "2"},
		{n: "#09", src: "var f = func(a int) int {return a+3}; f(2)", res: "5"},
		{n: "#10", src: "var a int; func f(a int) {a = a+2}; f(0); a", res: "0"},
		{n: "#11", src: "func f(a int) {a = a+2}; a := 1; f(0); a", res: "1"},
		// local variables
		{n: "#12", src: "func f(a int) int { b := a + 1; return b }; f(3)", res: "4"},
		{n: "#13", src: "func f(a int) int { var b int = a + 1; return b }; f(3)", res: "4"},
		{n: "#14", src: "func f() int { a := 1; b := 2; c := 3; return a+b+c }; f()", res: "6"},
		{n: "#15", src: "func f(a int) int { b := 0; b = a + 1; return b }; f(4)", res: "5"},
		// input parameters are pass-by-value
		{n: "#16", src: "func inc(a int) { a = 100 }; x := 5; inc(x); x", res: "5"},
		// recursion (requires correct local frame isolation per call)
		{n: "#17", src: "func fib(n int) int { if n < 2 { return n }; return fib(n-1) + fib(n-2) }; fib(6)", res: "8"},
		{n: "#18", src: "var a int; func f() { a:=2 }; f(); a", res: "0"},
		// var declaration without explicit type inside a function (undefinedType path)
		{n: "#19", src: "func f() int {var x = 42; return x}; f()", res: "42"},
		{n: "#20", src: "func f() int {var a, b = 2, 3; return 10*a+b}; f()", res: "23"},
		// nil func return preserves type for %T
		{n: "#21", src: `import "fmt"; func f() func() { return nil }; g := f(); fmt.Sprintf("%T", g)`, res: "func()"},
		// func with return type whose body is only a panic
		{n: "#22", src: `func f() string { panic("boom") }; f()`, err: "panic: boom"},
		// %T on non-nil func values shows correct function type
		{n: "#23", src: `import "fmt"; fn := func(a int) string { return "" }; fmt.Sprintf("%T", fn)`, res: "func(int) string"},
		{n: "#24", src: `import "fmt"; x := 1; fn := func() int { return x }; fmt.Sprintf("%T", fn)`, res: "func() int"},
		{n: "#25", src: `import "fmt"; fn := func() {}; fmt.Sprintf("%T", fn)`, res: "func()"},
		{n: "#26", src: "func f(\n\tx int,\n\t// comment\n\ty string,\n) string { return y }; f(1, \"a\")", res: "a"},
		// A bare `nil` literal passed to a concrete nilable parameter (slice/map/
		// ptr/chan/func) is coerced at the call site to a typed-nil of the param
		// type (emitNilCoerce), so range/len/index inside the callee see e.g. a nil
		// []int rather than an untyped zero reflect.Value (which used to panic with
		// "reflect: call of reflect.Value.Type on zero Value").
		{n: "nil_literal_slice_arg_range", src: `func f(xs []int) int { n := 0; for range xs { n++ }; return n }; f(nil)`, res: "0"},
		{n: "nil_literal_map_arg_len", src: `func f(m map[string]int) int { return len(m) }; f(nil)`, res: "0"},
		{n: "nil_literal_variadic_spread", src: `func f(xs ...int) int { return len(xs) }; f(nil...)`, res: "0"},
		// A bare `return nil` becomes a typed nil of the result type, so the
		// value survives a later Iface box: passing f() straight to a native
		// variadic (fmt.Printf) used to panic with
		// "reflect: call of reflect.Value.Set on zero Value" (fastjson).
		{n: "return_nil_slice_to_printf", src: `import "fmt"; func f() []byte { return nil }; fmt.Sprintf("%q", f())`, res: `""`},
		{n: "return_nil_map_to_printf", src: `import "fmt"; func f() map[string]int { return nil }; fmt.Sprintf("%v", f())`, res: "map[]"},
		// An untyped const return adopts the declared return type; `return 1`
		// in a float64 func was bit-reinterpreted (read back as 5e-324).
		{n: "return_const_float", src: `func f() float64 { return 1 }; f() + 0.5`, res: "1.5"},
		{n: "return_const_float_generic", src: `func one[T float64 | int]() T { return 1 }; one[float64]() + 0.5`, res: "1.5"},
	})
}

func TestFusedOps(t *testing.T) {
	run(t, []etest{
		{n: "sub_imm", src: "func f(a int) int { return a - 3 }; f(10)", res: "7"},
		{n: "sub_imm_neg", src: "func f(a int) int { return a - 1 }; f(0)", res: "-1"},
		{n: "add_imm", src: "func f(a int) int { return a + 5 }; f(3)", res: "8"},
		{n: "mul_imm", src: "func f(a int) int { return a * 4 }; f(7)", res: "28"},
		{n: "lower_jf", src: "func f(a int) int { if a < 5 { return 1 }; return 0 }; f(3)", res: "1"},
		{n: "lower_jf_false", src: "func f(a int) int { if a < 5 { return 1 }; return 0 }; f(5)", res: "0"},
		{n: "lower_jf_edge", src: "func f(a int) int { if a < 5 { return 1 }; return 0 }; f(4)", res: "1"},
		{n: "greater_jf", src: "func f(a int) int { if a > 5 { return 1 }; return 0 }; f(6)", res: "1"},
		{n: "greater_jf_false", src: "func f(a int) int { if a > 5 { return 1 }; return 0 }; f(5)", res: "0"},
		{n: "greater_jf_edge", src: "func f(a int) int { if a > 5 { return 1 }; return 0 }; f(4)", res: "0"},
		{n: "ret_local", src: "func f(a int) int { return a }; f(42)", res: "42"},
		{n: "ret_local_expr", src: "func f(a int) int { b := a * 2; return b }; f(5)", res: "10"},
		{n: "get2_add", src: "func f(a, b int) int { return a + b }; f(3, 4)", res: "7"},
		{n: "get2_sub", src: "func f(a, b int) int { return a - b }; f(10, 3)", res: "7"},
		{n: "fib", src: "func fib(n int) int { if n < 2 { return n }; return fib(n-1) + fib(n-2) }; fib(10)", res: "55"},
		{n: "greater_zero", src: "func f(a int) int { if a > 0 { return 1 }; return 0 }; f(0)", res: "0"},
		{n: "greater_neg", src: "func f(a int) int { if a > -1 { return 1 }; return 0 }; f(-1)", res: "0"},
		{n: "greater_neg2", src: "func f(a int) int { if a > -1 { return 1 }; return 0 }; f(0)", res: "1"},
		{n: "loop", src: "func f(n int) int { s := 0; for i := 0; i < n; i++ { s = s + i }; return s }; f(5)", res: "10"},
	})
}

func TestOutOfOrder(t *testing.T) {
	run(t, []etest{
		// function declared after use
		{n: "#00", src: "func f() int { return g() }; func g() int { return 2 }; f()", res: "2"},
		// mutual recursion: even and odd call each other, both declared before use
		{n: "#01", src: "func even(n int) bool { if n == 0 { return true }; return odd(n-1) }; func odd(n int) bool { if n == 0 { return false }; return even(n-1) }; even(4)", res: "true"},
		// f calls two functions declared after it
		{n: "#02", src: "func f() int { return g() + h() }; func g() int { return 3 }; func h() int { return 4 }; f()", res: "7"},
		// three-level chain: a depends on b, b depends on c
		{n: "#03", src: "func a() int { return b() }; func b() int { return c() }; func c() int { return 7 }; a()", res: "7"},
		{n: "#04", src: `type T1 T; func foo() T1 {return T1(T{"foo"})}; type T struct {Name string}; foo().Name`, res: "foo"},

		// Deref of a global pointer var declared after the function using it.
		// Exercises the s.Type==nil guard in lang.Deref.
		{n: "deref_fwd", src: `
func f() int { return *p }
var n int = 42
var p = &n
f()`, res: "42"},

		// Method call on a global var declared after the function using it.
		// Exercises checkTopN(1) in lang.Period and the s.Type==nil guards.
		{n: "method_on_fwd_var", src: `
func bar() bool { return obj.Foo() }
type T struct{}
func (t *T) Foo() bool { return t != nil }
var obj = &T{}
bar()`, res: "true"},

		// Type declared after the const that uses it in an array size.
		{n: "const_before_type", src: `
const size = 3
type Vec struct { data [size]int }
len(Vec{}.data)`, res: "3"},

		// A const using a type-conversion of a type declared later: the forward
		// ref must defer (ErrUndefined), not register a Cval-less stub that
		// poisons consts referencing it (gonum graph.Empty/nothing/empty).
		{n: "const_typed_conv_fwd_type", src: `
const Empty = nothing
const nothing = empty(0)
type empty int
int(Empty)`, res: "0"},

		// var with initializer declared after the func that uses it.
		{n: "var_init_after_func", src: `
func get() int { return x }
var x = 10
get()`, res: "10"},

		// Forward reference between vars in a var block.
		{n: "var_block_fwd_ref", src: `
var (
	a = b
	b = "hello"
)
a`, res: "hello"},

		// Dependency chain with function calls in a var block.
		{n: "var_block_dep_chain", src: `
var (
	a = concat("hello", b)
	b = concat(" ", c, "!")
	c = d
	d = "world"
)
func concat(a ...string) string {
	var s string
	for _, ss := range a { s += ss }
	return s
}
a`, res: "hello world!"},

		// Dependency chain across separate var declarations.
		{n: "separate_var_dep_chain", src: `
var a = concat("hello", b)
var b = concat(" ", c, "!")
var c = d
var d = "world"
func concat(a ...string) string {
	var s string
	for _, ss := range a { s += ss }
	return s
}
a`, res: "hello world!"},
	})
}

func TestVariadic(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "func sum(a ...int) int { s := 0; for _, v := range a { s = s + v }; return s }; sum(1, 2, 3)", res: "6"},
		{n: "#01", src: "func sum(a ...int) int { s := 0; for _, v := range a { s = s + v }; return s }; sum()", res: "0"},
		{n: "#02", src: "func sum(a ...int) int { s := 0; for _, v := range a { s = s + v }; return s }; sum(42)", res: "42"},
		{n: "#03", src: "func add(x int, rest ...int) int { s := x; for _, v := range rest { s = s + v }; return s }; add(10, 1, 2, 3)", res: "16"},
		{n: "#04", src: "func add(x int, rest ...int) int { s := x; for _, v := range rest { s = s + v }; return s }; add(10)", res: "10"},
		{n: "#05", src: "var r int; func f(a ...int) { for _, v := range a { r = r + v } }; f(1, 2, 3); r", res: "6"},
		{n: "#06", src: `func sum(a ...int) int { s := 0; for _, v := range a { s += v }; return s }; x := []int{1, 2, 3}; sum(x...)`, res: "6"},
		{n: "#07", src: `func add(x int, rest ...int) int { s := x; for _, v := range rest { s += v }; return s }; r := []int{1, 2}; add(10, r...)`, res: "13"},
		{n: "#08", src: `import "fmt"; var a, b string; dest := []interface{}{&a, &b}; fmt.Sscanf("hello world", "%s %s", dest...); a + " " + b`, res: "hello world"},
		{n: "#09", src: `import "fmt"; f := fmt.Sprintf; f("hello %s", "world")`, res: "hello world"},
		// Multi-line param list with a trailing comma: the ...T param is not
		// the last Split element, but must still become []T (quicktest's
		// CodecEquals signature shape).
		{n: "trailing_comma", src: "func f(\n\ta int,\n\topts ...string,\n) int {\n\treturn a + len(opts)\n}\nf(1, \"x\", \"y\")", res: "3"},
	})
}

func TestGenericFunc(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max[int](3, 5)`, res: "5"},
		{n: "#01", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max[string]("alpha", "beta")`, res: "beta"},
		{n: "#02", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max[float64](1.5, 2.5)`, res: "2.5"},
		{n: "#03", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max[int](3, 5) + Max[int](10, 7)`, res: "15"},
		{n: "#04", src: `import "fmt"; func Pair[K any, V any](k K, v V) string { return fmt.Sprintf("%v:%v", k, v) }; Pair[int, string](42, "hello")`, res: "42:hello"},
		{n: "#05", src: `func Id[T any](x T) T { return x }; f := Id[int]; f(42)`, res: "42"},
	})
}

func TestGenericType(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: `type Box[T any] struct { Value T }; b := Box[int]{Value: 42}; b.Value`, res: "42"},
		{n: "#01", src: `type Box[T any] struct { Value T }; b := Box[string]{Value: "hello"}; b.Value`, res: "hello"},
		{n: "#02", src: `type Box[T any] struct { Value T }; a := Box[int]{Value: 1}; b := Box[string]{Value: "x"}; a.Value + len(b.Value)`, res: "2"},
		{n: "#03", src: `import "fmt"; type Pair[K any, V any] struct { Key K; Val V }; p := Pair[string, int]{Key: "a", Val: 1}; fmt.Sprintf("%s:%d", p.Key, p.Val)`, res: "a:1"},
		{n: "#04", src: `type Box[T any] struct { Value T }; var b Box[int]; b.Value`, res: "0"},
		{n: "#05", src: `type Box[T any] struct { Value T }; b := &Box[int]{Value: 42}; b.Value`, res: "42"},
		{n: "#06", src: `type Box[T any] struct { Value T }; a := Box[int]{Value: 1}; b := Box[int]{Value: 2}; a.Value + b.Value`, res: "3"},
		{n: "generic_method", src: `type Box[T any] struct { V T }; func (b Box[T]) Get() T { return b.V }; Box[int]{V: 42}.Get()`, res: "42"},
		{n: "generic_method_var", src: `type Box[T any] struct { V T }; func (b Box[T]) Get() T { return b.V }; b := Box[int]{V: 7}; b.Get()`, res: "7"},
		{n: "generic_method_ptr", src: `type Box[T any] struct { V T }; func (b *Box[T]) Set(v T) { b.V = v }; b := &Box[int]{V: 0}; b.Set(99); b.V`, res: "99"},
		{n: "generic_method_multi", src: `type Box[T any] struct { V T }; func (b Box[T]) Get() T { return b.V }; func (b *Box[T]) Set(v T) { b.V = v }; b := &Box[int]{V: 1}; b.Set(2); b.Get()`, res: "2"},
		{n: "generic_method_multi_tparam", src: `type Pair[K comparable, V any] struct { K K; V V }; func (p Pair[K, V]) Key() K { return p.K }; Pair[string, int]{K: "x", V: 1}.Key()`, res: "x"},
		{n: "constraint_check", src: `func Less[T comparable](a, b T) bool { return a < b }; Less[func()](nil, nil)`, err: "does not satisfy constraint"},
		{n: "comparable_ok", src: `func Id[T comparable](x T) T { return x }; Id[int](42)`, res: "42"},
		{n: "comparable_slice", src: `func Id[T comparable](x T) T { return x }; Id[[]int](nil)`, err: "does not satisfy constraint"},
		{n: "constraint_iface", src: `import "fmt"; func Str[T fmt.Stringer](x T) string { return x.String() }; Str[int](42)`, err: "does not satisfy constraint"},
		{n: "union_constraint", src: `func Add[T int | float64](a, b T) T { return a + b }; Add[int](1, 2)`, res: "3"},
		{n: "union_reject", src: `func Add[T int | float64](a, b T) T { return a + b }; Add[string]("a", "b")`, err: "does not satisfy constraint"},
		{n: "approx_constraint", src: `type MyInt int; func Id[T ~int](x T) T { return x }; Id[MyInt](MyInt(1))`, res: "1"},
		{n: "approx_reject", src: `func Id[T ~int](x T) T { return x }; Id[string]("a")`, err: "does not satisfy constraint"},
		{n: "generic_interface", src: `type Stringer[T any] interface { String(T) string }; ""`, res: ""},
		{n: "nested_generic", src: `type Box[T any] struct { V T }; type Wrap[U any] struct { Inner Box[U] }; w := Wrap[int]{Inner: Box[int]{V: 1}}; w.Inner.V`, res: "1"},
		{n: "generic_type_alias", src: `type Box[T any] struct { V T }; type IntBox = Box[int]; b := IntBox{V: 1}; b.V`, res: "1"},
		{n: "union_iface_ok", src: `type Ord interface { ~int | ~string }; func F[T Ord](x T) T { return x }; F[int](42)`, res: "42"},
		{n: "union_iface_ok_str", src: `type Ord interface { ~int | ~string }; func F[T Ord](x T) T { return x }; F[string]("hi")`, res: "hi"},
		{n: "union_iface_reject", src: `type Ord interface { ~int | ~string }; func F[T Ord](x T) T { return x }; F[float64](1.0)`, err: "does not satisfy constraint"},
		// A named constraint interface embedded as a union term contributes
		// its own type elements (cast's Basic = string | bool | Number).
		{n: "nested_union_ok", src: `type Number interface { int | float64 }; type Basic interface { string | Number }; func F[T Basic](x T) T { return x }; F(42)`, res: "42"},
		{n: "nested_union_reject", src: `type Number interface { int | float64 }; type Basic interface { string | Number }; func F[T Basic](x T) T { return x }; F(true)`, err: "does not satisfy constraint"},
		{n: "approx_named", src: `type Ord interface { ~int }; type MyInt int; func F[T Ord](x T) T { return x }; F[MyInt](MyInt(1))`, res: "1"},
		{n: "typeparam_ref", src: `func F[T any, U T](x U) U { return x }; F[int, int](1)`, res: "1"},
		{n: "comparable_iface_elem_ok", src: `type C interface { comparable }; func F[T C](x T) T { return x }; F[int](42)`, res: "42"},
		{n: "comparable_iface_elem_reject", src: `type C interface { comparable }; func F[T C](x T) T { return x }; F[func()](nil)`, err: "does not satisfy constraint"},
		{n: "comparable_iface_with_method", src: `import "fmt"; type C interface { comparable; error }; func F[T C](xs []T) bool { var z T; return len(xs) > 0 && xs[0] != z }; F([]error{fmt.Errorf("x")})`, res: "true"},
		{n: "iface_constraint_interp_ptr", src: `type E struct{ s string }; func (e *E) Error() string { return e.s }; func F[T error](x T) T { return x }; F[*E](&E{"x"}).Error()`, res: "x"},
		{n: "iface_constraint_interp_value", src: `type E struct{ s string }; func (e E) Error() string { return e.s }; func F[T error](x T) T { return x }; F[E](E{"y"}).Error()`, res: "y"},
		{n: "iface_constraint_interp_reject", src: `type E struct{ n int }; func F[T error](x T) T { return x }; F[*E](&E{})`, err: "does not satisfy constraint"},
		{n: "iface_constraint_interp_iface_arg", src: `type I interface { Foo() string }; func F[T I](x T) string { return x.Foo() }; type T struct{}; func (T) Foo() string { return "ok" }; var v I = T{}; F[I](v)`, res: "ok"},
		{n: "instantiation_cycle", src: `func F[T any]() { F[[]T]() }; F[int]()`, err: "instantiation cycle"},
	})
}

func TestGenericImplicit(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max(3, 5)`, res: "5"},
		{n: "#01", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max("alpha", "beta")`, res: "beta"},
		{n: "#02", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max(1.5, 2.5)`, res: "2.5"},
		{n: "#03", src: `func Id[T any](x T) T { return x }; Id(42)`, res: "42"},
		{n: "#04", src: `import "fmt"; func Pair[K any, V any](k K, v V) string { return fmt.Sprintf("%v:%v", k, v) }; Pair(42, "hello")`, res: "42:hello"},
		{n: "#05", src: `func Id[T any](x T) T { return x }; f := Id(42); f`, res: "42"},
		{n: "#06", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; func f(x int) int { return x * 2 }; func g(x int) int { return x + 10 }; Max(f(3), g(4))`, res: "14"},
		{n: "conversion", src: `func Id[T any](x T) T { return x }; Id(int(1.0))`, res: "1"},
		{n: "conversions", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max(int(3.0), int(5.0))`, res: "5"},
		{n: "assertion", src: `func Id[T any](x T) T { return x }; var i interface{} = 42; Id(i.(int))`, res: "42"},
		{n: "assertions", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; var a, b interface{} = 3, 5; Max(a.(int), b.(int))`, res: "5"},
		{n: "mixed_expr", src: `func Max[T any](a, b T) T { if a > b { return a }; return b }; Max(2+3, 1+1)`, res: "5"},
		{n: "index_expr", src: `func Id[T any](x T) T { return x }; a := []int{10, 20, 30}; Id(a[2])`, res: "30"},
		{n: "slice", src: `import "fmt"; func Fmt[T any](x T) string { return fmt.Sprintf("%v", x) }; Fmt([]int{1, 2, 3})`, res: "[1 2 3]"},
		{n: "map", src: `import "fmt"; func Fmt[T any](x T) string { return fmt.Sprintf("%v", x) }; Fmt(map[string]int{"a": 1})`, res: "map[a:1]"},
		{n: "chan", src: `func Id[T any](x T) T { return x }; c := make(chan int, 1); c <- 5; Id(<-c)`, res: "5"},
		{n: "pointer", src: `func Id[T any](x T) T { return x }; a := 42; p := Id(&a); *p`, res: "42"},
		{n: "deref", src: `func Id[T any](x T) T { return x }; a := 42; b := &a; Id(*b)`, res: "42"},
		{n: "infer_ptr", src: `func F[T any](x *T) T { return *x }; a := 42; F(&a)`, res: "42"},
		{n: "infer_slice_of", src: `func F[T any](x []T) T { return x[0] }; F([]int{7, 8})`, res: "7"},
		{n: "infer_ptr_slice", src: `func F[T any](x *[]T) int { return len(*x) }; a := []int{1, 2}; F(&a)`, res: "2"},
		{n: "infer_map", src: `func F[K comparable, V any](m map[K]V, k K) V { return m[k] }; F(map[string]int{"a": 1}, "a")`, res: "1"},
		{n: "infer_slice_ptr", src: `func F[T any](x []*T) T { return *x[0] }; a := 9; F([]*int{&a})`, res: "9"},
		{n: "infer_variadic_spread", src: `func Or[T comparable](vals ...T) T { var z T; for _, v := range vals { if v != z { return v } }; return z }; s := []int{0, 2}; Or(s...)`, res: "2"},
		{n: "infer_pkg_qualified_call_arg", src: `import "strings"; func First[T comparable](vals ...T) T { var z T; for _, v := range vals { if v != z { return v } }; return z }; First(strings.Compare("a", "b"))`, res: "-1"},
		{n: "infer_named_struct_compound_arg", src: `func swap2[S ~[]E, E any](s S) E { s[0], s[1] = s[1], s[0]; return s[0] }; type O struct{ N int }; swap2([]O{{1}, {2}}).N`, res: "2"},
		{n: "infer_func_literal_inner_generic", src: `func sortish[S ~[]E, E any](s S, f func(a, b E) int) E { var z E; for _, v := range s { if f(v, z) != 0 { z = v } }; return z }; func id[T any](x T) T { return x }; sortish([]int{3, 1, 2}, func(a, b int) int { return id(a) - id(b) })`, res: "2"},
		{n: "infer_generic_call_as_arg", src: `func Or[T comparable](vals ...T) T { var z T; for _, v := range vals { if v != z { return v } }; return z }; func cmp3[T int](a, b T) int { if a < b { return -1 }; if a > b { return 1 }; return 0 }; Or(cmp3(1, 2), cmp3(3, 3))`, res: "-1"},
		{n: "infer_pkg_qualified_ptr_type_pos", src: `import "bytes"; func zero[T any](x T) T { var z T; return z }; var p *bytes.Buffer; zero(p) == nil`, res: "true"},
		{n: "infer_pkg_qualified_slice_type_pos", src: `import "bytes"; func first[T any](xs []T) T { var z T; if len(xs) == 0 { return z }; return xs[0] }; var p *bytes.Buffer; first([]*bytes.Buffer{p, nil}) == nil`, res: "true"},
		// Infer a type param from an interface-method-call result (gonum
		// internal/order: cmp.Compare(a.ID(), b.ID()) with a/b interface-typed).
		{n: "infer_iface_method_result", src: `import "cmp"; type N interface{ ID() int64 }; type n int64; func (x n) ID() int64 { return int64(x) }; var a, b N = n(1), n(2); cmp.Compare(a.ID(), b.ID())`, res: "-1"},
		{n: "infer_iface_method_result_via_temp", src: `import "cmp"; type N interface{ ID() int64 }; type n int64; func (x n) ID() int64 { return int64(x) }; var a N = n(5); v := a.ID(); cmp.Compare(v, v)`, res: "0"},
		// Infer a type param from a len/cap builtin call result.
		{n: "infer_len_builtin_arg", src: `import "cmp"; func f(a, b []int) int { return cmp.Compare(len(a), len(b)) }; f([]int{1, 2, 3}, []int{1})`, res: "1"},
		// Comma-ok type assertion types its value local, so a downstream interface-
		// method-result slice flows into generic inference (gonum order.ByID).
		{n: "infer_commaok_assert_iface_slice", src: `type N interface{ ID() int64 }; type nd int64; func (x nd) ID() int64 { return int64(x) }; type Slicer interface{ Slice() []N }; type nds []N; func (s nds) Slice() []N { return []N(s) }; func cnt[S ~[]E, E N](s S) int { return len(s) }; func f() int { var it interface{} = nds{nd(1), nd(2)}; s, _ := it.(Slicer); got := s.Slice(); return cnt(got) }; f()`, res: "2"},
	})
}

// TestGenericInfer captures the generics type-inference gaps targeted by the
// dedicated overhaul (docs/plans wise-skipping-hoare). Each skipped case is a
// distilled, self-contained repro of a real `mvm test slices` / `mvm test maps`
// failure; flip skip:false as each phase lands. The non-skipped load_guard case
// protects package source compilation (it would have caught the prior revert).
//
// Note: nested-generic-call inference (e.g. slices.Sorted(maps.Keys(m))) was a
// gap in earlier sessions but now works, so it has no skipped case here.
func TestGenericInfer(t *testing.T) {
	run(t, []etest{
		// Gap C: partial type-argument lists (some explicit, rest inferred).
		// Mirrors maps_test.go:26 `Equal[map[int]int, map[int]int](nil, nil)`
		// (2 of 4) and slices.Concat's `Grow[S](nil, size)` (1 of 2).
		{n: "partial_type_args_slice", src: `func Grow[S ~[]E, E any](s S, n int) int { return n + len(s) }; Grow[[]int]([]int{1, 2}, 3)`, res: "5"},
		{n: "partial_type_args_map", src: `func Eq[M1 ~map[K]V, M2 ~map[K]V, K comparable, V comparable](a M1, b M2) bool { return len(a) == len(b) }; Eq[map[int]int, map[int]int](nil, nil)`, res: "true"},

		// Gap B: a `:=` make/slice-expr local INSIDE a function body is not
		// type-recorded, so a later generic call can't infer from it. The same
		// pattern at top level works (the funcScope == "" path is skipped).
		// Mirrors slices sort_test.go:67 IsSorted(data), example_test.go:352
		// Clip(s), slices_test.go:138 EqualFunc(xs, ys, ...).
		{n: "define_make_local_in_func", src: `func srt[S ~[]E, E any](x S) bool { return len(x) > 0 }; func f(n int) bool { data := make([]int, n); return srt(data) }; f(3)`, res: "true"},
		{n: "define_sliceexpr_local_in_func", src: `func clip[S ~[]E, E any](x S) int { return len(x) }; func f() int { a := []int{0, 1, 2, 3}; s := a[:2]; return clip(s) }; f()`, res: "2"},
		// Same gap but the make-local's element type is itself a placeholder
		// type param (map[K]V inside a generic body). Mirrors maps/iter.go:60
		// `Insert(m, seq)` in Collect -- the source-load blocker for maps.
		{n: "define_make_map_in_generic", src: `func ins[M ~map[K]V, K comparable, V any](m M) int { return len(m) }; func col[K comparable, V any]() int { m := make(map[K]V); return ins(m) }; col[int, string]()`, res: "0"},

		// Gap A: slicing/indexing a value typed by a `~[]E` type parameter needs
		// the placeholder to carry its element shape. Mirrors slices.go Insert:206
		// / Replace:328 `rotateRight(s[i:], m)` (masked today by an explicit-[E]
		// mirror patch this overhaul removes).
		{n: "slice_typeparam_value_in_generic", src: `func rr[E any](s []E) E { return s[0] }; func rep[S ~[]E, E any](src S) E { r := make(S, len(src)); copy(r, src); return rr(r[1:]) }; rep[[]int, int]([]int{1, 2, 3})`, res: "2"},

		// Gap B follow-ups (in-package `mvm test slices` revealed more `:=` RHS
		// forms that left a local untyped). Multi-RHS define `a, b := x, y` went
		// through parseAssignMultiRHS which did no define-typing; and an
		// `append(...)` local was untyped because postfixType didn't know append.
		{n: "define_multi_rhs", src: `func g[S ~[]E, E any](x S) int { return len(x) }; func f() int { a, b := []int{1, 2}, []int{3}; return g(a) + g(b) }; f()`, res: "3"},
		{n: "define_append_local", src: `func g[S ~[]E, E any](x S) int { return len(x) }; func f() int { base := []int{1}; x := append(base, 2, 3); return g(x) }; f()`, res: "3"},
		// Known remaining: an append local whose slice arg is a composite literal
		// (`append([]int{1}, 2)`) stays untyped -- postfixType can't span a
		// composite operand in a call's arg list. Same class blocks
		// slices/iter_test.go:92. Out of this pass; see the multi-file/postfixType
		// follow-up.
		{n: "define_append_composite_arg", skip: true, src: `func g[S ~[]E, E any](x S) int { return len(x) }; func f() int { x := append([]int{1}, 2, 3); return g(x) }; f()`, res: "3"},

		// postfixType under-counted a method call on a non-pkg receiver (recv.M())
		// by its receiver token, misaligning the binary-op split -> wrong generic
		// arg type. From go/types api_test.go:1401.
		{n: "method_call_in_concat_generic_arg", src: `import ("slices"; "strings"); type R struct{}; func (R) Names() []string { return []string{"x"} }; func f() bool { var r R; d := "k" + ":" + strings.Join(r.Names(), " "); return slices.Contains([]string{"k:x"}, d) }; f()`, res: "true"},

		// Load guard (NOT skipped): keep slices/maps package source compilable.
		// References the rotateRight-using funcs (Insert/Replace) so a regression
		// that breaks their compilation surfaces here, not silently in `mvm test`.
		{n: "load_guard", src: `import ("slices"; "maps"); s := slices.Insert([]int{1, 5}, 1, 2, 3, 4); s = slices.Replace(s, 0, 1, 9); slices.Reverse(s); m := maps.Clone(map[int]int{1: 2}); len(s) + len(m)`, res: "6"},

		// A pkg-qualified named type whose bare name matches a type param
		// (testing.T vs param T) must not bind it; T comes from the func arg.
		// Mirrors cast's runSliceTests[T](t *testing.T, ..., to func(i any) []T).
		{n: "param_name_collides_named_type", src: `import "time"; func g[Time any](d time.Time, mk func() Time) Time { return mk() }; g(time.Time{}, func() int { return 3 })`, res: "3"},
	})
}

func TestFuncNamedReturn(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "func f(a int) (r int) { r = a + 2; return }; f(3)", res: "5"},
		{n: "#01", src: "func f(a int) (r int) { r = a; r = r + 2; return }; f(3)", res: "5"},
		{n: "#02", src: "func f(a int) (x, y int) { x = a; y = a + 1; return }; a, b := f(3); a+b", res: "7"},
		{n: "#03", src: "func f(a int) (r int) { return a + 2 }; f(3)", res: "5"},
		// tuple assignment to named returns
		{n: "tuple_named_return", src: `func f() (n int, s string) { n, s = 42, "hello"; return n, s }; a, b := f(); a`, res: "42"},
		// named return must be zeroed on each call (yaegi-issue-1488)
		{n: "named_return_reinit", src: `func inc() (out int) { out++; return }; a := inc(); b := inc(); c := inc(); a + b + c`, res: "3"},

		// Named-return slice/map need a typed zero so append() / m[k]=v don't
		// panic in reflect with "Type on zero Value" (Grow alone leaves the
		// slot as an untyped Value{}). Hit by language.ParseAcceptLanguage.
		{n: "named_return_slice_append", src: `func f() (s []int) { s = append(s, 1); s = append(s, 2); return }; f()`, res: "[1 2]"},
		{n: "named_return_map_assign", src: `func f() (m map[string]int) { m = map[string]int{}; m["a"] = 1; return }; f()["a"]`, res: "1"},

		// Go's `:=` rebinds an existing same-block LocalVar when at least one LHS
		// ident is new. Previously addLocalVar overwrote the named-return Symbol
		// with a Type=nil entry, crashing the compiler in typeSym.
		{n: "named_return_redef", src: `func g() (int, bool) { return 9, true }; func f() (x int, skip bool) { x, ok := g(); _ = ok; return x, false }; a, _ := f(); a`, res: "9"},
		{n: "short_decl_param_rebind", src: `func g() (int, bool) { return 9, true }; func f(x int) int { x, ok := g(); _ = ok; return x }; f(1)`, res: "9"},

		// Bit-ops on a never-written named-return scalar used to drop the result:
		// the op left .ref invalid and assignSlot's reflect.Zero fallback overwrote src.num with 0.
		{n: "named_return_or_uninit", src: `type C uint32; func f() (c C) { c |= C(313) << 20; return c }; f()`, res: "328204288"},
		{n: "named_return_and_uninit", src: `func f() (c uint32) { c &= 0xff; return c }; f()`, res: "0"},
		{n: "named_return_xor_uninit", src: `func f() (c uint32) { c ^= 0xff; return c }; f()`, res: "255"},
		{n: "named_return_shl_uninit", src: `func f() (c uint32) { c <<= 4; return c }; f()`, res: "0"},
	})
}

func TestIf(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "a := 0; if a == 0 { a = 2 } else { a = 1 }; a", res: "2"},
		{n: "#01", src: "a := 0; if a == 1 { a = 2 } else { a = 1 }; a", res: "1"},
		{n: "#02", src: "a := 0; if a == 1 { a = 2 } else if a == 0 { a = 3 } else { a = 1 }; a", res: "3"},
		{n: "#03", src: "a := 0; if a == 1 { a = 2 } else if a == 2 { a = 3 } else { a = 1 }; a", res: "1"},
		{n: "#04", src: "a := 1; if a > 0 && a < 2 { a = 3 }; a", res: "3"},
		{n: "#05", src: "a := 1; if a < 0 || a < 2 { a = 3 }; a", res: "3"},
		{n: "#06", src: `func f() (int, error) { return 3, nil }; r := 0; if a, err := f(); err != nil { r = 1 } else { r = a }; r`, res: "3"},
		{n: "#07", src: `func f() (int, error) { return 0, nil }; func g() ([]int, error) { return []int{1,2}, nil }; r := 0; if a, err := f(); err != nil { r = a } else if _, err2 := g(); err2 != nil { r = 1 } else { r = 3 }; r`, res: "3"},
		// composite literal in the if-init clause: its `{}` must not be mistaken
		// for the if body when scanning the statement.
		{n: "#08", src: `r := 0; if s := []int{1, 2, 3}; len(s) == 3 { r = s[2] }; r`, res: "3"},
	})
}

func TestFor(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "a := 0; for i := 0; i < 3; i = i+1 {a = a+i}; a", res: "3"},
		{n: "#01", src: "func f() int {a := 0; for i := 0; i < 3; i = i+1 {a = a+i}; return a}; f()", res: "3"},
		{n: "#02", src: "a := 0; for {a = a+1; if a == 3 {break}}; a", res: "3"},
		{n: "#03", src: "func f() int {a := 0; for {a = a+1; if a == 3 {break}}; return a}; f()", res: "3"},
		{n: "#04", src: "func f() int {a := 0; for {a = a+1; if a < 3 {continue}; break}; return a}; f()", res: "3"},
		{n: "#05", src: "a := []int{1,2,3,4}; b := 0; for i := range a {b = b+i}; b", res: "6"},
		{n: "#06", src: "func f() int {a := []int{1,2,3,4}; b := 0; for i := range a {b = b+i}; return b}; f()", res: "6"},
		{n: "#07", src: "a := []int{1,2,3,4}; b := 0; for i, e := range a {b = b+i+e}; b", res: "16"},
		{n: "#08", src: "a := [4]int{1,2,3,4}; b := 0; for i, e := range a {b = b+i+e}; b", res: "16"},
		{n: "#09", src: "a:= 0; for i := 0; i < 10; i++ { if i < 5 {a++; continue}}; a", res: "5"},
		{n: "#10", src: `a := 0; Outer: for i := 0; i < 3; i++ { for j := 0; j < 3; j++ { if j == 1 { continue Outer }; a++ } }; a`, res: "3"},
		{n: "#11", src: `a := 0; Outer: for i := 0; i < 3; i++ { switch i { case 1: continue Outer }; a++ }; a`, res: "2"},
		{n: "#12", src: `s := "abc"; b := 0; for i := range s { b += i }; b`, res: "3"},
		{n: "#13", src: `s := "abc"; n := 0; for _, r := range s { n += int(r) }; n`, res: "294"},
		{n: "#14", src: `const s = "ab"; b := 0; for i, r := range s { b += i + int(r) }; b`, res: "196"},
		{n: "#15", src: `s := "a1b"; n := 0; for i, r := range s { if r == '1' { n = i } }; n`, res: "1"},
		{n: "#16", src: `b := 0; for i := range 4 { b += i }; b`, res: "6"},
		{n: "#17", src: `func f() int { b := 0; for i := range 4 { b += i }; return b }; f()`, res: "6"},
		{n: "#18", src: `m := map[string]int{"a": 1}; v, ok := m["a"]; ok && v == 1`, res: "true"},
		{n: "#19", src: `m := map[string]int{"a": 1}; v, ok := m["b"]; !ok && v == 0`, res: "true"},
		{n: "#20", src: `
func f() string {
	s := make([]map[string]string, 0)
	m := make(map[string]string)
	m["m1"] = "m1"
	s = append(s, m)
	tmpStr := "start"
	for _, v := range s {
		tmpStr, _ := v["m1"]
		_ = tmpStr
	}
	return tmpStr
}
f()`, res: "start"},
		{n: "#21", src: `n := 0; for range []int{1,2,3} { n++ }; n`, res: "3"},
		{n: "#22", src: `for range []struct{}{} {}; true`, res: "true"},
		{n: "#23", src: `func f() bool { for range []struct{}{} {}; return true }; f()`, res: "true"},
		{n: "#24", src: `n := 0; for range 4 { n++ }; n`, res: "4"},
		{n: "#25", src: `n := 0; for range map[string]int{"a": 1, "b": 2} { n++ }; n`, res: "2"},
		{n: "#26", src: `m := map[string]int{"a": 1, "b": 2}; n := 0; for k := range m { n += len(k) }; n`, res: "2"},
		{n: "#27", src: `m := map[string]int{"a": 1, "b": 2}; n := 0; for _, v := range m { n += v }; n`, res: "3"},
		{n: "#28", src: `n := 0; for range []int{0,1,2} { n++ }; n`, res: "3"},
		{n: "#29", src: `n := 0; for range []bool{true,false,true} { n++ }; n`, res: "3"},
		{n: "#30", src: `ch := make(chan int, 3); ch <- 1; ch <- 2; ch <- 3; close(ch); n := 0; for v := range ch { n += v }; n`, res: "6"},
		{n: "#31", src: `ch := make(chan string, 2); ch <- "a"; ch <- "bb"; close(ch); n := 0; for v := range ch { n += len(v) }; n`, res: "3"},
		{n: "#32", src: `ch := make(chan int, 3); ch <- 1; ch <- 2; ch <- 3; close(ch); n := 0; for range ch { n++ }; n`, res: "3"},
		{n: "#33", src: `func f() int { ch := make(chan int, 3); ch <- 10; ch <- 20; ch <- 30; close(ch); s := 0; for v := range ch { s += v }; return s }; f()`, res: "60"},
		{n: "#34", src: `import "fmt"; func f() string { ch := make(chan string, 1); ch <- "ok"; close(ch); s := ""; for v := range ch { fmt.Println(v); s = v }; return s }; f()`, res: "ok"},
		{n: "#35", src: `a := []int{1, 2, 3}; for i, v := range a { a[i] = v * 2 }; a[0] + a[1] + a[2]`, res: "12"},
		{n: "#36", src: `m := map[string]int{"a": 1, "b": 2}; for k := range m { m[k] = 0 }; m["a"] + m["b"]`, res: "0"},
		{n: "#37", src: `func f() string { return "a" }; m := map[string]int{f(): 1}; m["a"]`, res: "1"},
		// composite literal in the for-init clause: its `{}` must not be mistaken
		// for the loop body when scanning the statement.
		{n: "#38", src: `n := 0; for s := []int{}; len(s) < 3; { s = append(s, 1); n++ }; n`, res: "3"},
		{n: "#39", src: `func f() int { n := 0; for s := []int{}; len(s) < 3; { s = append(s, 1); n++ }; return n }; f()`, res: "3"},
		{n: "#40", src: `n := 0; for m := map[string]int{}; len(m) < 2; { m[string(rune('a'+len(m)))] = 1; n++ }; n`, res: "2"},
		// continue inside a 3-clause for with an empty post clause: jumps back to
		// the condition, not to a (never-emitted) post label.
		{n: "#41", src: `n := 0; for i := 0; i < 5; { if i == 2 { i++; continue }; n++; i++ }; n`, res: "4"},
		{n: "#42", src: `func f() int { n := 0; for i := 0; i < 5; { if i == 2 { i++; continue }; n++; i++ }; return n }; f()`, res: "4"},
		// Assign-form range: loop vars are existing assignable targets, not
		// defines (desugared to a := range over temps + per-iteration assigns).
		{n: "range_assign_existing_var", src: `n := 0; for _, n = range []int{40, 41} {}; n`, res: "41"},
		{n: "range_assign_key_only", src: `i := -1; for i = range []string{"a", "b", "c"} {}; i`, res: "2"},
		{n: "range_assign_captured", src: `func f() int { n := 0; g := func() { for _, n = range []int{40, 41} {} }; g(); return n }; f()`, res: "41"},
		{n: "range_assign_index_deref", src: `func f() int { a := make([]int, 1); v := 0; pv := &v; for a[0], *pv = range []int{10, 20} {}; return a[0]*100 + v }; f()`, res: "120"},
		{n: "range_assign_map", src: `k := ""; v := 0; for k, v = range map[string]int{"x": 7} {}; k + string(rune('0'+v))`, res: "x7"},
		{n: "range_assign_nested", src: `func f() int { t, a, b := 0, 0, 0; for _, a = range []int{1, 2} { for _, b = range []int{10, 20} { t += a + b } }; return t*100 + a*10 + b }; f()`, res: "6640"},
		// A for loop in a closure in the range subject must not consume the stash.
		{n: "range_assign_closure_subject", src: `func mk(f func()) []int { f(); return []int{40, 41} }; func g() int { n := 0; for _, n = range mk(func() { for range []int{1} {} }) {}; return n }; g()`, res: "41"},
		// Go spec violations: clean compile error, not VM panic.
		{n: "range_int_two_vars", src: `for k, v := range 5 { _ = k; _ = v }`, err: "range over integer permits only one iteration variable"},
		{n: "range_chan_two_vars", src: `ch := make(chan int); close(ch); for k, v := range ch { _ = k; _ = v }`, err: "range over channel permits only one iteration variable"},
		{n: "range_invalid_subject", src: `var x struct{}; for k := range x { _ = k }`, err: "cannot range over"},
		// A zero constant divisor must not be folded (go/constant would panic the
		// compiler); it is left to the runtime, which reports a clean error.
		{n: "const_div_zero", src: `10 / 0`, err: "divide by zero"},
		{n: "const_rem_zero", src: `10 % 0`, err: "divide by zero"},
		// Constant overflow of a typed value is a compile error, as in gc.
		{n: "const_conv_overflow", src: `int8(200)`, err: "constant 200 overflows int8"},
		{n: "const_add_overflow", src: `int8(100) + int8(100)`, err: "overflows int8"},
		{n: "const_uint_overflow", src: `uint8(256)`, err: "overflows uint8"},
		{n: "const_neg_into_unsigned", src: `uint8(-1)`, err: "overflows uint8"},
		{n: "const_shift_overflow", src: `int64(1) << 70`, err: "overflows int64"},
		{n: "const_float32_overflow", src: `float32(1e40)`, err: "overflows float32"},
		{n: "const_complex64_overflow", src: `complex64(1e40)`, err: "overflows complex64"},
	})
}

func TestGoto(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: `
func f(a int) int {
	a = a+1
	goto end
	a = a+1
end:
	return a
}
f(3)`, res: "4"},
	})
}

func TestSwitch(t *testing.T) {
	src0 := `func f(a int) int {
	switch a {
	default:  a = 0
	case 1,2: a = a+1
	case 3:   a = a+2; break; a = 3
	case 4:   a = 10
	}
	return a
}
`
	src1 := `func f(a int) int {
	switch {
	case a < 3: return 2
	case a < 5: return 5
	default:  a = 0
	}
	return a
}
`
	src2 := `func f(a int) int {
	switch a {
	case 1: a = 10; fallthrough
	case 2: a++
	case 3: a = 30
	}
	return a
}
`
	src3 := `func f(a int) int {
	switch a {
	case 1,2: fallthrough
	case 3:   a = 99
	case 4:   a = 0
	}
	return a
}
`
	run(t, []etest{
		{n: "#00", src: src0 + "f(1)", res: "2"},
		{n: "#01", src: src0 + "f(2)", res: "3"},
		{n: "#02", src: src0 + "f(3)", res: "5"},
		{n: "#03", src: src0 + "f(4)", res: "10"},
		{n: "#04", src: src0 + "f(5)", res: "0"},

		{n: "#05", src: src1 + "f(1)", res: "2"},
		{n: "#06", src: src1 + "f(4)", res: "5"},
		{n: "#07", src: src1 + "f(6)", res: "0"},

		{n: "#08", src: src2 + "f(1)", res: "11"},
		{n: "#09", src: src2 + "f(2)", res: "3"},
		{n: "#10", src: src2 + "f(3)", res: "30"},

		{n: "#11", src: src3 + "f(1)", res: "99"},
		{n: "#12", src: src3 + "f(2)", res: "99"},
		{n: "#13", src: src3 + "f(3)", res: "99"},
		{n: "#14", src: src3 + "f(4)", res: "0"},

		{n: "empty", src: `switch {}; 1`, res: "1"},

		{n: "default_in_range", src: `r := ""; for _, c := range "abc" { switch c { case 97: r += "A"; default: r += string(c) } }; r`, res: "Abc"},

		// Stray comment between '{' and the first 'case' must not crash; this
		// pattern appears in github.com/google/uuid/uuid.go (Parse, Validate).
		{n: "comment_before_first_case", src: "func f(x int) string {\n\tswitch x {\n\t// leading comment\n\tcase 1: return \"one\"\n\tdefault: return \"other\"\n\t}\n}; f(1)", res: "one"},
		{n: "comment_in_type_switch", src: "func f(v interface{}) string {\n\tswitch v.(type) {\n\t// numeric branch\n\tcase int: return \"int\"\n\tdefault: return \"other\"\n\t}\n}; f(42)", res: "int"},
		{n: "comment_in_select", src: "func f() int {\n\tch := make(chan int, 1); ch <- 7\n\tselect {\n\t// pick from ch\n\tcase v := <-ch: return v\n\t}\n\treturn 0\n}; f()", res: "7"},

		// Each case clause is its own implicit block: a `v := ...` in one case
		// must not rebind v for sibling cases (go-cmp FormatValue shadows its
		// param v inside case reflect.Struct).
		{n: "case_shadow_param", src: "func f(v map[string]int, k int) bool {\n\tswitch k {\n\tcase 1:\n\t\tv := map[string]int{\"x\": 9}\n\t\t_ = v\n\tcase 2:\n\t\treturn v == nil\n\t}\n\treturn true\n}; f(map[string]int{\"a\": 1}, 2)", res: "false"},
		{n: "case_shadow_sibling", src: "func f(k int) int {\n\tn := 7\n\tswitch k {\n\tcase 1:\n\t\tn := 1\n\t\t_ = n\n\tcase 2:\n\t\treturn n\n\t}\n\treturn 0\n}; f(2)", res: "7"},
	})
}

func TestConst(t *testing.T) {
	src0 := `const (
	a = iota
	b
	c
)
`
	run(t, []etest{
		{n: "#00", src: "const a = 1+2; a", res: "3"},
		{n: "#01", src: "const a, b = 1, 2; a+b", res: "3"},
		{n: "#02", src: "const huge = 1 << 100; const four = huge >> 98; four", res: "4"},

		{n: "#03", src: src0 + "c", res: "2"},
		{n: "#04", src: `func f() string {return a}; const a = "hello"; f()`, res: "hello"},

		{n: "fwd_in_block", src: `const (a = 2; b = c + d; c = 4; d = 5); b`, res: "9"},
		{n: "fwd_deep_chain", src: `const (a = 2; b = c + d; c = a + d; d = e + f; e = 3; f = 4); b`, res: "16"},
		{n: "fwd_cross_block", src: `const b = c + 1; const c = 5; b`, res: "6"},
		{n: "fwd_array_size", src: `
const maxN = 30
const bufSize = maxN + 2
type T struct { pos uint8; size uint8 }
type buf struct { data [bufSize]T }
len(buf{}.data)`, res: "32"},

		{n: "len_string", src: `const n = len("hello"); n`, res: "5"},
		{n: "len_string_expr", src: `const n = len("hello") + 1; n`, res: "6"},

		{n: "conv_int", src: `const a = int(3.0); a`, res: "3"},
		{n: "conv_float", src: `const a = float64(3) + 0.5; a`, res: "3.5"},
		{n: "conv_string", src: `const a = string(65); a`, res: "A"},

		{n: "len_array_var", src: `var a = [3]int{1,2,3}; const n = len(a); n`, res: "3"},
		{n: "cap_array_var", src: `var a = [...]int{1,2,3}; const n = cap(a); n`, res: "3"},

		{n: "sub_add_chain", src: `const (a = 2; b = 10; c = b - a + 1); c`, res: "9"},                                        // right-assoc: 10-(2+1)=7
		{n: "typed_sub_add", src: `type L int8; const (a L = -1; b L = 5; d = b - a + 1); type A [d]int; len(A{})`, res: "7"}, // right-assoc: b-(a+1)=5

		{n: "typed_iota", src: `import "fmt"; const (a uint8 = 2 * iota; b; c); fmt.Sprintf("%T %v %v %v", c, a, b, c)`, res: "uint8 0 2 4"},

		{n: "pkg_value_expr", src: `import "time"; const period = 300 * time.Millisecond; int(period)`, res: "300000000"},
		{n: "grouped_pkg_value", src: `import "time"; const (a = 300 * time.Millisecond; b = 30 * time.Millisecond); int(b)`, res: "30000000"},
		{n: "pkg_value_call", src: `import "time"; const d = 100 * time.Millisecond; time.Sleep(d); "ok"`, res: "ok"},

		// Conversion of an untyped constant to an imported named type, folded at compile time.
		{n: "pkg_type_conv", src: `import "time"; const d = time.Duration(5); int64(d)`, res: "5"},
		{n: "pkg_type_conv_expr", src: `import "time"; const d = time.Duration(3) * time.Second; d.String()`, res: "3s"},

		{n: "uint64_complement", src: `const a = ^uint64(0); a`, res: "18446744073709551615"},
		{n: "uint64_maxint", src: `const a = int64(^uint64(0) >> 1); a`, res: "9223372036854775807"},
		{n: "uint8_complement", src: `import "fmt"; const a = ^uint8(0); fmt.Sprintf("%T %v", a, a)`, res: "uint8 255"},
		{n: "uint64_nested_conv", src: `const a = int64(int64(^uint64(0) >> 1)); a`, res: "9223372036854775807"},

		{n: "leading_comment_in_block", src: "const (\n\t// header comment\n\ta = 1\n\tb = 2\n)\na+b", res: "3"},
	})
}

func TestArray(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "type T []int; var t T; t", res: "[]"},
		{n: "#01", src: "type T [3]int; var t T; t", res: "[0 0 0]"},
		{n: "#02", src: "type T [3]int; var t T; t[1]", res: "0"},
		{n: "#03", src: "type T [3]int; var t T; t[1] = 2; t", res: "[0 2 0]"},

		{n: "ellipsis", src: `a := [...]int{10, 20, 30}; len(a)`, res: "3"},
		{n: "ellipsis_key", src: `a := [...]string{9: "hello"}; len(a)`, res: "10"},
		{n: "ellipsis_keys", src: `a := [...]int{2: 10, 5: 20}; len(a)`, res: "6"},
		{n: "ellipsis_const_key", src: `const (a = iota; b; c); x := [...]int{c: 99}; len(x)`, res: "3"},
		{n: "ellipsis_const_expr_key", src: `const (a = iota; b; c); x := [...]int{c + 2: 99}; len(x)`, res: "5"},
		// Length is highest index + 1, not last key + 1 (keys out of order).
		{n: "ellipsis_keys_unordered", src: `a := [...]int{3: 30, 1: 10}; len(a)`, res: "4"},
		{n: "ellipsis_keys_unordered_vals", src: `a := [...]int{3: 30, 1: 10}; a[3] + a[1]`, res: "40"},

		{n: "2d_array", src: `a := [3][2]int{{1, 2}, {3, 4}, {5, 6}}; a[1][0]`, res: "3"},
		{n: "2d_slice", src: `a := [][]int{{1, 2}, {3, 4}}; a[1][0]`, res: "3"},

		// Slice with explicit-index keys: parseComposite must size the outer
		// to highest-index + 1, else Fnew allocates a zero-length slice and
		// the per-element IndexSet panics (see paradigmLocales in
		// golang.org/x/text/language/tables.go).
		{n: "slice_indexed_keys", src: `a := [][3]uint16{0: {1, 2, 3}, 1: {4, 5, 6}, 2: {7, 8, 9}}; a[2][1]`, res: "8"},
		{n: "slice_sparse_index", src: `a := []int{2: 7}; len(a)`, res: "3"},
		// Mixed keyed/unkeyed slice elements: parseComposite synthesizes
		// [curIdx, value, colon] tokens around unkeyed elements so the
		// compiler emits IndexSet at the right slot.
		{n: "slice_mixed_keys", src: `a := []int{2: 7, 9}; len(a)`, res: "4"},
		{n: "slice_mixed_keys_value", src: `a := []int{2: 7, 9}; a[3]`, res: "9"},
		{n: "slice_mixed_keys_leading_unkeyed", src: `a := []int{1, 2: 7, 9}; a[0]`, res: "1"},
		{n: "slice_mixed_keys_leading_unkeyed_len", src: `a := []int{1, 2: 7, 9}; len(a)`, res: "4"},
		// Const-expression keys (e.g. reflect.Bool) are normalized to literal
		// Int keys at parse; the compiler only consumes Int-keyed elements.
		{n: "slice_qualified_const_key", src: `import "reflect"; a := []string{reflect.Bool: "b"}; len(a)`, res: "2"},
		{n: "slice_qualified_const_key_val", src: `import "reflect"; a := []string{reflect.Bool: "b", reflect.String: "s"}; a[1] + a[24]`, res: "bs"},
		{n: "slice_local_const_key", src: `const k = 2; a := []string{k: "two", 0: "zero"}; len(a)`, res: "3"},
		// Arrays share the slice keyed-element handling (const-expr key
		// normalization and mixed unkeyed synthesis).
		{n: "array_qualified_const_key", src: `import "reflect"; a := [3]string{reflect.Bool: "b"}; a[1]`, res: "b"},
		{n: "array_ellipsis_qualified_const_key", src: `import "reflect"; a := [...]string{reflect.Bool: "b"}; a[1]`, res: "b"},
		{n: "array_mixed_keys", src: `a := [5]int{1, 2: 7, 9}; a[0] + a[2] + a[3]`, res: "17"},
		{n: "slice_after_make", src: `a := []int{1, 2, 3}; b := make([]int, 2); copy(b, a); b[1]`, res: "2"},
		{n: "multi_slice_lit", src: `a := []int{1, 2, 3}; b := []int{4, 5}; a[2] + b[1]`, res: "8"},
		{n: "2d_named", src: `type M [3][16]int; m := M{}; m[0][1] = 7; m[0][1]`, res: "7"},

		{n: "ptr_index", src: `type T [2]int; func f(t *T) int { return t[0] }; f(&T{1, 2})`, res: "1"},
		{n: "ptr_index_set", src: `type T [2]int; t := &T{1, 2}; t[1] = 9; t[1]`, res: "9"},
		{n: "ptr_range2", src: `
type T [3]int
func f(t *T) int { s := 0; for _, v := range t { s += v }; return s }
f(&T{1, 2, 3})`, res: "6"},
		{n: "ptr_range1", src: `
type T [3]int
t := &T{10, 20, 30}
s := 0; for i := range t { s += i }; s`, res: "3"},

		// len(v.Field) where Field is an array type is a compile-time constant,
		// usable as an array size. Test both package-level and local variables,
		// and both [N]T and *[N]T field types.
		{n: "len_field_const_local", src: `
type T struct{ Path [12]int8 }
t := &T{}
b := [12]byte{}
p := (*[len(t.Path)]byte)(&b)
len(p)`, res: "12"},
		{n: "len_field_const_pkgvar", src: `
type T struct{ Path [12]int8 }
var t = &T{}
b := [12]byte{}
p := (*[len(t.Path)]byte)(&b)
len(p)`, res: "12"},
		{n: "len_field_const_ptr_field", src: `
type T struct{ Path *[12]int8 }
t := &T{}
b := [12]byte{}
p := (*[len(t.Path)]byte)(&b)
len(p)`, res: "12"},
	})
}

func TestPointer(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "var a *int; a", res: "<nil>"},
		{n: "#01", src: "var a int; var b *int = &a; *b", res: "0"},
		{n: "#02", src: "var a int = 2; var b *int = &a; *b", res: "2"},

		{n: "deref_assign_int", src: "var a int; p := &a; *p = 42; a", res: "42"},
		{n: "deref_assign_string", src: "var s string; p := &s; *p = \"hello\"; s", res: "hello"},
		{n: "deref_assign_expr", src: "var a int; p := &a; *p = 3 + 4; a", res: "7"},
		{n: "deref_assign_func", src: `var a int; func f() *int { return &a }; *f() = 99; a`, res: "99"},
		{n: "deref_assign_slice", src: `var a, b int; s := []*int{&a, &b}; *s[1] = 10; b`, res: "10"},
		{n: "deref_field_assign", src: `type T struct { x int }; p := &T{0}; (*p).x = 5; p.x`, res: "5"},
		{n: "deref_index_assign", src: `s := []int{1, 2, 3}; p := &s; (*p)[1] = 20; s[1]`, res: "20"},
		{n: "auto_deref_field", src: `type T struct { x int }; p := &T{0}; p.x = 7; p.x`, res: "7"},
		{n: "deref_double", src: `var a int; p := &a; pp := &p; **pp = 33; a`, res: "33"},
		{n: "deref_assign_new", src: "p := new(int); *p = 5; *p", res: "5"},
		{n: "deref_assign_nil_slice", src: "var s []byte; p := &s; *p = nil; *p == nil", res: "true"},
		{n: "deref_assign_nil_map", src: "var m map[string]int; p := &m; *p = nil; *p == nil", res: "true"},
		{n: "deref_assign_nil_new_slice", src: "p := new([]byte); *p = nil; *p == nil", res: "true"},
		{n: "deref_inc", src: "a := 2; p := &a; *p++; a", res: "3"},
		{n: "deref_dec", src: "a := 2; p := &a; *p--; a", res: "1"},
		{n: "iife_ptr", src: `var a = func() *bool { b := true; return &b }(); *a && true`, res: "true"},
		{n: "iife_stmt", src: `a := 0; func() { a = 5 }(); a`, res: "5"},
		{n: "iife_stmt_arg", src: `a := 0; func(x int) { a = x }(7); a`, res: "7"},
		{n: "addr_slice_elem", src: `a := []int{1, 2, 3}; p := &a[1]; *p = 99; a[1]`, res: "99"},
		{n: "addr_array_elem", src: `a := [3]int{1, 2, 3}; p := &a[1]; *p = 99; a[1]`, res: "99"},

		{n: "addr_2d_elem", src: `
type counters [3][16]int
cs := &counters{}
p := &cs[0][1]
*p = 2
cs[0][1]`, res: "2"},
		{n: "var_ptr_native", src: `import "os"; var fd *os.File; fd`, res: "<nil>"},
		{n: "var_ptr_local", src: `type T struct{X int}; var v *T; v`, res: "<nil>"},

		{n: "addr_param_iface", src: `func f(r interface{}) int { p := &r; return (*p).(int) }; f(42)`, res: "42"},
		{n: "addr_param_nil", src: `func f(r interface{}) bool { p := &r; return *p == nil }; f(nil)`, res: "true"},
		{n: "addr_param_string", src: `func f(s string) string { return *(&s) }; f("hello")`, res: "hello"},
	})
}

func TestStruct(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "type T struct {a string; b, c int}; var t T; t", res: "{ 0 0}"},
		{n: "#01", src: "type T struct {a int}; var t T; t.a", res: "0"},
		{n: "#02", src: "type T struct {a int}; var t T; t.a = 1; t.a", res: "1"},
		{n: "#03", src: "type T struct {a int}; var t T = T{1}; t.a", res: "1"},
		{n: "#04", src: "type T struct {a int}; var t *T = &T{1}; t.a", res: "1"},
		{n: "field_name_matches_var", src: `type T struct{x int}; x := 42; t := T{x: x}; t.x`, res: "42"},
		{n: "iface_field_fmt", src: `import "fmt"; type T struct{V interface{}}; t := T{V: "hello"}; fmt.Sprint(t)`, res: "{hello}"},
		{n: "iface_field_assign_fmt", src: `import "fmt"; type T struct{V interface{}}; t := T{}; t.V = 42; fmt.Sprint(t)`, res: "{42}"},
		{n: "nil_assign_struct_field", src: `type node struct { parent *node; child []*node; key string }; root := &node{key: "root"}; root.child = nil; root.parent = nil; root.key`, res: "root"},

		{n: "field_tag", src: `import "reflect"; type T struct { Name string ` + "`" + `json:"name"` + "`" + `}; f, _ := reflect.TypeOf(T{}).FieldByName("Name"); f.Tag.Get("json")`, res: "name"},

		// reflect.TypeFor[T]() is a Go 1.22 generic native that mvm cannot
		// bind via reflect.ValueOf; goparser.RegisterGenericShim installs an
		// interpreted equivalent at parser setup. These cases exercise the
		// pkg.Generic[T] lookup path, generic-instantiation pipeline, the
		// (*T)(nil) typed-conversion idiom in the body, and method dispatch
		// on the returned reflect.Type.
		{n: "typefor_size", src: `import "reflect"; type Tag struct { a uint64; b uint16; c uint8 }; reflect.TypeFor[Tag]().Size()`, res: "16"},
		{n: "typefor_numfield", src: `import "reflect"; type Tag struct { a uint64; b uint16 }; reflect.TypeFor[Tag]().NumField()`, res: "2"},
		{n: "typefor_field_pkgpath", src: `import "reflect"; type Tag struct { a uint64 }; reflect.TypeFor[Tag]().Field(0).PkgPath != ""`, res: "true"},
		{n: "typefor_primitive", src: `import "reflect"; reflect.TypeFor[int]().Kind().String()`, res: "int"},

		// errors.AsType[E error] is a Go 1.26 generic native that cannot be bound
		// via reflect.ValueOf; goparser.RegisterGenericShim installs an
		// interpreted equivalent (stdlib/errors_shim.go) instantiated through the
		// normal generic pipeline. See [[project_mvm_test_errors]].
		{n: "astype_match", src: `import "errors"; type E struct{ s string }; func (e *E) Error() string { return e.s }; var err error = &E{"boom"}; _, ok := errors.AsType[*E](err); ok`, res: "true"},
		{n: "astype_value", src: `import "errors"; type E struct{ s string }; func (e *E) Error() string { return e.s }; var err error = &E{"boom"}; v, _ := errors.AsType[*E](err); v.s`, res: "boom"},
		{n: "astype_nomatch", src: `import "errors"; type E struct{ s string }; func (e *E) Error() string { return e.s }; type F struct{ n int }; func (f *F) Error() string { return "f" }; var err error = &E{"boom"}; _, ok := errors.AsType[*F](err); ok`, res: "false"},
		{n: "astype_unwrap_chain", src: `import "errors"; import "fmt"; type E struct{ s string }; func (e *E) Error() string { return e.s }; base := &E{"inner"}; w := fmt.Errorf("ctx: %w", base); v, ok := errors.AsType[*E](w); ok && v.s == "inner"`, res: "true"},

		// Generic sync helpers, installed as a shim (stdlib/sync_shim.go) since
		// they can't bind via reflect.ValueOf. See [[project_sync_oncevalue_shim]].
		{n: "oncevalue_caches", src: `import "sync"; n := 0; f := sync.OnceValue(func() int { n++; return 7 }); a := f() + f(); a*10 + n`, res: "141"},
		{n: "oncevalues_multi", src: `import "sync"; g := sync.OnceValues(func() (int, int) { return 3, 4 }); a, b := g(); c, d := g(); a + b + c + d`, res: "14"},
		{n: "oncevalue_panics", src: `import "sync"; f := sync.OnceValue(func() int { panic("boom") }); r := func() (s string) { defer func() { s, _ = recover().(string) }(); f(); return }(); r`, res: "boom"},

		// A panic inside a deferred func used to loop forever (vm: deferStartedFlag).
		// See [[project_panic_in_defer_hang]].
		{n: "repanic_in_defer", src: `func f() (s string) { defer func() { s = recover().(string) }(); func() { defer func() { panic(recover()) }(); panic("x") }(); return }; f()`, res: "x"},
		{n: "panic_in_defer_normal_return", src: `func inner() { defer func() { panic("x") }() }; func f() (s string) { defer func() { s = recover().(string) }(); inner(); return }; f()`, res: "x"},
		{n: "panic_in_defer_earlier_still_runs", src: `func inner(log *[]int) { defer func() { *log = append(*log, 1) }(); defer func() { panic("x") }() }; func f() int { log := []int{}; func() { defer func() { recover() }(); inner(&log) }(); return len(log) }; f()`, res: "1"},

		// SKIP: recovering an inner panic (raised in a defer running during an
		// outer panic's unwind) drops the outer one -- mvm has a single panic
		// slot, not a stack. See [[project_panic_in_defer_hang]].
		{n: "nested_panic_outer_resumes", skip: true, src: `func f() (out string) { defer func() { if r := recover(); r != nil { out = r.(string) } }(); defer func() { defer func() { recover() }(); panic("inner") }(); panic("outer"); return }; f()`, res: "outer"},

		// Go 1.21+: panic of a nil interface yields *runtime.PanicNilError; a
		// typed nil stays itself. See [[project_panic_nil_error]].
		{n: "panic_nil_literal", src: `func f() (s string) { defer func() { s = recover().(error).Error() }(); panic(nil); return }; f()`, res: "panic called with nil argument"},
		{n: "panic_nil_iface", src: `func f() (s string) { defer func() { s = recover().(error).Error() }(); var e error; panic(e); return }; f()`, res: "panic called with nil argument"},
		{n: "panic_typed_nil_kept", src: `func f() (out bool) { defer func() { _, out = recover().(*int) }(); panic((*int)(nil)); return }; f()`, res: "true"},

		// defer in a range body used to crash (sp-resident iterator displaced by
		// the defer entry); now off-stack. See [[project_defer_in_range_loop_bug]].
		{n: "defer_in_range_lifo", src: `func f() string { s := ""; func() { for _, k := range []string{"a", "b"} { defer func(k string) { s += k }(k) } }(); return s }; f()`, res: "ba"},
		{n: "defer_in_range_break", src: `func f() string { s := ""; func() { for _, k := range []string{"a", "b", "c"} { defer func(k string) { s += k }(k); if k == "b" { return } } }(); return s }; f()`, res: "ba"},
		{n: "defer_in_range_panic_recover", src: `func f() string { s := ""; func() { defer func() { recover() }(); for _, k := range []int{1, 2, 3} { defer func(k int) { s += string(rune('0' + k)) }(k); if k == 2 { panic("x") } } }(); return s }; f()`, res: "21"},
		{n: "defer_in_nested_range", src: `func f() int { n := 0; for _, a := range []int{1, 2} { for _, b := range []int{1, 2} { defer func() { n++ }(); _ = a; _ = b } }; return n }; f()`, res: "0"},
		// Native callback re-enters Run with a reset fp; must not reclaim the
		// outer Run's live iterators (iterBase floor). g(2) = 3+3*3+3*9 iters.
		{n: "range_iter_reentrant_callback", src: `import "fmt"; type e struct{ s string }; func (x e) String() string { return x.s }; func g(n int) int { c := 0; for _, v := range []int{1, 2, 3} { _ = fmt.Sprint(e{"x"}); c++; _ = v; if n > 0 { c += g(n-1) } }; return c }; g(2)`, res: "39"},

		{n: "errors_is_custom_match", src: `import "errors"; import "io/fs"; type E struct{ s string }; func (e E) Error() string { return e.s }; func (e E) Is(t error) bool { return t == fs.ErrPermission }; var err error = E{"x"}; errors.Is(err, fs.ErrPermission)`, res: "true"},
		{n: "errors_is_custom_nomatch", src: `import "errors"; import "io/fs"; type E struct{ s string }; func (e E) Error() string { return e.s }; func (e E) Is(t error) bool { return t == fs.ErrPermission }; var err error = E{"x"}; errors.Is(err, fs.ErrNotExist)`, res: "false"},

		// Custom Is survives fmt.Errorf %w (the `any` variadic boundary).
		{n: "errors_is_through_native_wrap", src: `import "errors"; import "fmt"; import "io/fs"; type E struct{ s string }; func (e E) Error() string { return e.s }; func (e E) Is(t error) bool { return t == fs.ErrPermission }; w := fmt.Errorf("ctx: %w", E{"x"}); errors.Is(w, fs.ErrPermission)`, res: "true"},

		{n: "errors_as_custom_match", src: `import "errors"; import "io/fs"; type E struct{ s string }; func (e E) Error() string { return e.s }; func (e E) As(target any) bool { pe, ok := target.(**fs.PathError); if !ok { return false }; *pe = &fs.PathError{Op: "custom", Path: "/", Err: errors.New(e.s)}; return true }; var err error = E{"boom"}; var pe *fs.PathError; ok := errors.As(err, &pe); ok && pe.Path == "/"`, res: "true"},

		{n: "errors_is_with_unwrap_method", src: `import "errors"; import "io/fs"; type E struct{ inner error }; func (e E) Error() string { return "e" }; func (e E) Is(t error) bool { return t == fs.ErrPermission }; func (e E) Unwrap() error { return e.inner }; var err error = E{inner: errors.New("x")}; errors.Is(err, fs.ErrPermission)`, res: "true"},

		{n: "errors_is_and_as_combined", src: `import "errors"; import "io/fs"; type E struct{ s string }; func (e E) Error() string { return e.s }; func (e E) Is(t error) bool { return t == fs.ErrPermission }; func (e E) As(target any) bool { pe, ok := target.(**fs.PathError); if !ok { return false }; *pe = &fs.PathError{Op: "custom", Path: "/", Err: errors.New(e.s)}; return true }; var err error = E{"x"}; var pe *fs.PathError; errors.Is(err, fs.ErrPermission) && errors.As(err, &pe) && pe.Path == "/"`, res: "true"},

		{n: "errors_as_with_unwrap_method", src: `import "errors"; import "io/fs"; type E struct{ inner error }; func (e E) Error() string { return "e" }; func (e E) As(target any) bool { pe, ok := target.(**fs.PathError); if !ok { return false }; *pe = &fs.PathError{Op: "custom", Path: "/", Err: errors.New("x")}; return true }; func (e E) Unwrap() error { return e.inner }; var err error = E{inner: errors.New("y")}; var pe *fs.PathError; errors.As(err, &pe) && pe.Path == "/"`, res: "true"},

		// %T shows the source type name, not a bridge StructOf name.
		{n: "errors_pct_T_identity", src: `import "fmt"; type E struct{ s string }; func (e E) Error() string { return e.s }; var err error = E{"x"}; fmt.Sprintf("%T", err)`, res: "main.E"},

		{n: "errors_multierror_is", src: `import "errors"; import "fmt"; import "io/fs"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; var err error = M{errors.New("x"), fmt.Errorf("w: %w", fs.ErrPermission)}; errors.Is(err, fs.ErrPermission)`, res: "true"},

		{n: "errors_multierror_is_miss", src: `import "errors"; import "fmt"; import "io/fs"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; var err error = M{errors.New("x"), fmt.Errorf("w: %w", fs.ErrPermission)}; errors.Is(err, fs.ErrNotExist)`, res: "false"},

		{n: "errors_multierror_as", src: `import "errors"; import "io/fs"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; var err error = M{errors.New("x"), &fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}}; var pe *fs.PathError; errors.As(err, &pe) && pe.Path == "/x"`, res: "true"},

		{n: "errors_multierror_astype", src: `import "errors"; import "io/fs"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; var err error = M{&fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}}; _, ok := errors.AsType[*fs.PathError](err); ok`, res: "true"},

		{n: "errors_multierror_astype_nested", src: `import "errors"; type errorT struct{ s string }; func (e errorT) Error() string { return e.s }; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; err := error(M{M{errors.New("a"), errorT{"x"}}, errorT{"y"}}); got, ok := errors.AsType[errorT](err); ok && got.s == "x"`, res: "true"},

		{n: "iface_method_sig_discriminates", src: `type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; var e error = M{}; _, single := e.(interface{ Unwrap() error }); _, multi := e.(interface{ Unwrap() []error }); !single && multi`, res: "true"},

		{n: "reflect_valueof_ptr_error_no_bridge", src: `import "reflect"; type E struct{ s string }; func (e E) Error() string { return e.s }; e := E{"x"}; v, ok := reflect.ValueOf(&e).Elem().Interface().(E); ok && v.s == "x"`, res: "true"},

		{n: "reflect_valueof_value_method_visible", src: `import "reflect"; type S struct{ s string }; func (x S) String() string { return "S:"+x.s }; reflect.ValueOf(S{"z"}).MethodByName("String").IsValid()`, res: "true"},

		{n: "errors_as_into_struct", src: `import "errors"; type E struct{ s string }; func (e E) Error() string { return e.s }; type w struct{ e error }; func (x w) Error() string { return "w" }; func (x w) Unwrap() error { return x.e }; var got E; ok := errors.As(w{E{"x"}}, &got); ok && got.s == "x"`, res: "true"},

		// Regression: value-receiver Error/Is methods must be promoted onto the
		// synth *T method set so *E satisfies native error (Go: method-set(*T)
		// includes value methods). Before the fix errors.Is(&E{}, ...) panicked
		// "reflect: Call using *E as type error".
		{n: "errors_is_ptr_recv_value_method", src: `import "errors"; import "io/fs"; type E struct{ s string }; func (e E) Error() string { return e.s }; func (e E) Is(t error) bool { return t == fs.ErrPermission }; var err error = &E{"x"}; errors.Is(err, fs.ErrPermission)`, res: "true"},

		// SKIP (documented gap): a panic from an interpreted Is dispatched by
		// native errors.Is is swallowed to false rather than propagated, because
		// re-panicking it crashes the nested-panic-across-native-boundary path
		// (interp -> native errors.Is -> interp Is -> panic leaves the machine
		// stack inconsistent on unwind: "index out of range" while running the
		// deferred recover). The synth S4/S6/S7 handlers therefore swallow. Want:
		// the panic propagates and recover() sees it (res "true").
		{n: "errors_is_panic_propagates", skip: true, src: `import "errors"; type E struct{ s string }; func (e E) Error() string { return e.s }; func (e E) Is(t error) bool { panic("boom") }; func f() (r bool) { defer func() { r = recover() != nil }(); errors.Is(E{"x"}, errors.New("t")); return false }; f()`, res: "true"},

		// Regression: multiple blank `_` result params must each get their own
		// slot+type; before the fix they collided on one "scope/_" key, so the
		// bare return zero-init wrote a struct zero into the bool slot
		// ("reflect.Set: ... not assignable to bool"). Surfaced via errors.AsType.
		{n: "blank_struct_result_bare_return", src: `type S struct{ n int }; func g() (_ S, _ bool) { return }; _, b := g(); !b`, res: "true"},
		{n: "generic_blank_result_bare_return", src: `type S struct{ n int }; func g[E any]() (_ E, _ bool) { return }; _, b := g[S](); !b`, res: "true"},

		{n: "errors_as_validation_basic", src: `import "errors"; func f() (r bool) { defer func() { r = recover() != nil }(); var s string; errors.As(errors.New("e"), &s); return false }; f()`, res: "true"},

		// errors.As into a method-bearing anonymous interface target: a synth
		// interface rtype built at the boundary resolves real satisfaction instead
		// of a methodless any (which matched every error). See vm.bridgePtrToIface.
		{n: "errors_as_anon_iface_target_via_any", src: `import "errors"; var timeout interface{ Timeout() bool }; tc := struct{ t any }{&timeout}; errors.As(errors.New("e"), tc.t)`, res: "false"},

		{n: "errors_as_anon_iface_target_unwrap", src: `import "errors"; import "os"; _, errF := os.Open("/nonexistent-x"); type W struct{ e error }; func (w W) Error() string { return "w" }; func (w W) Unwrap() error { return w.e }; var t interface{ Timeout() bool }; tc := struct{ t any }{&t}; errors.As(W{errF}, tc.t)`, res: "true"},

		// Unwrap to an error that does NOT implement the target: no match (was
		// wrongly "true" before the fix).
		{n: "errors_as_anon_iface_unwrap_nomatch", src: `import "errors"; type W struct{ e error }; func (w W) Error() string { return "w" }; func (w W) Unwrap() error { return w.e }; var t interface{ Timeout() bool }; tc := struct{ t any }{&t}; errors.As(W{errors.New("x")}, tc.t)`, res: "false"},

		// Direct read of the iface var after As (synth iface rtype as storage identity).
		{n: "errors_as_anon_iface_direct_read", src: `import "errors"; import "os"; _, errF := os.Open("/nonexistent-x"); type W struct{ e error }; func (w W) Error() string { return "w" }; func (w W) Unwrap() error { return w.e }; var t interface{ Timeout() bool }; errors.As(W{errF}, &t); t != nil`, res: "true"},

		{n: "errors_multierror_self", src: `import "errors"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; var err error = M{errors.New("x")}; errors.Is(err, err)`, res: "false"},

		{n: "errors_multierror_custom_is", src: `import "errors"; import "fmt"; import "io/fs"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; func (m M) Is(t error) bool { return t == fs.ErrExist }; var err error = M{fmt.Errorf("w: %w", fs.ErrPermission)}; errors.Is(err, fs.ErrExist) && errors.Is(err, fs.ErrPermission)`, res: "true"},

		{n: "errors_multierror_custom_as", src: `import "errors"; import "io/fs"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; func (m M) As(target any) bool { pe, ok := target.(**fs.PathError); if !ok { return false }; *pe = &fs.PathError{Op: "c", Path: "/p"}; return true }; var err error = M{errors.New("x")}; var pe *fs.PathError; errors.As(err, &pe) && pe.Path == "/p"`, res: "true"},

		{n: "errors_multierror_custom_is_as", src: `import "errors"; import "io/fs"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; func (m M) Is(t error) bool { return t == fs.ErrExist }; func (m M) As(target any) bool { pe, ok := target.(**fs.PathError); if !ok { return false }; *pe = &fs.PathError{Op: "c", Path: "/p"}; return true }; var err error = M{errors.New("x")}; var pe *fs.PathError; errors.Is(err, fs.ErrExist) && errors.As(err, &pe) && pe.Path == "/p"`, res: "true"},

		{n: "errors_join_unwrap_deepequal", src: `import "errors"; import "reflect"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; merr := M{errors.New("e3")}; got := errors.Join(merr).(interface{ Unwrap() []error }).Unwrap(); reflect.DeepEqual(got, []error{merr})`, res: "true"},

		{n: "errors_deepequal_array", src: `import "errors"; import "reflect"; type M []error; func (m M) Error() string { return "m" }; func (m M) Unwrap() []error { return []error(m) }; e := errors.New("e3"); reflect.DeepEqual([1]error{M{e}}, [1]error{M{e}})`, res: "true"},

		{n: "reflect_deepequal_named_in_any_slice", src: `import "reflect"; type Xs string; var x Xs = "ee"; st := reflect.TypeOf(make([]any, 1)); rv := reflect.MakeSlice(st, 1, 1); rv.Index(0).Set(reflect.ValueOf(&x).Elem()); reflect.DeepEqual(rv.Interface(), []any{Xs("ee")})`, res: "true"},

		{n: "reflect_deepequal_methodful_in_any_slice", src: `import "reflect"; type Xs string; func (x Xs) String() string { return string(x) }; var x Xs = "ee"; st := reflect.TypeOf(make([]any, 1)); rv := reflect.MakeSlice(st, 1, 1); rv.Index(0).Set(reflect.ValueOf(&x).Elem()); reflect.DeepEqual(rv.Interface(), []any{Xs("ee")})`, res: "true"},

		{n: "fmt_methodful_in_ptr_any_slice_field", src: `import "fmt"; type I int; func (i I) String() string { return fmt.Sprintf("<%d>", int(i)) }; type SI struct{ X any }; func run() string { return fmt.Sprintf("%z", SI{&[]any{I(1), I(2)}}) }; run()`, res: "{%!z(*[]interface {}=&[1 2])}"},

		{n: "fmt_pct_T_interpreted_error", src: `import "fmt"; type S struct{ s string }; func (e S) Error() string { return e.s }; var err error = S{"x"}; fmt.Sprintf("%T", err)`, res: "main.S"},

		{n: "fmt_named_num_in_any_slice", src: `import "fmt"; type I int; func (i I) String() string { return fmt.Sprintf("<%d>", int(i)) }; func run() string { return fmt.Sprintf("%v", []any{I(1), I(2)}) }; run()`, res: "[<1> <2>]"},

		{n: "fmt_nil_func_in_array_gosyntax", src: `import "fmt"; type Fn func() int; func (fn Fn) String() string { return "s" }; var fnValue Fn; func run() string { return fmt.Sprintf("%#v", [1]Fn{fnValue}) }; run()`, res: "[1]main.Fn{(main.Fn)(nil)}"},

		{n: "fmt_empty_struct_method_name", src: `import "fmt"; type E struct{}; func (E) Hi() string { return "h" }; func run() string { return fmt.Sprintf("%T", E{}) }; run()`, res: "main.E"},

		// Multierror Unwrap() []error promoted from an embedded field.
		{n: "errors_multierror_embedded", src: `import "errors"; import "fmt"; import "io/fs"; type base []error; func (b base) Error() string { return "b" }; func (b base) Unwrap() []error { return []error(b) }; type wrap struct{ base }; var err error = wrap{base{fmt.Errorf("w: %w", fs.ErrPermission)}}; errors.Is(err, fs.ErrPermission)`, res: "true"},

		// SKIP (fundamental gap): interpreted methods are invisible to NATIVE reflect.
		// Tier-1 value path: a type assertion on an interpreted value round-tripped
		// through native reflect (reflect.Zero(t).Interface()) must recognize the
		// interpreted type's methods. The TypeAssert opcode recovers the *Type via
		// typeByRtype and consults vm.Type.Implements (the synthetic rtype itself
		// carries no native methods). reflect.Type.NumMethod / reflect.Type.Implements
		// remain Tier-2 gaps (still rtype-level).
		{n: "reflect_interpreted_method_via_iface_assert", src: `
import "reflect"
type T struct{ X int }
func (T) Hello() string { return "hi" }
type Speaker interface{ Hello() string }
t := reflect.TypeOf(T{})
_, ok := reflect.Zero(t).Interface().(Speaker)
ok`, res: "true"},

		{n: "reflect_value_methodbyname_iface", src: `
import "reflect"
type Namer interface { Name() string }
type T struct{}
func (T) Name() string { return "hello" }
var n Namer = T{}
rv := reflect.ValueOf(n)
rv.MethodByName("Name").Call(nil)[0].String()`, res: "hello"},

		{n: "reflect_value_methodbyname_slice_return", src: `
import "reflect"
type Lister interface { List() []int }
type L struct{}
func (L) List() []int { return []int{1,2,3} }
var l Lister = L{}
rv := reflect.ValueOf(l)
out := rv.MethodByName("List").Call(nil)[0].Interface().([]int)
len(out)`, res: "3"},

		{n: "reflect_value_methodbyname_reentrant", src: `
import "reflect"
type Greeter interface { Hi() string }
type G struct{}
func (G) Hi() string { return "hi" }
var g Greeter = G{}
f := func() string {
	rv := reflect.ValueOf(g)
	return rv.MethodByName("Hi").Call(nil)[0].String()
}
reflect.ValueOf(f).Call(nil)[0].String()`, res: "hi"},

		{n: "reflect_assert_iface_okform", src: `
import "reflect"
type T struct{ x int }
func (t T) Hello() string { return "hi" }
type Speaker interface{ Hello() string }
func f() string { x := reflect.ValueOf(T{5}).Interface(); s, ok := x.(Speaker); if !ok { return "no" }; return s.Hello() }
f()`, res: "hi"},

		// A by-value struct param must be addressable for a field assignment even
		// when the argument itself is unaddressable (here, a map-index result).
		{n: "unaddressable_struct_arg_field_set", src: `
type Attr struct{ Key string; Val int }
func g(a Attr) Attr { a.Key = "sev"; a.Val++; return a }
m := map[string]Attr{"k": {Key: "level", Val: 41}}
r := g(m["k"]); r.Key`, res: "sev"},

		// A closure invoked from native code (reflect.Call) must get an
		// addressable struct param so it can assign to a field of its by-value arg.
		{n: "reflect_call_struct_param_field_set", src: `
import ("fmt"; "reflect")
type Attr struct{ Key string; Val int }
f := func(a Attr) Attr { a.Key = "sev"; a.Val++; return a }
out := reflect.ValueOf(f).Call([]reflect.Value{reflect.ValueOf(Attr{Key: "level", Val: 41})})
r := out[0].Interface().(Attr)
fmt.Sprintf("%s:%d", r.Key, r.Val)`, res: "sev:42"},

		{n: "reflect_assert_iface_panicform", src: `
import "reflect"
type T struct{}
func (t T) Hello() string { return "hi" }
type Speaker interface{ Hello() string }
reflect.ValueOf(T{}).Interface().(Speaker).Hello()`, res: "hi"},

		{n: "reflect_assert_iface_ptr_receiver", src: `
import "reflect"
type T struct{ n int }
func (t *T) Inc() int { t.n++; return t.n }
type Inc interface{ Inc() int }
func f() int { x := reflect.ValueOf(&T{5}).Interface(); if v, ok := x.(Inc); ok { return v.Inc() }; return -1 }
f()`, res: "6"},

		{n: "reflect_typeswitch_iface", src: `
import "reflect"
type T struct{}
func (t T) Hello() string { return "hi" }
type Speaker interface{ Hello() string }
func f() string { x := reflect.ValueOf(T{}).Interface(); switch v := x.(type) { case Speaker: return v.Hello(); default: return "def" } }
f()`, res: "hi"},

		{n: "reflect_assert_iface_negative", src: `
import "reflect"
type T struct{}
func (t T) Hello() string { return "hi" }
type Missing interface{ Nope() int }
func f() bool { x := reflect.ValueOf(T{}).Interface(); _, ok := x.(Missing); return ok }
f()`, res: "false"},

		{n: "reflect_assert_concrete_from_any", src: `
import "reflect"
type T struct{ x int }
func f() int { x := reflect.ValueOf(T{7}).Interface(); t, ok := x.(T); if !ok { return -1 }; return t.x }
f()`, res: "7"},

		{n: "reflect_zero_iface_assert", src: `
import "reflect"
type G struct{}
func (g G) Generate() int { return 42 }
type Generator interface{ Generate() int }
func f() int { if g, ok := reflect.Zero(reflect.TypeOf(G{})).Interface().(Generator); ok { return g.Generate() }; return -1 }
f()`, res: "42"},

		{n: "reflect_rtype_collision_no_crossdispatch", src: `
import "reflect"
type Celsius float64
func (c Celsius) Tag() string { return "C" }
type Fahrenheit float64
func (f Fahrenheit) Tag() string { return "F" }
type Tagger interface{ Tag() string }
func f() string {
	cv := reflect.ValueOf(Celsius(0)).Interface()
	fv := reflect.ValueOf(Fahrenheit(0)).Interface()
	ct, cok := cv.(Tagger)
	ft, fok := fv.(Tagger)
	if cok && ct.Tag() == "F" { return "BUG-C" }
	if fok && ft.Tag() == "C" { return "BUG-F" }
	return "safe"
}
f()`, res: "safe"},

		{n: "reflect_defined_primitive_recovers", src: `
import "reflect"
type Celsius float64
func (c Celsius) Tag() string { return "C" }
type Tagger interface{ Tag() string }
func f() string { cv := reflect.ValueOf(Celsius(0)).Interface(); if t, ok := cv.(Tagger); ok { return t.Tag() }; return "no" }
f()`, res: "C"},

		// An inline named-INT conversion T(1) takes the runtime Convert path now
		// (not the const fold), so it keeps its rtype and reflects its methods.
		{n: "named_int_const_inline_keeps_methods", src: `
import "reflect"
type T int
func (t T) String() string { return "s" }
reflect.TypeOf(T(1)).NumMethod()`, res: "1"},

		// Named-int const identifier and arithmetic keep the named rtype.
		{n: "named_int_const_ident_keeps_methods", src: `
import "reflect"
type T int
func (t T) String() string { return "s" }
const G T = 1
reflect.TypeOf(G).NumMethod()`, res: "1"},
		{n: "named_int_const_arith_keeps_methods", src: `
import "reflect"
type T int
func (t T) String() string { return "s" }
reflect.TypeOf(T(1) + T(2)).NumMethod()`, res: "1"},

		// make([]T, n) for a method-bearing (synth-rtype) named T must build the
		// slice from the canonical element type, not a pre-synth-attach
		// placeholder snapshot, so the result stays assignable to the []T slot.
		// Regression: x/text TestRemoteXTextLanguageImport ("[]struct{PTag_N int}
		// not assignable to []language.Tag").
		{n: "make_named_slice_keeps_synth_elem", src: `
type Tag struct{ id int }
func (t Tag) String() string { return "s" }
func f() int { s := make([]Tag, 3); return len(s) }
f()`, res: "3"},

		// sort.Sort(ByAge(people)) pattern: named slice type with methods over a
		// synth element; the conversion needs ByAge's element to match []Person's.
		{n: "named_slice_type_conv_keeps_synth_elem", src: `
import "reflect"
type Person struct{ Age int }
func (p Person) String() string { return "p" }
type ByAge []Person
func (a ByAge) Len() int { return len(a) }
func f() bool {
	people := []Person{{1}, {2}}
	b := ByAge(people)
	return reflect.TypeOf(b).Elem() == reflect.TypeOf(people).Elem() && reflect.TypeOf(b).NumMethod() == 1
}
f()`, res: "true"},

		// encoding/json/xml `make(map[Animal]int)` pattern: named-key map round-trip.
		{n: "make_named_map_key_keeps_synth", src: `
type Animal int
func (a Animal) String() string { return "x" }
func f() int {
	m := make(map[Animal]int)
	m[Animal(1)] += 10
	m[Animal(2)] = 20
	delete(m, Animal(2))
	return m[Animal(1)] + len(m)
}
f()`, res: "11"},

		// make(map[K]T) values keep T's method-bearing synth rtype.
		{n: "make_named_map_value_keeps_synth_elem", src: `
import "reflect"
type Tag struct{ id int }
func (t Tag) String() string { return "s" }
var m map[string]Tag
func f() int { m = make(map[string]Tag); return reflect.TypeOf(m).Elem().NumMethod() }
f()`, res: "1"},

		{n: "reflect_struct_sibling_no_crossdispatch", src: `
import "reflect"
type X struct{ n int }
func (x X) Tag() string { return "X" }
type Y X
func (y Y) Tag() string { return "Y" }
type Tagger interface{ Tag() string }
func f() string {
	xv := reflect.ValueOf(X{}).Interface()
	yv := reflect.ValueOf(Y{}).Interface()
	xt, xok := xv.(Tagger)
	yt, yok := yv.(Tagger)
	if xok && xt.Tag() == "Y" { return "BUG-X" }
	if yok && yt.Tag() == "X" { return "BUG-Y" }
	return "safe"
}
f()`, res: "safe"},

		{n: "ptr_recv_value_not_impl_mvmiface", src: `
type T struct{ n int }
func (t *T) Inc() int { t.n++; return t.n }
type Inc interface{ Inc() int }
func f() bool { var x interface{} = T{5}; _, ok := x.(Inc); return ok }
f()`, res: "false"},

		{n: "ptr_recv_value_not_impl_reflect", src: `
import "reflect"
type T struct{ n int }
func (t *T) Inc() int { t.n++; return t.n }
type Inc interface{ Inc() int }
func f() bool { x := reflect.ValueOf(T{5}).Interface(); _, ok := x.(Inc); return ok }
f()`, res: "false"},

		{n: "ptr_recv_pointer_impl_mvmiface", src: `
type T struct{ n int }
func (t *T) Inc() int { t.n++; return t.n }
type Inc interface{ Inc() int }
func f() int { var x interface{} = &T{5}; if v, ok := x.(Inc); ok { return v.Inc() }; return -1 }
f()`, res: "6"},

		{n: "reflect_closure_in0_iface_assert", src: `
import "reflect"
type G struct{}
func (g G) Generate() int { return 42 }
type Generator interface{ Generate() int }
func check(f interface{}) int { if g, ok := reflect.Zero(reflect.TypeOf(f).In(0)).Interface().(Generator); ok { return g.Generate() }; return -1 }
func run() int { fn := func(g G) bool { return true }; return check(fn) }
run()`, res: "42"},

		{n: "reflect_closure_out0_iface_assert", src: `
import "reflect"
type G struct{}
func (g G) Generate() int { return 7 }
type Generator interface{ Generate() int }
func check(f interface{}) int { if g, ok := reflect.Zero(reflect.TypeOf(f).Out(0)).Interface().(Generator); ok { return g.Generate() }; return -1 }
func run() int { fn := func() G { return G{} }; return check(fn) }
run()`, res: "7"},

		{n: "reflect_typeassert_iface", src: `
import "reflect"
type G struct{}
func (g G) Generate() int { return 42 }
type Generator interface{ Generate() int }
func f() int { g, ok := reflect.TypeAssert[Generator](reflect.Zero(reflect.TypeOf(G{}))); if ok { return g.Generate() }; return -1 }
f()`, res: "42"},

		{n: "reflect_typeassert_closure_in0", src: `
import "reflect"
type G struct{}
func (g G) Generate() int { return 42 }
type Generator interface{ Generate() int }
func check(f interface{}) int { g, ok := reflect.TypeAssert[Generator](reflect.Zero(reflect.TypeOf(f).In(0))); if ok { return g.Generate() }; return -1 }
func run() int { fn := func(g G) bool { return true }; return check(fn) }
run()`, res: "42"},

		{n: "reflect_typeassert_concrete", src: `
import "reflect"
func f() int { n, ok := reflect.TypeAssert[int](reflect.ValueOf(7)); if ok { return n }; return -1 }
f()`, res: "7"},

		{n: "reflect_typeassert_nomatch", src: `
import "reflect"
type G struct{}
type Generator interface{ Generate() int }
func f() bool { _, ok := reflect.TypeAssert[Generator](reflect.Zero(reflect.TypeOf(G{}))); return ok }
f()`, res: "false"},

		{n: "embed_with_methods", src: `
import "bytes"
type Buf struct {
	*bytes.Buffer
	size int
}
b := &Buf{Buffer: bytes.NewBufferString("hello"), size: 5}
b.size`, res: "5"},

		{n: "struct_eq_iface_field", src: `
type fullTag interface{ Marker() }
type Inner struct{ a uint16; b string }
func (Inner) Marker() {}
type Tag struct{ lang uint16; full fullTag }
mk := func(s string) Tag { return Tag{lang: 2, full: Inner{a: 5, b: s}} }
t1 := mk("af-Arab")
t2 := mk("af-Arab")
t1 == t2`, res: "true"},

		{n: "struct_eq_iface_field_diff", src: `
type fullTag interface{ Marker() }
type Inner struct{ a uint16; b string }
func (Inner) Marker() {}
type Tag struct{ lang uint16; full fullTag }
mk := func(s string) Tag { return Tag{lang: 2, full: Inner{a: 5, b: s}} }
mk("af-Arab") == mk("es-419")`, res: "false"},

		{n: "struct_param_field_write_no_leak", src: `
type T struct{ A, B int }
func f(t T) { t.A = 99 }
t := T{A: 1, B: 2}
f(t)
t.A`, res: "1"},

		{n: "array_param_index_write_no_leak", src: `
func f(a [3]int) { a[0] = 99 }
a := [3]int{1, 2, 3}
f(a)
a[0]`, res: "1"},

		{n: "value_recv_method_no_leak", src: `
type T struct{ A int }
func (t T) Set() { t.A = 99 }
t := T{A: 1}
t.Set()
t.A`, res: "1"},
	})
}

func TestRecursiveStruct(t *testing.T) {
	run(t, []etest{
		{n: "linked_list", src: `
type Node struct { V int; Next *Node }
a := &Node{V: 1, Next: &Node{V: 2, Next: &Node{V: 3}}}
a.Next.Next.V`, res: "3"},

		{n: "nil_field", src: `
type Node struct { V int; Next *Node }
var n Node
n.Next == nil`, res: "true"},

		{n: "binary_tree", src: `
type Tree struct { V int; Left, Right *Tree }
t := &Tree{V: 1, Left: &Tree{V: 2}, Right: &Tree{V: 3}}
t.Left.V + t.Right.V`, res: "5"},

		{n: "assign_field", src: `
type Node struct { V int; Next *Node }
n := Node{V: 1}
n.Next = &Node{V: 42}
n.Next.V`, res: "42"},

		{n: "mutual_recurse", src: `
type F func(a *A)
type A struct { Name string; F }
a := &A{"hello", func(a *A) {}}
a.Name`, res: "hello"},

		{n: "slice_field_index", src: `
type Node struct { Name string; Child []*Node }
n := &Node{Name: "parent"}
n.Child = append(n.Child, &Node{Name: "child"})
n.Child[0].Name`, res: "child"},

		{n: "value_order", src: `
type A struct { B; X int }
type B struct { Y int }
a := A{B: B{Y: 10}, X: 20}
a.Y + a.X`, res: "30"},

		{n: "mutual_map_append", src: `
type S struct { ts map[string][]*T }
type T struct { s *S }
func (c *S) getT(addr string) (t *T, ok bool) {
	cns, ok := c.ts[addr]
	if !ok || len(cns) == 0 {
		return nil, false
	}
	t = cns[len(cns)-1]
	c.ts[addr] = cns[:len(cns)-1]
	return t, true
}
s := &S{ts: map[string][]*T{}}
s.ts["k"] = append(s.ts["k"], &T{s: s})
t, ok := s.getT("k")
t != nil && ok`, res: "true"},
	})
}

func TestEmbeddedStruct(t *testing.T) {
	run(t, []etest{
		{n: "field", src: `
type Base struct { X int }
type T struct { Base; Y int }
var t T; t.X = 1; t.Y = 2; t.X`, res: "1"},

		{n: "literal", src: `
type Base struct { X int }
type T struct { Base; Y int }
t := T{Base{10}, 20}; t.X`, res: "10"},

		{n: "method", src: `
type Base struct { X int }
func (b Base) GetX() int { return b.X }
type T struct { Base; Y int }
t := T{Base{7}, 0}; t.GetX()`, res: "7"},

		// Func-local struct embedding a method-bearing struct: the embedded field
		// clone must resolve to the canonical Base identity (the clone carries the
		// method, so it must not reserve a distinct "Base" rtype).
		{n: "func_local_embed_method", src: `
type Base struct { X int }
func (b Base) GetX() int { return b.X }
f := func() int {
	type T struct { Base; Y int }
	t := T{Base{7}, 3}
	return t.GetX() + t.Y
}
f()`, res: "10"},

		{n: "iface", src: `
type Getter interface { GetX() int }
type Base struct { X int }
func (b Base) GetX() int { return b.X }
type T struct { Base }
var g Getter = T{Base{42}}
g.GetX()`, res: "42"},

		{n: "override", src: `
type Base struct { X int }
func (b Base) GetX() int { return b.X }
type T struct { Base }
func (t T) GetX() int { return t.X * 10 }
t := T{Base{3}}; t.GetX()`, res: "30"},

		{n: "nested", src: `
type A struct { V int }
type B struct { A }
type C struct { B }
c := C{B{A{99}}}; c.V`, res: "99"},

		{n: "ptr_field", src: `
type Base struct { X int }
type T struct { *Base }
t := T{&Base{5}}; t.X`, res: "5"},

		{n: "ptr_set", src: `
type Base struct { X int }
type T struct { *Base }
t := T{&Base{0}}; t.X = 42; t.X`, res: "42"},

		{n: "ptr_method", src: `
type Base struct { X int }
func (b Base) GetX() int { return b.X }
type T struct { *Base }
t := T{&Base{8}}; t.GetX()`, res: "8"},

		{n: "ptr_recv_method", src: `
type Base struct { X int }
func (b *Base) SetX(v int) { b.X = v }
type T struct { *Base }
t := T{&Base{0}}; t.SetX(99); t.X`, res: "99"},

		{n: "ptr_iface", src: `
type Getter interface { GetX() int }
type Base struct { X int }
func (b *Base) GetX() int { return b.X }
type T struct { *Base }
var g Getter = T{&Base{55}}
g.GetX()`, res: "55"},

		{n: "ptr_nested", src: `
type A struct { V int }
type B struct { *A }
type C struct { B }
c := C{B{&A{77}}}; c.V`, res: "77"},

		{n: "embed_iface", src: `
type Transformer interface { Reset() string }
type Encoder struct { Transformer }
type nop struct{}
func (nop) Reset() string { return "ok" }
func f(e Transformer) string { return e.Reset() }
e := Encoder{Transformer: nop{}}
f(e)`, res: "ok"},

		{n: "struct_tag", src: `
type Users []string
type Config struct {
	Users        ` + "`" + `json:"users,omitempty"` + "`" + `
	UsersFile    string ` + "`" + `json:"usersFile,omitempty"` + "`" + `
	RemoveHeader bool   ` + "`" + `json:"removeHeader,omitempty"` + "`" + `
}
c := &Config{}
c.RemoveHeader`, res: "false"},

		// Package-qualified embedded field with promoted native method.
		{n: "pkg_embed", src: `import "time"
type MyTime struct { time.Time; index int }
t := MyTime{}
t.Time = time.Date(2009, time.November, 10, 23, 4, 5, 0, time.UTC)
t.Minute()`, res: "4"},

		{n: "pkg_embed_ptr_recv", src: `import "time"
type MyTime struct { time.Time }
t := MyTime{}
t.Time = time.Date(2009, time.November, 10, 23, 4, 5, 0, time.UTC)
t.Second()`, res: "5"},

		{n: "embedded_iface_method_via_ptr", src: `
import "fmt"
type withMessage struct { cause error; msg string }
func (w *withMessage) Error() string { return w.msg + ": " + w.cause.Error() }
type withStack struct { error }
func wrap(err error, msg string) error {
	return &withStack{&withMessage{cause: err, msg: msg}}
}
e := wrap(fmt.Errorf("boom"), "oops")
e.Error()`, res: "oops: boom"},

		{n: "method_value_from_unexp_field", src: `
type T struct { count int }
func (t T) Get() int { return t.count }
type wrap struct { inner T }
w := wrap{T{5}}
fn := w.inner.Get
fn()`, res: "5"},
	})
}

func TestMethodResolve(t *testing.T) {
	run(t, []etest{
		{n: "val_on_val", src: `
type T struct { X int }
func (t T) GetX() int { return t.X }
v := T{3}; v.GetX()`, res: "3"},

		{n: "val_on_ptr", src: `
type T struct { X int }
func (t T) GetX() int { return t.X }
p := &T{5}; p.GetX()`, res: "5"},

		{n: "ptr_on_ptr", src: `
type T struct { X int }
func (t *T) SetX(v int) { t.X = v }
p := &T{0}; p.SetX(7); p.X`, res: "7"},

		{n: "ptr_on_val", src: `
type T struct { X int }
func (t *T) SetX(v int) { t.X = v }
var v T; v.SetX(9); v.X`, res: "9"},

		{n: "both", src: `
type T struct { X int }
func (t T) GetX() int { return t.X }
func (t *T) Double() { t.X = t.X * 2 }
var v T; v.X = 4; v.Double(); v.GetX()`, res: "8"},

		// Symbolic defined basic types: a method must resolve via the var/const's
		// named *Type, not the underlying kind (global vars used to retype wrong).
		{n: "defined_basic_global_var_method", src: `
type Grams int
func (g Grams) Double() Grams { return g * 2 }
var w Grams = 7
int(w.Double())`, res: "14"},

		{n: "defined_basic_const_method", src: `
type Grams int
func (g Grams) Double() Grams { return g * 2 }
const c Grams = 5
int(c.Double())`, res: "10"},

		{n: "defined_basic_global_var_noinit_method", src: `
type Grams int
func (g Grams) Double() Grams { return g * 2 }
var w Grams
func f() int { w = 6; return int(w.Double()) }
f()`, res: "12"},

		{n: "iface_val_recv", src: `
type Getter interface { GetX() int }
type T struct { X int }
func (t T) GetX() int { return t.X }
var g Getter = &T{6}
g.GetX()`, res: "6"},

		{n: "iface_ptr_recv", src: `
type Setter interface { SetX(int) }
type T struct { X int }
func (t T) GetX() int { return t.X }
func (t *T) SetX(v int) { t.X = v }
var t0 = &T{0}
var s Setter = t0
s.SetX(11)
t0.GetX()`, res: "11"},

		{n: "named_val_on_val", src: `
type N int
func (n N) IsPos() bool { return int(n) > 0 }
v := N(5); v.IsPos()`, res: "true"},

		{n: "named_val_on_ptr", src: `
type N int
func (n N) IsPos() bool { return int(n) > 0 }
p := new(N); *p = N(3); p.IsPos()`, res: "true"},

		{n: "named_ptr_on_val", src: `
type N int
func (n *N) Inc() { *n = *n + 1 }
var v N = 10; v.Inc(); v`, res: "11"},

		{n: "local_var", src: `
type T struct { X int }
func (t T) GetX() int { return t.X }
func f() int { v := T{42}; return v.GetX() }
f()`, res: "42"},

		{n: "field_access", src: `
type Coord struct { x, y int }
func (c Coord) dist() int { return c.x*c.x + c.y*c.y }
type Point struct { Coord; z int }
o := Point{Coord{3, 4}, 5}
o.Coord.dist()`, res: "25"},

		{n: "slice_elem", src: `
type S struct { X int }
func (s S) GetX() int { return s.X }
a := []S{S{7}, S{9}}
a[0].GetX()`, res: "7"},

		{n: "field_chain_method", src: `
type A struct { B string; C D }
func (a *A) Test() string { return "test" }
type D struct { E *A }
a := &A{B: "b"}
d := D{E: a}
a.C = d
a.C.E.Test()`, res: "test"},
	})
}

func TestMap(t *testing.T) {
	src0 := `type M map[string]bool;`
	run(t, []etest{
		{n: "#00", src: src0 + `var m M; m`, res: `map[]`},
		{n: "#01", src: `m := map[string]bool{"foo": true}; m["foo"]`, res: `true`},
		{n: "#02", src: src0 + `m := M{"xx": true}; m`, res: `map[xx:true]`},
		{n: "#03", src: src0 + `var m = M{"xx": true}; m`, res: `map[xx:true]`},
		{n: "#04", src: src0 + `var m = M{"xx": true}; m["xx"] = false; m`, res: `map[xx:false]`},
		{n: "#05", src: "var m map[string]int64; func f() {m = make(map[string]int64)}; f(); len(m)", res: "0"},
		{n: "ptr_elem", src: `type T struct{v int}; m := map[int]*T{0: {v: 2}}; m[0].v`, res: "2"},
		{n: "iface_elem", src: `type I interface { Foo() int }; type S1 struct { i int }; func (s S1) Foo() int { return s.i }; type S2 struct{}; func (s *S2) Foo() int { return 42 }; Is := map[string]I{"foo": S1{21}, "bar": &S2{}}; n := 0; for _, s := range Is { n += s.Foo() }; n`, res: "63"},
		{n: "iface_addr_lit", src: `type I interface { Foo() int }; type S struct{}; func (s *S) Foo() int { return 7 }; m := map[string]I{"k": &S{}}; m["k"].Foo()`, res: "7"},
		{n: "append_missing_key", src: `m := map[string][]int{}; m["x"] = append(m["x"], 1); m["x"][0]`, res: "1"},
		{n: "slice_val_lit", src: `m := map[string][]string{"a": []string{"x", "y"}}; m["a"][1]`, res: "y"},
		{n: "nested_range", src: `import "sort"; m := map[string][]string{"a": []string{"1", "2"}, "b": []string{"3"}}; var r []string; for k, vs := range m { for _, v := range vs { r = append(r, k+v) } }; sort.Strings(r); r`, res: "[a1 a2 b3]"},
		{n: "func_val_ret", src: `func f(s string) string { return "hi " + s }; m := map[string]func(string) string{"f": f}; m["f"]("x")`, res: "hi x"},
		// composite-literal map keys: array-typed keys, explicit and elided type,
		// and a var-init map with negative values (mirrors x/text grandfatheredMap).
		{n: "array_key_explicit", src: `m := map[[3]byte]int{[3]byte{'a', 'b', 'c'}: 1, [3]byte{'d', 'e', 'f'}: 2}; m[[3]byte{'d', 'e', 'f'}]`, res: "2"},
		{n: "array_key_elided", src: `m := map[[3]byte]int{{'a', 'b', 'c'}: 1, {'d', 'e', 'f'}: 2}; len(m)*10 + m[[3]byte{'a', 'b', 'c'}]`, res: "21"},
		{n: "array_key_var_neg", src: `var m = map[[2]int]int{{1, 2}: -7, {3, 4}: 5}; m[[2]int{1, 2}]`, res: "-7"},
		{n: "array_key_string_val", src: `m := map[[2]int]string{{1, 2}: "x", {3, 4}: "y"}; m[[2]int{3, 4}]`, res: "y"},
		{n: "native_iface_key", src: `import "errors"; m := map[error]int{}; var e error = errors.New("x"); m[e] = 7; v := m[e]; delete(m, e); v*10 + len(m)`, res: "70"},
	})
}

func TestSlice(t *testing.T) {
	src0 := `s := []int{0, 1, 2, 3};`
	run(t, []etest{
		{n: "#00", src: src0 + `s`, res: `[0 1 2 3]`},
		{n: "#01", src: src0 + `s[:]`, res: `[0 1 2 3]`},
		{n: "#02", src: src0 + `s[1:3]`, res: `[1 2]`},
		{n: "#03", src: src0 + `s[1:3:4]`, res: `[1 2]`},
		{n: "#04", src: src0 + `s[:3:4]`, res: `[0 1 2]`},
		{n: "#05", src: src0 + `s[:2:]`, err: `final index required in 3-index slice`},
		{n: "#06", src: src0 + `s[:3:4:]`, err: `expected ']', found ':'`},
		{n: "#07", src: src0 + `s[2:]`, res: `[2 3]`},
		{n: "#08", src: src0 + `s[:0]`, res: `[]`},
		{n: "#09", src: `"Hello"[1:3]`, res: `el`},
		{n: "#10", src: `s := "Hello"; s[1:3]`, res: `el`},
		{n: "#11", src: src0 + `z := s[1:3]; z`, res: `[1 2]`},
		{n: "#12", src: `s := "Hello"; z := s[1:3]; z`, res: `el`},

		{n: "spread_iface_slice", src: `import "errors"
			errs := []error{errors.New("a"), errors.New("b")}
			r := append([]error{}, errs...); len(r)`, res: "2"},

		{n: "variadic_iface_slice", src: `type E struct{ msg string }
			func (e *E) Error() string { return e.msg }
			f := func(errs ...error) int { return len(errs) }
			f(&E{msg: "x"})`, res: "1"},

		{n: "nil_iface_in_slice", src: `var e error; r := append([]error{}, e); len(r)`, res: "1"},

		{n: "nested_composite_same_type", src: `import "errors"
			type E struct{ Errors []error }
			func (e *E) Error() string { return "x" }
			x := []error{errors.New("one"), &E{Errors: []error{errors.New("two")}}}
			len(x)`, res: "2"},
	})
}

func TestType(t *testing.T) {
	src0 := `type (
	I int
	S string
)
`
	run(t, []etest{
		{n: "#00", src: "type t int; var a t = 1; a", res: "1"},
		{n: "#01", src: "type t = int; var a t = 1; a", res: "1"},
		{n: "#02", src: src0 + `var s S = "xx"; s`, res: "xx"},
		{n: "group_forward_ref", src: `
type (
	One struct { Two Two }
	Two string
)
var o One = One{Two: "hi"}; string(o.Two)`, res: "hi"},

		{n: "named_arith", src: "type t int; var a, b t = 3, 4; a + b", res: "7"},
		{n: "named_conv", src: "type t int; t(42)", res: "42"},
		{n: "named_method", src: "type t int; func (v t) Double() int { return int(v) * 2 }; var a t = 5; a.Double()", res: "10"},
		{n: "const_len", src: `
type t1 uint8
const (
	n1 t1 = iota
	n2
)
type T struct { elem [n2 + 1]int }
len(T{}.elem)`, res: "2"},

		{n: "alias", src: "type Number = int; Number(1) < int(2)", res: "true"},
		{n: "local_shadow", src: `
type T struct { X int }
func f() int {
	type T struct { Y string }
	var v T
	v.Y = "hello"
	return len(v.Y)
}
f()`, res: "5"},
		{n: "local_shadow_outer", src: `
type T struct { X int }
func f() { type T struct { Y string }; var v T; v.Y = "ok" }
f()
var t T
t.X = 99
t.X`, res: "99"},

		{n: "var_shadows_own_type", src: `
type T struct { x int }
func f() int { T := &T{42}; return T.x }
f()`, res: "42"},

		{n: "block_scope", src: `
func f() int {
	a := 1
	{ a := 3; _ = a }
	return a
}
f()`, res: "1"},

		{n: "block_scope_nested", src: `
func f() int {
	a := 1
	{ a := 2; { a := 3; _ = a }; _ = a }
	return a
}
f()`, res: "1"},

		{n: "param_shadows_pkg", src: `
import "time"
func test(time string, t time.Time) string { return time }
test("ok", time.Now())`, res: "ok"},

		{n: "field_shadows_type", src: `
type P struct { pos uint8; size uint8 }
type buf struct { rune [3]P }
len(buf{}.rune)`, res: "3"},

		{n: "local_alias_composite", src: `
type original struct { Field string }
func f() string {
	type alias original
	type alias2 alias
	a := &alias2{Field: "test"}
	return a.Field
}
f()`, res: "test"},

		{n: "leading_comment_in_block", src: "type (\n\t// header comment\n\tA int\n\tB int\n)\nvar a A = 1; var b B = 2; int(a) + int(b)", res: "3"},
	})
}

func TestInterface(t *testing.T) {
	run(t, []etest{
		{n: "basic", src: `
type Stringer interface { String() string }
type T int
func (t T) String() string { return "hello" }
var s Stringer = T(1)
s.String()`, res: "hello"},
		// Boxing copies the value (Go spec): reassigning a local map/slice var
		// after it was stored in an interface must not change the boxed copy
		// (IfaceWrap snapshots a settable slot, not just struct/array).
		{n: "box_map_snapshot", src: `func f() int { x := map[string]int{"a": 1}; var i any = x; x = map[string]int{"b": 2, "c": 3}; _ = x; return len(i.(map[string]int)) }; f()`, res: "1"},
		{n: "box_slice_snapshot", src: `func f() int { x := []int{1}; var i any = x; x = []int{2, 3}; _ = x; return len(i.([]int)) }; f()`, res: "1"},
		{n: "box_range_append", src: `import "fmt"; func f() string { v := []map[string]int{{"a": 1}, {"b": 2}}; var s []any; for _, u := range v { s = append(s, u) }; return fmt.Sprint(s) }; f()`, res: "[map[a:1] map[b:2]]"},
		// Numerics skip the IfaceWrap clone (payload lives in Value.num);
		// the snapshot semantics must still hold.
		{n: "box_int_snapshot", src: `func f() int { x := 1; var i any = x; x = 2; _ = x; return i.(int) }; f()`, res: "1"},
		{n: "box_string_snapshot", src: `func f() string { x := "a"; var i any = x; x = "b"; _ = x; return i.(string) }; f()`, res: "a"},
		{n: "box_named_int_method", src: `type N int; func (n N) Val() int { return int(n) }; type V interface{ Val() int }; func f() int { x := N(1); var v V = x; x = N(2); _ = x; return v.Val() }; f()`, res: "1"},

		{n: "embed", src: `
type Fooer interface { Foo() string }
type Barer interface {
	Fooer
	Bar() string
}
type T struct{}
func (t *T) Foo() string { return "foo" }
func (t *T) Bar() string { return "bar" }
var b Barer = &T{}
b.Foo() + b.Bar()`, res: "foobar"},

		{n: "embed_builtin", src: `
type Error interface {
	error
	Message() string
}
type T struct{ Msg string }
func (t *T) Error() string   { return t.Msg }
func (t *T) Message() string { return "msg:" + t.Msg }
func newError() Error { return &T{"test"} }
e := newError()
e.Error()`, res: "test"},

		{n: "recv_value", src: `
type Doubler interface { Double() int }
type N int
func (n N) Double() int { return int(n) * 2 }
var d Doubler = N(5)
d.Double()`, res: "10"},

		{n: "named_int_error_method", src: `
import "fmt"
type T int
func (t T) Error() string { return fmt.Sprintf("v=%d", t) }
var a T = 5
a.Error()`, res: "v=5"},

		{n: "reassign", src: `
type Doubler interface { Double() int }
type N int
func (n N) Double() int { return int(n) * 2 }
var d Doubler = N(3)
d = N(7)
d.Double()`, res: "14"},

		{n: "empty_iface", src: "type I interface {}; var x I; x", res: "<nil>"},
		{n: "self_ref_iface", src: `type Edge interface { ReverseEdge() Edge }; println("ok")`, res: "ok"},
		{n: "mutual_ref_iface", src: `type A interface { Foo() B }; type B interface { Bar() A }; println("ok")`, res: "ok"},

		{n: "struct_recv", src: `
type Getter interface { Get() int }
type S struct { n int }
func (s S) Get() int { return s.n }
var g Getter = S{42}
g.Get()`, res: "42"},

		{n: "iface_method", src: `
type I interface { inI() }
type T struct {name string}
func (t *T) inI() {}
var i I = &T{name: "foo"}
var r = ""
if i, ok := i.(*T); ok { r = i.name }
r`, res: "foo"},

		{n: "any_set", src: "var a interface{} = 2 + 5; a.(int)", res: "7"},
		{n: "iface_return", src: `func f(x int) interface{} {return x}; f(42).(int)`, res: "42"},
		{n: "iface_return_cap", src: `func f(a []int) interface{} {return cap(a)}; a := []int{1, 2, 3}; f(a).(int)`, res: "3"},
		{n: "iface_return_multi", src: `func f(x int) (interface{}, int) {return x, x + 1}; a, b := f(5); a.(int) + b`, res: "11"},

		{n: "iface_return_method", src: `
type I interface { A() string }
type s struct{}
func NewS() (I, error) { return &s{}, nil }
func (c *s) A() string { return "a" }
v, _ := NewS()
v.A()`, res: "a"},

		{n: "iface_struct_field_method", src: `
type Enabler interface { Enabled() bool }
type Logger struct { core Enabler }
func (log *Logger) GetCore() Enabler { return log.core }
type T struct{}
func (t *T) Enabled() bool { return true }
base := &Logger{&T{}}
base.GetCore().Enabled()`, res: "true"},

		{n: "error_nil_shortcircuit", src: `
var a error = nil
r := ""
if a == nil || a.Error() == "nil" { r = "nil" }
r`, res: "nil"},

		{n: "explicit_iface_conv", src: `
type myInterface interface { myFunc() string }
type V struct{}
func (v *V) myFunc() string { return "hello" }
type U struct { v myInterface }
func (u *U) myFunc() string { return u.v.myFunc() }
x := V{}
y := myInterface(&x)
y = &U{y}
y.myFunc()`, res: "hello"},

		{n: "sort_iface", src: `
import "sort"
type byLen []string
func (b byLen) Len() int           { return len(b) }
func (b byLen) Less(i, j int) bool { return len(b[i]) < len(b[j]) }
func (b byLen) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }
s := byLen{"bbb", "a", "cc"}
sort.Sort(s)
s[0] + " " + s[1] + " " + s[2]`, res: "a cc bbb"},

		{n: "heap_iface", src: `
import "container/heap"
type IntHeap []int
func (h IntHeap) Len() int           { return len(h) }
func (h IntHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h IntHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *IntHeap) Push(x interface{}) { *h = append(*h, x.(int)) }
func (h *IntHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}
h := &IntHeap{5, 3, 1}
heap.Init(h)
heap.Push(h, 2)
r := 0
for h.Len() > 0 { r = r*10 + heap.Pop(h).(int) }
r`, res: "1235"},

		{n: "sort_iface_ptr", src: `
import "sort"
type S struct { vals []int }
func (s *S) Len() int           { return len(s.vals) }
func (s *S) Less(i, j int) bool { return s.vals[i] < s.vals[j] }
func (s *S) Swap(i, j int)      { s.vals[i], s.vals[j] = s.vals[j], s.vals[i] }
s := &S{vals: []int{3, 1, 2}}
sort.Sort(s)
s.vals[0]*100 + s.vals[1]*10 + s.vals[2]`, res: "123"},

		{n: "flag_value_bridge", src: `
import "flag"
type myVal struct{ s string }
func (v *myVal) String() string     { return v.s }
func (v *myVal) Set(s string) error { v.s = s; return nil }
v := &myVal{s: "init"}
fs := flag.NewFlagSet("test", flag.ContinueOnError)
fs.Var(v, "myflag", "usage")
fs.Set("myflag", "updated")
v.String()`, res: "updated"},

		{n: "sort_slice", src: `
import "sort"
s := []int{3, 1, 4, 1, 5}
sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
s[0]*10000 + s[1]*1000 + s[2]*100 + s[3]*10 + s[4]`, res: "11345"},

		{n: "callback_global_write_visible", src: `
import "strings"
var counter int
func bump(r rune) rune {
	counter++
	return r
}
strings.Map(bump, "abc")
counter`, res: "3"},

		{n: "native_iface_conv", src: `
import "io"
type T struct { r io.Reader }
func (t *T) Read(p []byte) (int, error) { return t.r.Read(p) }
x := io.LimitedReader{}
y := io.Reader(&x)
y = &T{y}
n, _ := y.Read([]byte(""))
n`, res: "0"},

		{n: "writer_assert", src: `
import "fmt"
import "io"
type T []byte
func (t *T) Write(p []byte) (n int, err error) { *t = append(*t, p...); return len(p), nil }
func foo(w io.Writer) string {
	a := w.(*T)
	fmt.Fprint(a, "test")
	return fmt.Sprintf("%s", *a)
}
x := T{}
foo(&x)`, res: "test"},

		{n: "append_iface_slice", src: `
import "io"
type B []byte
func (b B) Write(p []byte) (int, error) { return len(p), nil }
b := B{}
a := make([]io.Writer, 0)
a = append(a, b)
len(a)`, res: "1"},

		{n: "embed_pkg_iface", src: `
import "io"
type Transport interface { io.Reader }
type T struct{}
func (t *T) Read(p []byte) (int, error) { return 0, nil }
var tr Transport = &T{}
_, err := tr.Read(nil)
err == nil`, res: "true"},

		{n: "embed_pkg_iface_multi", src: `
import "io"
type RW interface {
	io.Reader
	io.Writer
}
type T struct{}
func (t *T) Read(p []byte) (int, error) { return 0, nil }
func (t *T) Write(p []byte) (int, error) { return len(p), nil }
var rw RW = &T{}
n, _ := rw.Write([]byte("hi"))
n`, res: "2"},

		{n: "embed_native_iface_native_type", src: `
import (
	"io"
	"os"
)
type sink interface {
	io.Writer
	io.Closer
}
func run() int {
	file, err := os.CreateTemp("", "mvm-test.*")
	if err != nil { panic(err) }
	var s sink = file
	n, err := s.Write([]byte("Hello\n"))
	if err != nil { panic(err) }
	var w io.Writer = s
	m, err := w.Write([]byte("Hello\n"))
	if err != nil { panic(err) }
	var c io.Closer = s
	err = c.Close()
	if err != nil { panic(err) }
	err = os.Remove(s.(*os.File).Name())
	if err != nil { panic(err) }
	return m*10 + n
}
run()`, res: "66"},

		{n: "embed_binary_iface_call", src: `
import (
	"io"
	"os"
)
type myWriter struct {
	io.Writer
}
func main() int {
	w := &myWriter{os.Stdout}
	var iw io.Writer = w
	n, _ := iw.Write([]byte("hello\n"))
	return n
}
main()`, res: "6"},

		{n: "embed_iface_method_call", src: `
type I2 interface { F() string }
type S2 struct { I2 }
type S3 struct { base *S2 }
type T struct{ name string }
func (t *T) F() string { return "hello " + t.name }
t := &T{"world"}
s2 := &S2{t}
s3 := &S3{s2}
s2.F() + " " + s3.base.F()`, res: "hello world hello world"},

		{n: "embed_iface_method_assign", src: `
import "net/http"
import "net/http/httptest"
import "fmt"
import "io"
type T struct { http.ResponseWriter }
type mw struct{}
func (m *mw) ServeHTTP(rw http.ResponseWriter, rq *http.Request) {
	t := &T{ResponseWriter: rw}
	x := t.Header()
	fmt.Fprint(rw, "ok", x)
}
mux := http.NewServeMux()
mux.HandleFunc("/", (&mw{}).ServeHTTP)
server := httptest.NewServer(mux)
resp, _ := http.Get(server.URL)
body, _ := io.ReadAll(resp.Body)
server.Close()
string(body)`, res: "okmap[]"},

		{n: "struct_field_iface_native", src: `
import "io"
type myReader struct{ data string; done bool }
func (r *myReader) Read(p []byte) (int, error) {
	if r.done { return 0, io.EOF }
	n := copy(p, r.data)
	r.done = true
	return n, io.EOF
}
type wrapper struct{ R *myReader }
w := wrapper{R: &myReader{data: "hello"}}
b, _ := io.ReadAll(w.R)
string(b)`, res: "hello"},

		{n: "iface_slice_spread_both", src: `
type Option interface { val() int }
type T struct{ v int }
func (t *T) val() int { return t.v }
func f(opts ...Option) int { return opts[0].val() }
opt := []Option{&T{v: 21}}
a := f(opt[0])
b := f(opt...)
a + b`, res: "42"},

		{n: "map_func_value_native", src: `
m := map[string]interface{}{
	"double": func(s string) string { return s + s },
}
f := m["double"].(func(string) string)
f("hello")`, res: "hellohello"},

		{n: "map_nil_interface_value", src: `
import "fmt"
var errs = map[int]error{0: nil}
fmt.Sprint(errs)`, res: "map[0:<nil>]"},
	})
}

func TestTypeAssert(t *testing.T) {
	run(t, []etest{
		{n: "simple", src: `var i any = 42; i.(int)`, res: "42"},
		{n: "string", src: `var i any = "hello"; i.(string)`, res: "hello"},
		{n: "arith", src: `var i any = 42; i.(int) + 1`, res: "43"},

		{n: "ok_true", src: `var i any = 42; v, ok := i.(int); ok`, res: "true"},
		{n: "ok_val", src: `var i any = 42; v, ok := i.(int); v + 1`, res: "43"},

		{n: "ok_false", src: `var i any = 42; _, ok := i.(string); ok`, res: "false"},

		{n: "iface_assert", src: `
type Getter interface { Get() int }
type S struct { n int }
func (s S) Get() int { return s.n }
var g Getter = S{7}
v, ok := g.(S)
v.Get()`, res: "7"},

		{n: "iface_assert_ok", src: `
type Getter interface { Get() int }
type S struct { n int }
func (s S) Get() int { return s.n }
var g Getter = S{7}
_, ok := g.(S)
ok`, res: "true"},

		{n: "iface_assert_fail", src: `
type Getter interface { Get() int }
type Other struct { n int }
type S struct { n int }
func (s S) Get() int { return s.n }
var g Getter = S{7}
_, ok := g.(Other)
ok`, res: "false"},

		{n: "iface_to_iface", src: `
type Root struct { Name string }
type One struct { Root }
type Hi interface { Hello() string }
type Hey interface { Hello() string }
func (r *Root) Hello() string { return "Hello " + r.Name }
var one Hey = &One{Root{Name: "test2"}}
one.(Hi).Hello()`, res: "Hello test2"},

		{n: "iface_to_iface_ok", src: `
type Root struct { Name string }
type One struct { Root }
type Hi interface { Hello() string }
type Hey interface { Hello() string }
func (r *Root) Hello() string { return "Hello " + r.Name }
var one Hey = &One{Root{Name: "test2"}}
_, ok := one.(Hi)
ok`, res: "true"},

		{n: "iface_to_iface_fail", src: `
type S struct{}
type A interface { Foo() }
type B interface { Bar() }
func (s S) Foo() {}
var a A = S{}
_, ok := a.(B)
ok`, res: "false"},

		{
			n: "nil_panic", src: `var i any; i.(int)`,
			err: "interface conversion: interface {} is nil, not int",
		},

		{n: "nil_recover", src: `
r := 0
func f() {
	defer func() { recover(); r = 1 }()
	var i any
	i.(int)
}
f()
r`, res: "1"},

		{
			n: "wrong_type_panic", src: `var i any = "hello"; i.(int)`,
			err: "interface conversion: interface {} is string, not int",
		},

		{n: "wrong_type_recover", src: `
r := 0
func f() {
	defer func() { recover(); r = 1 }()
	var i any = "hello"
	i.(int)
}
f()
r`, res: "1"},

		{n: "int64_return", src: `
func f1(a int) interface{} { return a + 1 }
func f2(a int64) interface{} { return a + 1 }
v1 := f1(3).(int)
v2 := f2(3).(int64)
v1 + int(v2)`, res: "8"},

		{n: "iface_field_same_name_as_type", src: `
type Pooler interface { Get() string }
type baseClient struct { connPool Pooler }
type connPool struct { name string }
func (c *connPool) Get() string { return c.name }
func newBaseClient(p Pooler) *baseClient { return &baseClient{connPool: p} }
func newConnPool() *connPool { return &connPool{name: "connPool"} }
b := newBaseClient(newConnPool())
b.connPool.(*connPool).name`, res: "connPool"},

		{n: "iface_struct_field_assert", src: `
import "io"
import "strings"
type rac struct { r io.ReadCloser }
a := rac{io.NopCloser(strings.NewReader("test"))}
_, ok := a.r.(io.Closer)
ok`, res: "true"},

		{n: "iface_assert_wrong_iface", src: `
import "io"
import "strings"
func f(r io.Reader) bool { _, ok := r.(io.Closer); return ok }
f(strings.NewReader("test"))`, res: "false"},

		{n: "native_iface_to_wrong_struct", src: `
import "errors"
type T struct { P1 int }
var i error = errors.New("boom")
v, ok := i.(T)
println(v); ok`, res: "false"},

		{n: "any_iface_to_wrong_struct", src: `
import "errors"
type T struct { P1 int }
var i interface{} = errors.New("boom")
_, ok := i.(T)
ok`, res: "false"},

		{n: "user_iface_native_err_no_match", src: `
import "errors"
type causer interface { Cause() error }
e := errors.New("test")
_, ok := e.(causer)
ok`, res: "false"},

		{n: "user_iface_ptr_recv_match", src: `
type stringer interface { String() string }
type W struct { s string }
func (w *W) String() string { return w.s }
var s stringer = &W{s: "ok"}
_, ok := s.(stringer)
ok`, res: "true"},
	})
}

func TestTypeSwitch(t *testing.T) {
	run(t, []etest{
		{n: "no_bind_int", src: `var i any = 42; var r int; switch i.(type) { case int: r = 1 }; r`, res: "1"},
		{n: "no_bind_str", src: `var i any = "hi"; var r int; switch i.(type) { case int: r = 1; case string: r = 2 }; r`, res: "2"},
		{n: "no_bind_default", src: `var i any = true; var r int; switch i.(type) { case int: r = 1; default: r = 9 }; r`, res: "9"},

		{n: "bind_int", src: `var i any = 42; switch v := i.(type) { case int: v + 1 }`, res: "43"},
		{n: "bind_str", src: `var i any = "hi"; switch v := i.(type) { case string: v }`, res: "hi"},
		{n: "bind_second", src: `var i any = "hi"; switch v := i.(type) { case int: v; case string: v }`, res: "hi"},
		{n: "bind_default", src: `var i any = true; var r int; switch i.(type) { case int: r = 1; default: r = 9 }; r`, res: "9"},

		{n: "multi_int", src: `var i any = 42; var r int; switch i.(type) { case int, string: r = 1; default: r = 2 }; r`, res: "1"},
		{n: "multi_str", src: `var i any = "hi"; var r int; switch i.(type) { case int, string: r = 1; default: r = 2 }; r`, res: "1"},
		{n: "multi_default", src: `var i any = true; var r int; switch i.(type) { case int, string: r = 1; default: r = 2 }; r`, res: "2"},

		{n: "nil_match", src: `var i any; var r int; switch i.(type) { case nil: r = 1; default: r = 2 }; r`, res: "1"},
		{n: "nil_no_match", src: `var i any = 42; var r int; switch i.(type) { case nil: r = 1; default: r = 2 }; r`, res: "2"},

		{n: "iface_guard", src: `
type Getter interface { Get() int }
type S struct { n int }
func (s S) Get() int { return s.n }
var g Getter = S{7}
switch v := g.(type) { case S: v.Get() }`, res: "7"},

		{n: "iface_case_nobind", src: `
type T struct{}
func (t T) Hello() string { return "hi" }
type Speaker interface{ Hello() string }
var x interface{} = T{}
var r int
switch x.(type) { case Speaker: r = 1; default: r = 2 }
r`, res: "1"},

		{n: "iface_case_multitype", src: `
type T struct{}
func (t T) Hello() string { return "hi" }
type Speaker interface{ Hello() string }
type Other interface{ Nope() int }
var x interface{} = T{}
var r int
switch x.(type) { case Other, Speaker: r = 1; default: r = 2 }
r`, res: "1"},

		{n: "iface_case_nomatch", src: `
type T struct{}
func (t T) Hello() string { return "hi" }
type Other interface{ Nope() int }
var x interface{} = T{}
var r int
switch x.(type) { case Other: r = 1; default: r = 2 }
r`, res: "2"},

		{n: "iface_case_reflect_nobind", src: `
import "reflect"
type T struct{}
func (t T) Hello() string { return "hi" }
type Speaker interface{ Hello() string }
func f() int { x := reflect.ValueOf(T{}).Interface(); switch x.(type) { case Speaker: return 1; default: return 2 } }
f()`, res: "1"},

		{n: "ptr_type", src: `
type T struct{ N int }
func f(t interface{}) int {
	switch t.(type) { case *T: return 1; default: return 2 }
}
f(&T{})`, res: "1"},

		{n: "ptr_bind", src: `
type T struct{ N int }
func f(t interface{}) int {
	switch v := t.(type) { case *T: return v.N; default: return -1 }
}
f(&T{42})`, res: "42"},

		{n: "variadic_iface_str", src: `
func f(params ...interface{}) int {
	switch params[0].(type) { case string: return 1; default: return 2 }
}
f("hello")`, res: "1"},

		{n: "variadic_iface_bind", src: `
func f(params ...interface{}) string {
	switch v := params[0].(type) { case string: return v; default: return "no" }
}
f("world")`, res: "world"},

		{n: "variadic_iface_default", src: `
func f(params ...interface{}) int {
	switch params[0].(type) { case string: return 1; default: return 2 }
}
f(99)`, res: "2"},

		{n: "native_iface", src: `
import (
	"io"
	"os"
)
func f(i interface{}) string {
	switch i.(type) {
	case int, int8:
		return "integer"
	case io.Reader:
		return "reader"
	}
	return "other"
}
var fd *os.File
var r io.Reader = fd
f(r)`, res: "reader"},

		{n: "iface_slice_index", src: `
type Option interface { val() int }
type T struct{ v int }
func (t *T) val() int { return t.v }
opt := []Option{&T{v: 7}}
opt[0].val()`, res: "7"},

		{n: "iface_spread", src: `
type Option interface { val() int }
type T struct{ v int }
func (t *T) val() int { return t.v }
func f(opts ...Option) int { return opts[0].val() }
opt := []Option{&T{v: 42}}
f(opt...)`, res: "42"},

		{n: "native_map_typeswitch_str", src: `
import "encoding/json"
var m map[string]interface{}
json.Unmarshal([]byte(` + "`" + `{"a":"hello"}` + "`" + `), &m)
switch m["a"].(type) { case string: "ok"; default: "fail" }`, res: "ok"},

		{n: "native_map_typeswitch_nil", src: `
import "encoding/json"
var m map[string]interface{}
json.Unmarshal([]byte(` + "`" + `{"a":null}` + "`" + `), &m)
switch m["a"].(type) { case nil: "ok"; default: "fail" }`, res: "ok"},

		{n: "generic_ptr_writeback", src: `
func F[T int | string](x T) T {
	switch v := any(&x).(type) {
	case *int: *v = 999
	case *string: *v = "Z"
	}
	return x
}
F(42)`, res: "999"},

		{n: "iface_slice_struct_aliased_rtype", src: `
type Variant struct{ s string }
type Extension struct{ s string }
v := Variant{s: "rozaj"}
e := Extension{s: "u-va-posix"}
p := []interface{}{v, e}
var r string
for _, x := range p {
	switch y := x.(type) {
	case Variant:
		r += "V:" + y.s + ";"
	case Extension:
		r += "E:" + y.s + ";"
	}
}
r`, res: "V:rozaj;E:u-va-posix;"},

		{n: "iface_slice_int_aliased_rtype", src: `
type Foo int
type Bar int
p := []interface{}{Foo(1), Bar(2)}
var r string
for _, x := range p {
	switch y := x.(type) {
	case Foo:
		r += "F" + string(rune('0'+int(y))) + ";"
	case Bar:
		r += "B" + string(rune('0'+int(y))) + ";"
	}
}
r`, res: "F1;B2;"},

		{n: "iface_append_methodful_keeps_identity", src: `
import "fmt"
type V struct{ s string }
type E struct{ s string }
func (v V) String() string { return "V:" + v.s }
func (e E) String() string { return "E:" + e.s }
var p []interface{}
p = append(p, V{s: "x"})
p = append(p, E{s: "y"})
var r string
for _, x := range p {
	switch y := x.(type) {
	case V:
		r += "v:" + y.s + ";"
	case E:
		r += "e:" + y.s + ";"
	}
}
fmt.Sprint(p[0]) + "|" + fmt.Sprint(p[1]) + "|" + r`, res: "V:x|E:y|v:x;e:y;"},

		{n: "iface_typeswitch_bridge_unwrap", src: `
import "fmt"
type Err struct{ X int }
func (e *Err) Error() string { return "err" }
func makeErr() *Err { return &Err{X: 42} }
func collect(errs ...error) string {
	for _, e := range errs {
		switch v := e.(type) {
		case *Err:
			return fmt.Sprintf("OK %d", v.X)
		default:
			return fmt.Sprintf("MISS %T", v)
		}
	}
	return ""
}
collect(makeErr())`, res: "OK 42"},
	})
}

func TestVar(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "var a int; a", res: "0"},
		{n: "#01", src: "var a, b, c int; a", res: "0"},
		{n: "#02", src: "var a, b, c int; a + b", res: "0"},
		{n: "#03", src: "var a, b, c int; a + b + c", res: "0"},
		{n: "#04", src: "var a int = 2+1; a", res: "3"},
		{n: "#05", src: "var a, b int = 2, 5; a+b", res: "7"},
		{n: "#06", src: "var x = 5; x", res: "5"},
		{n: "#07", src: "var a = 1; func f() int { var a, b int = 3, 4; return a+b}; a+f()", res: "8"},
		{n: "#08", src: `var a = "hello"; a`, res: "hello"},
		{n: "#09", src: `var ( a, b int = 4+1, 3; c = 8); a+b+c`, res: "16"},
		{n: "#10", src: "var (\n\tx = 1\n\n\t// stray comment\n\ty = 2\n)\nx+y", res: "3"},
	})
}

func TestImport(t *testing.T) {
	src0 := `import (
	"fmt"
)
`
	run(t, []etest{
		{n: "#00", src: "fmt.Println(4)", err: "undefined: fmt"},
		{n: "#01", src: `import "xxx"`, err: "stat xxx: no such file or directory"},
		{n: "#02", src: `import "fmt"; fmt.Println(4)`, res: "<nil>"},
		{n: "#03", src: src0 + "fmt.Println(4)", res: "<nil>"},
		{n: "#04", src: `func main() {import "fmt"; fmt.Println("hello")}`, err: "unexpected import"},
		{n: "#05", src: `import m "fmt"; m.Println(4)`, res: "<nil>"},
		{n: "#06", src: `import . "fmt"; Println(4)`, res: "<nil>"},
		{n: "#07", src: `import "context"; func get(ctx context.Context) string { return "ok" }; get(context.Background())`, res: "ok"},
		{n: "#08", src: `import "context"; ctx := context.WithValue(context.Background(), "a", "b"); context.WithValue(ctx, "c", "d")`, res: "context.Background.WithValue(a, b).WithValue(c, d)"},
		{n: "#09", src: `import "context"; ctx := context.WithValue(context.Background(), "a", "b"); ctx.Value("a").(string)`, res: "b"},
		{n: "#10", src: `import "strings"; r := strings.NewReader("hello"); r.Len()`, res: "5"},
		{n: "#11", src: `import "os"; f, _ := os.CreateTemp("", "test"); n := f.Name(); f.Close(); os.Remove(n); len(n) > 0`, res: "true"},
		{n: "#12", src: `import "bytes"; b := bytes.NewBuffer(nil); b.WriteString("hello"); b.String()`, res: "hello"},
		{n: "#13", src: `import "net/url"; v := url.Values{}; v.Set("a", "b"); v.Get("a")`, res: "b"},
		{n: "#14", src: `import . "net"; v := IP{}; v`, res: "<nil>"},

		{n: "#15", src: "import (\n\t// header comment\n\t\"strings\"\n)\nstrings.ToUpper(\"ok\")", res: "OK"},
	})
}

func TestComposite(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: "type T struct{}; t := T{}; t", res: "{}"},
		{n: "#01", src: "t := struct{}{}; t", res: "{}"},
		{n: "#02", src: `type T struct {}; var t T; t = T{}; t`, res: "{}"},
		{n: "#03", src: `type T struct{N int; S string}; var t T; t = T{2, "foo"}; t`, res: `{2 foo}`},
		{n: "#04", src: `type T struct{N int; S string}; t := T{2, "foo"}; t`, res: `{2 foo}`},
		{n: "#05", src: `type T struct{N int; S string}; t := T{S: "foo"}; t`, res: `{0 foo}`},
		{n: "#06", src: `a := []int{}`, res: `[]`},
		{n: "#07", src: `a := []int{1, 2, 3}; a`, res: `[1 2 3]`},
		{n: "#08", src: `m := map[string]bool{}`, res: `map[]`},
		{n: "#09", src: `m := map[string]bool{"hello": true}; m`, res: `map[hello:true]`},
		{n: "#10", src: `m := map[int]struct{b bool}{1:struct {b bool}{true}}; m`, res: `map[1:{true}]`},
		{n: "#11", src: `type T struct {b bool}; m := []T{T{true}}; m`, res: `[{true}]`},
		{n: "#12", src: `type T struct {b bool}; m := []T{{true}}; m`, res: `[{true}]`},
		{n: "#13", src: `m := []struct{b bool}{{true}}; m`, res: `[{true}]`},
		{n: "#14", src: `m := map[int]struct{b bool}{1:{true}}; m`, res: `map[1:{true}]`},
		{n: "#15", src: `type T *struct {b bool}; m := []T{{true}}; m[0]`, res: `&{true}`},
		{n: "#16", src: `type T *struct {b bool}; m := []T{{true}}; m[0].b`, res: `true`},
		{n: "#17", src: `a := [3]int{1, 2, 3}; a`, res: `[1 2 3]`},
		{n: "#18", src: `import "time"; t := time.Time{}; t.IsZero()`, res: `true`},
		{n: "#19", src: `import "time"; t := &time.Time{}; t.IsZero()`, res: `true`},
		{n: "#20", src: `import "fmt"; type T struct{name string}; fmt.Sprintf("%v", []interface{}{T{"hello"}})`, res: `[{hello}]`},
		// Inline closure as composite literal field value.
		{n: "#21", src: `type T struct{ F func() int }; s := T{F: func() int { return 42 }}; s.F()`, res: `42`},
		// Comment-only line between struct fields inside a slice literal.
		// Regression: scanner used to insert an auto-Semicolon after the
		// comment, which leaked into parseExpr (pkg/errors errors_test.go).
		{n: "#22", src: "type T struct{ a, b int }\n" +
			"s := []T{{\n" +
			"\t// leading comment\n" +
			"\ta: 1,\n" +
			"\tb: 2,\n" +
			"}, {\n" +
			"\t// another comment\n" +
			"\ta: 3,\n" +
			"\tb: 4,\n" +
			"}}\n" +
			"s[0].a + s[1].b", res: `5`},
		// Parenthesized type conversion as a struct-literal field value.
		// Regression: parseExpr treated `(error)` after `field:` as a
		// function call (since Colon is not an operator), turning
		// `field: (error)(nil)` into call(call(field, error), nil).
		// pkg/errors errors_test.go uses `err: (error)(nil)`.
		{n: "#23", src: `type T struct{ e error }; t := T{e: (error)(nil)}; t.e == nil`, res: "true"},
	})
}

func TestClosure(t *testing.T) {
	run(t, []etest{
		// Reading outer scope (module-level) variable.
		{n: "#00", src: `a := 10; f := func() int { return a }; f()`, res: "10"},
		// Mutating outer scope variable.
		{n: "#01", src: `a := 5; f := func() { a = 20 }; f(); a`, res: "20"},
		// Closure with own params, also captures outer var.
		{n: "#02", src: `x := 3; f := func(n int) int { return x + n }; f(4)`, res: "7"},
		// Closure returned from anonymous func (inner captures global).
		{n: "#03", src: `a := 1; makeInc := func() func() int { return func() int { a = a+1; return a } }; inc := makeInc(); inc(); inc()`, res: "3"},
		// Closure stored as var then called.
		{n: "#04", src: `var f func(int) int; f = func(n int) int { return n*2 }; f(6)`, res: "12"},
		// Two closures sharing the same outer var.
		{n: "#05", src: `n := 0; inc := func() { n = n+1 }; get := func() int { return n }; inc(); inc(); get()`, res: "2"},
		// Closure capturing param of enclosing named func.
		{n: "#06", src: `func makeAdder(x int) func(int) int { return func(n int) int { return x + n } }; add5 := makeAdder(5); add5(3)`, res: "8"},
		// Counter pattern: closure captures and mutates enclosing local.
		{n: "#07", src: `func makeCounter() func() int { n := 0; return func() int { n = n+1; return n } }; c := makeCounter(); c(); c()`, res: "2"},
		// Per-iteration capture: each closure in a loop captures its own snapshot of the loop
		// variable (no aliasing to the shared frame slot).
		{n: "#08", src: `func f() int { var fns []func() int; for i := 0; i < 3; i++ { a := i; fns = append(fns, func() int { return i*10 + a }) }; return fns[0]() + fns[1]() + fns[2]() }; f()`, res: "33"},
		// Closure in struct func field appended to slice: funcFields keyed by address must
		// survive the struct copy that append does. All three closures must see their own i/a.
		{n: "#09", src: `
type T struct{ F func() int }
func f() int {
	var foos []T
	for i := 0; i < 3; i++ {
		a := i
		foos = append(foos, T{func() int { return i*10 + a }})
	}
	return foos[0].F() + foos[1].F() + foos[2].F()
}
f()`, res: "33"},
		// Closures in for-range-int loop each capture their own snapshot.
		{n: "#10", src: `
func f() int {
	var foos []func() int
	for i := range 3 {
		a := i
		foos = append(foos, func() int { return i*10 + a })
	}
	return foos[0]() + foos[1]() + foos[2]()
}
f()`, res: "33"},
		// Closures stored in []func() slice called via range iteration (yaegi-issue-1594).
		{n: "#11", src: `
func f() int {
	var fns []func() int
	for _, v := range []int{1, 2, 3} {
		x := v*100 + v
		fns = append(fns, func() int { return x })
	}
	result := 0
	for _, fn := range fns {
		result += fn()
	}
	return result
}
f()`, res: "606"},
		// Closure sees variable modified after capture (capture by reference).
		{n: "#12", src: `func f() int { i := 12; g := func() int { return i }; i = 20; return g() }; f()`, res: "20"},
		// Two closures share same cell inside a function.
		{n: "#13", src: `func f() int { n := 0; inc := func() { n = n+1 }; get := func() int { return n }; inc(); inc(); inc(); return get() }; f()`, res: "3"},
		// Closure captures shadowed loop variable (not the post-increment loop var).
		{n: "#14", src: `func f() int { foos := []func() int{}; for i := 0; i < 3; i++ { i := i; foos = append(foos, func() int { return i }) }; return foos[0]() + foos[1]()*10 + foos[2]()*100 }; f()`, res: "210"},
		// Transitive capture across multiple closure levels: innermost closure references
		// a variable defined two levels up. Without propagating the free var through the
		// intermediate closure, the compiler resolves the var against the wrong frame at
		// MkClosure and the heap slot holds a non-*Value, panicking the assertion. Mirrors
		// the structure of x/text/language TestCompliance's filepath.Walk -> ucd.Parse ->
		// t.Run nesting that captures the Walk callback's `err` from inside t.Run's body.
		{n: "transitive_capture_3level", src: `
func f() int {
	x := 100
	outer := func(a int) int {
		mid := func(b int) int {
			inner := func(c int) int {
				return x + a + b + c
			}
			return inner(3)
		}
		return mid(2)
	}
	return outer(1)
}
f()`, res: "106"},
		// Transitive capture where intermediate closure does NOT itself reference the
		// outer variable. Stresses propagateCapture: the intermediate's FreeVars must
		// pick up `x` even though its body never names `x` directly.
		{n: "transitive_capture_pass_through", src: `
func f() int {
	x := 7
	makeMid := func() func() int {
		return func() int { return x }
	}
	return makeMid()()
}
f()`, res: "7"},
		// Per-iteration snapshot for non-numeric range variables. HeapAlloc
		// must detach addressable string/slice/interface refs from the loop
		// slot so each closure captures its own value; regression flattened
		// all captures to the last iteration's value.
		{n: "range_string_snapshot", src: `
func f() string {
	vals := []string{"a", "b"}
	var out string
	fns := []func(){}
	for _, v := range vals {
		fns = append(fns, func() { out += v })
	}
	for _, fn := range fns {
		fn()
	}
	return out
}
f()`, res: "ab"},
		// Same shape with an interface-typed range variable: the surface
		// symptom in go-multierror's TestGroup.
		{n: "range_iface_snapshot", src: `
func f() string {
	vals := []any{"x", "y"}
	var out string
	fns := []func(){}
	for _, v := range vals {
		fns = append(fns, func() {
			s, _ := v.(string)
			out += s
		})
	}
	for _, fn := range fns {
		fn()
	}
	return out
}
f()`, res: "xy"},
		// A captured slice var reset to nil then re-appended through the
		// closure: setting the heap cell to a bare nil must preserve the
		// cell's slice type, else the next append reads a zero reflect.Value
		// (zerolog TestLevelHook). Without the fix this panics in append.
		{n: "captured_slice_nil_reassign_append", src: `
func f() int {
	var xs []int
	add := func(v int) { xs = append(xs, v) }
	add(1)
	xs = nil
	add(2)
	add(3)
	return len(xs)*100 + xs[0]*10 + xs[1]
}
f()`, res: "223"},
		// Captured var reset to nil then written through &var: the cell must
		// keep addressable storage or the write is lost (fmt TestHexBytes).
		{n: "captured_nil_reassign_addr_write", src: `
func f() int {
	var b []byte
	g := func() { _ = b }
	_ = g
	b = nil
	pb := &b
	*pb = []byte{1, 2, 3}
	return len(b)
}
f()`, res: "3"},
		// SKIP (known gap): &var taken BEFORE the reassignment aliases the
		// cell's old storage; see setCell.
		{n: "captured_addr_before_nil_reassign", skip: true, src: `
func f() int {
	var b []byte
	g := func() { _ = b }
	_ = g
	pb := &b
	b = nil
	*pb = []byte{1, 2, 3}
	return len(b)
}
f()`, res: "3"},
		// Same through a native writer: Sscanf fills b via *[]byte.
		{n: "captured_nil_reassign_scan", src: `
import "fmt"
func f() int {
	var b []byte
	g := func() { _ = b }
	_ = g
	b = nil
	fmt.Sscanf("00010203", "%x", &b)
	return len(b)
}
f()`, res: "4"},
	})
}

func TestMethod(t *testing.T) {
	run(t, []etest{
		// Value receiver, direct call.
		{n: "#00", src: `type I int; func(i I) F(a int) int { return a+int(i) }; var i I = 1; i.F(2)`, res: "3"},
		// Multiple params.
		{n: "#01", src: `type I int; func(i I) Add(a, b int) int { return a + b }; var i I = 0; i.Add(3, 4)`, res: "7"},

		// Read single field.
		{n: "#02", src: `type T struct{n int}; func(t T) N() int { return t.n }; x := T{5}; x.N()`, res: "5"},
		// Read field, add param.
		{n: "#03", src: `type T struct{n int}; func(t T) Add(a int) int { return t.n + a }; x := T{3}; x.Add(4)`, res: "7"},
		// Two fields.
		{n: "#04", src: `type T struct{a, b int}; func(t T) Sum() int { return t.a + t.b }; x := T{2, 3}; x.Sum()`, res: "5"},

		// Store method value, call later.
		{n: "#05", src: `type I int; func(i I) F(a int) int { return a+int(i) }; var i I = 2; f := i.F; f(3)`, res: "5"},
		// Two independent method values from different receivers.
		{n: "#06", src: `type I int; func(i I) Val() I { return i }; var a I = 1; var b I = 2; fa := a.Val; fb := b.Val; fa() + fb()`, res: "3"},
		// Pass method value to higher-order function.
		{n: "#07", src: `type I int; func(i I) F(a int) int { return a+int(i) }; apply := func(f func(int) int, n int) int { return f(n) }; var i I = 5; apply(i.F, 3)`, res: "8"},
		// Method value on struct receiver.
		{n: "#08", src: `type T struct{n int}; func(t T) Add(a int) int { return t.n + a }; x := T{3}; f := x.Add; f(4)`, res: "7"},

		// Pointer receiver increments field.
		{n: "#09", src: `type T struct{n int}; func(t *T) Inc() { t.n = t.n + 1 }; var x T; x.Inc(); x.Inc(); x.n`, res: "2"},
		// Pointer receiver method value.
		{n: "#10", src: `type T struct{n int}; func(t *T) Inc() { t.n = t.n + 1 }; var x T; f := x.Inc; f(); f(); x.n`, res: "2"},

		// Method returning a closure that captures the receiver.
		{n: "#11", src: `type T struct{n int}; func(t T) Adder() func(int) int { return func(a int) int { return t.n + a } }; x := T{3}; add := x.Adder(); add(4)`, res: "7"},

		// Native method on named numeric type from expression or function return.
		{n: "native_named_expr", src: `import "time"; (10 * time.Hour).String()`, res: "10h0m0s"},
		{n: "native_named_ret", src: `import "time"; func f() time.Duration { return 10 * time.Hour }; f().String()`, res: "10h0m0s"},
		{n: "const_named_type", src: `import "time"; const d = time.Minute * 30; d.String()`, res: "30m0s"},
		{n: "ptr_type_conv", src: `import "time"; type durationValue time.Duration; func (d *durationValue) String() string { return (*time.Duration)(d).String() }; var d durationValue; d.String()`, res: "0s"},
		// Arithmetic result boxed straight into interface{} keeps its named type:
		// 300*time.Millisecond is a time.Duration, not a bare int64.
		{n: "arith_named_iface_type", src: `import ("fmt"; "time"); fmt.Sprintf("%T", 300*time.Millisecond)`, res: "time.Duration"},
		{n: "arith_named_iface_val", src: `import ("fmt"; "time"); fmt.Sprint(1*time.Hour + 2*time.Minute + 300*time.Millisecond)`, res: "1h2m0.3s"},

		// Method expression: value receiver.
		{n: "mexpr_val", src: `type T struct{n int}; func(t T) Add(a int) int { return t.n + a }; T.Add(T{3}, 4)`, res: "7"},
		// reflect.Value.NumMethod/Method on an interpreted FUNC-typed value: the
		// synth rtype must carry the method (reserve/fill over a func layout).
		{n: "reflect_method_on_func_type", src: `import "reflect"; type Fn func() int; func(fn Fn) String() string { return "s" }; func f() int { var v Fn; return reflect.ValueOf(v).NumMethod() }; f()`, res: "1"},
		// Method expression: pointer receiver.
		{n: "mexpr_ptr", src: `type T struct{n int}; func(t *T) Get() int { return t.n }; (*T).Get(&T{n: 5})`, res: "5"},
		// Native method expression as a stored/passed VALUE: `(*big.Int).Add` is
		// lowered to its reflect Method.Func (Go's method-expression func value,
		// receiver first), so it works stored and passed to a helper that calls it.
		// This is the math/big TestAliasing shape (it passes (*big.Int).Or to
		// checkAliasingTwoArgs, which calls f(v, x, y)). See [[project_native_method_expression_arity]].
		{n: "mexpr_native_stored", src: `import "math/big"; func f() string { g := (*big.Int).Add; return g(big.NewInt(1), big.NewInt(2), big.NewInt(3)).String() }; f()`, res: "5"},
		{n: "mexpr_native_passed", src: `import "math/big"; func apply(f func(*big.Int, *big.Int, *big.Int) *big.Int, x, y, z *big.Int) *big.Int { return f(x, y, z) }; func run() string { return apply((*big.Int).Add, big.NewInt(1), big.NewInt(2), big.NewInt(3)).String() }; run()`, res: "5"},
		// Native method expression called DIRECTLY also works (Kind:Value -> value-call path).
		{n: "mexpr_native_direct", src: `import "math/big"; func f() string { return (*big.Int).Add(big.NewInt(1), big.NewInt(2), big.NewInt(3)).String() }; f()`, res: "5"},
		// An INTERPRETED VALUE-receiver method expression stored/passed as a value
		// works via a receiver-binding adapter (MkMethodExpr): the func binds its
		// first arg as the receiver and runs the method body on a pooled runner.
		// See [[project_native_method_expression_arity]].
		{n: "mexpr_val_stored", src: `type T struct{n int}; func(t T) Add(a int) int { return t.n + a }; func f() int { g := T.Add; return g(T{3}, 4) }; f()`, res: "7"},
		{n: "mexpr_val_passed", src: `type T struct{n int}; func(t T) Add(a, b int) int { return t.n + a + b }; func apply(f func(T, int, int) int) int { return f(T{10}, 2, 3) }; apply(T.Add)`, res: "15"},
		// Value-receiver method expression as a composite-literal element (the fmt
		// fmtTests shape that crashed: reflect.Value.Field on a misaligned stack).
		{n: "mexpr_val_composite", src: `type G int; func(g G) S() string { return "s" }; func f() int { t := []struct{ v any }{{G.S}}; return len(t) }; f()`, res: "1"},
		// POINTER-receiver method expression stored/passed (MkMethodExpr now
		// covers matching pointer-ness; the x/text/cases titleCaser shape).
		{n: "mexpr_ptr_stored", src: `type T struct{n int}; func(t *T) Add(a, b int) int { return t.n + a + b }; func f() int { g := (*T).Add; return g(&T{10}, 2, 3) }; f()`, res: "15"},
		// Pointer-receiver method expression as a func-typed struct-field value.
		{n: "mexpr_ptr_composite", src: `type T struct{n int}; func(t *T) Get() int { return t.n }; type C struct{ f func(*T) int }; func f() int { c := C{f: (*T).Get}; return c.f(&T{7}) }; f()`, res: "7"},
		// SKIP (still broken): the MIXED form (*T).M where M has a VALUE receiver
		// (legal Go: derefs the pointer); needs a deref in the MkMethodExpr wrapper.
		{n: "mexpr_ptr_on_val_stored", skip: true, src: `type T struct{n int}; func(t T) Add(a, b int) int { return t.n + a + b }; func f() int { g := (*T).Add; return g(&T{10}, 2, 3) }; f()`, res: "15"},

		// Method call on composite literal.
		{n: "comp_lit", src: `type T struct{n int}; func(t T) N() int { return t.n }; T{5}.N()`, res: "5"},
		// Method call on composite literal with promoted method.
		{n: "comp_lit_promoted", src: `type Foo struct{}; func (Foo) Call() string { return "Foo" }; type Bar struct{ Foo }; Bar{}.Call()`, res: "Foo"},

		// Method on named function type.
		{n: "named_func_method", src: `type F func(); func(f F) Run() string { return "ok" }; f := F(func(){}); f.Run()`, res: "ok"},
		// Named function type calling self.
		{n: "named_func_call_self", src: `type F func(); func(f F) Run() { f() }; var s string; F(func(){ s = "hello" }).Run(); s`, res: "hello"},

		// Native interface field holding interpreted func via named func type.
		{n: "native_iface_func", src: `import "net/http"; import "net/http/httptest"
			type T struct { next http.Handler }
			func(t *T) Do() { t.next.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)) }
			var s string
			f := http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) { s = "done" })
			t := &T{f}; t.Do(); s`, res: "done"},

		// Native method on a named func type carrying an interpreted func.
		// Regression (gorilla/mux ServeHTTP nil deref): the func value stayed a
		// bare mvm func ref, so the receiver for the native method was garbage.
		// Three forms: conversion-then-call, var declaration, and boxed return.
		{n: "named_func_native_method_conv", src: `import "net/http"; import "net/http/httptest"
			var s string
			f := func(rw http.ResponseWriter, req *http.Request) { s = "ok" }
			http.HandlerFunc(f).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)); s`, res: "ok"},
		{n: "named_func_native_method_var", src: `import "net/http"; import "net/http/httptest"
			var s string
			var h http.HandlerFunc = func(rw http.ResponseWriter, req *http.Request) { s = "ok" }
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)); s`, res: "ok"},
		{n: "named_func_native_method_boxed", src: `import "net/http"; import "net/http/httptest"
			var s string
			mk := func() http.Handler { return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) { s = "ok" }) }
			h := mk(); h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)); s`, res: "ok"},
		// Converting a nil func to a methodful native named func type must stay
		// nil-comparable: WrapFunc keeps the zero func, not a (non-nil) MvmFunc box.
		{n: "named_func_nil_conv_compare", src: `import "net/http"
			var f http.HandlerFunc
			http.HandlerFunc(f) == nil`, res: "true"},

		// Defined type whose underlying is a basic type, accessed through a
		// struct field, then chain-called. Regression: vm.Type.FieldLookup
		// overwrote the field-type Name with reflect's f.Type.Name() (which
		// returns "uintptr" for `type Frame uintptr`), losing the user-level
		// name needed to find methods on Frame. The fix preserves ft.Base.Name
		// (back-link to the original named vm.Type) when set.
		// Surfaces in pkg/errors json_test.go: `tt.Frame.MarshalText()`.
		{n: "named_type_method_via_field_chain", src: `type Frame uintptr
			func (f Frame) Tag() string { return "ok" }
			type T struct{ F Frame }
			tt := T{F: Frame(0)}
			tt.F.Tag()`, res: `ok`},
		// Same shape via an embedded field. parseEmbeddedField now also sets
		// ft.Base so FieldLookup can recover the named type.
		{n: "named_type_method_via_embedded_field", src: `type Frame uintptr
			func (f Frame) Tag() string { return "ok" }
			type T struct{ Frame; want string }
			tt := T{Frame(0), "x"}
			tt.Frame.Tag()`, res: `ok`},

		// Heterogeneous []error literal mixing native errors and an mvm-defined
		// type with embedded `error`. Regression: setFuncField fell through to
		// fv.Set(val.Reflect()) which panicked because the mvm Iface (or the
		// reflect.StructOf-built *struct{}) is not assignable to error. Fixed
		// by routing through bridgeIface for native interface targets.
		// Surfaces in pkg/errors's TestErrorEquality.
		{n: "error_slice_mixed_native_and_mvm", src: `
import "errors"
type withStack struct { error }
w := &withStack{errors.New("inner")}
vals := []error{nil, errors.New("a"), w}
vals[2].Error()`, res: "inner"},

		// Multierror-style chain: interpreted type implements Error + Is +
		// As + Unwrap. stderrors.Is/As must reach the interpreted Is/As
		// bodies so the chain element comparison fires. Regression: bridge
		// composite only carried Error+Unwrap, so Is/As fell back to direct
		// equality and Unwrap-only walks, never matching the chain element.
		{n: "errors_chain_is_as_unwrap", src: `
import "errors"
type chain []error
func (c chain) Error() string { return c[0].Error() }
func (c chain) Unwrap() error { if len(c) == 1 { return nil }; return c[1:] }
func (c chain) Is(target error) bool { return errors.Is(c[0], target) }
func (c chain) As(target any) bool   { return errors.As(c[0], target) }
type matchErr struct{}
func (*matchErr) Error() string { return "match" }
foo := errors.New("foo")
bar := errors.New("bar")
m := &matchErr{}
c := chain{foo, bar, m}
var got *matchErr
ok := errors.Is(c, bar) && errors.As(c, &got) && got != nil
ok`, res: "true"},

		// Named slice type preserved through s[1:]: the slice expression
		// must keep coll.Type so the result still routes method calls to
		// the named type's method set.
		{n: "named_slice_preserved_on_slice_expr", src: `
type S []int
func (s S) Head() int { return s[0] }
s := S{10, 20, 30}
tail := s[1:]
tail.Head()`, res: "20"},
	})
}

func TestArithInt(t *testing.T) {
	run(t, []etest{
		{n: "add", src: "3 + 4", res: "7"},
		{n: "add_neg", src: "-3 + 4", res: "1"},
		{n: "add_zero", src: "0 + 0", res: "0"},

		{n: "sub", src: "10 - 3", res: "7"},
		{n: "sub_neg_result", src: "3 - 10", res: "-7"},

		{n: "mul", src: "6 * 7", res: "42"},
		{n: "mul_zero", src: "42 * 0", res: "0"},
		{n: "mul_neg", src: "-3 * 4", res: "-12"},

		{n: "div", src: "10 / 3", res: "3"},
		{n: "div_exact", src: "12 / 4", res: "3"},
		{n: "div_neg", src: "-7 / 2", res: "-3"},
		{n: "div_neg2", src: "7 / -2", res: "-3"},

		{n: "rem", src: "10 % 3", res: "1"},
		{n: "rem_neg", src: "-10 % 3", res: "-1"},
		{n: "rem_exact", src: "12 % 4", res: "0"},

		{n: "negate", src: "-42", res: "-42"},
		{n: "negate_neg", src: "a := -1; -a", res: "1"},

		{n: "gt_true", src: "3 > 2", res: "true"},
		{n: "gt_false", src: "2 > 3", res: "false"},
		{n: "lt_true", src: "2 < 3", res: "true"},
		{n: "lt_false", src: "3 < 2", res: "false"},
		{n: "eq_true", src: "3 == 3", res: "true"},
		{n: "eq_false", src: "3 == 4", res: "false"},

		{n: "ge_true", src: "3 >= 3", res: "true"},
		{n: "ge_true2", src: "4 >= 3", res: "true"},
		{n: "ge_false", src: "2 >= 3", res: "false"},
		{n: "le_true", src: "3 <= 3", res: "true"},
		{n: "le_true2", src: "2 <= 3", res: "true"},
		{n: "le_false", src: "4 <= 3", res: "false"},
		{n: "ne_true", src: "3 != 4", res: "true"},
		{n: "ne_false", src: "3 != 3", res: "false"},

		{n: "max_int", src: "var a int = 9223372036854775807; a", res: "9223372036854775807"},
		{n: "min_int", src: "var a int = -9223372036854775808; a", res: "-9223372036854775808"},

		{n: "inc", src: "a := 5; a++; a", res: "6"},
		{n: "dec", src: "a := 5; a--; a", res: "4"},

		{n: "add_assign", src: "a := 5; a += 3; a", res: "8"},
		{n: "sub_assign", src: "a := 5; a -= 3; a", res: "2"},
		{n: "mul_assign", src: "a := 5; a *= 3; a", res: "15"},
		{n: "div_assign", src: "a := 12; a /= 4; a", res: "3"},
		{n: "div_assign_uint64", src: "var a uint64 = 64; a /= 64; a", res: "1"},
		{n: "rem_assign", src: "a := 10; a %= 3; a", res: "1"},

		{n: "rem_float_const", src: "i := 102; i % -1e2", res: "2"},
		{n: "add_assign_float_const", src: "a := 4; a += 13/4.0; a", res: "7"},

		// Binary operators are left-associative: (a op b) op c, not a op (b op c).
		{n: "sub_chain", src: "10 - 3 - 2", res: "5"},     // right-assoc: 10-(3-2)=9
		{n: "sub_add_chain", src: "10 - 3 + 2", res: "9"}, // right-assoc: 10-(3+2)=5
		{n: "div_chain", src: "12 / 6 / 2", res: "1"},     // right-assoc: 12/(6/2)=4
		// Unary operators are right-associative.
		{n: "negate_double", src: "a := 5; - -a", res: "5"}, // left-assoc: would panic
	})
}

func TestBitwiseInt(t *testing.T) {
	run(t, []etest{
		{n: "and", src: "0xff & 0x0f", res: "15"},
		{n: "and_zero", src: "0xff & 0", res: "0"},

		{n: "or", src: "0xf0 | 0x0f", res: "255"},
		{n: "or_same", src: "0xff | 0xff", res: "255"},

		{n: "xor", src: "0xff ^ 0x0f", res: "240"},
		{n: "xor_self", src: "a := 42; a ^ a", res: "0"},

		{n: "andnot", src: "0xff &^ 0x0f", res: "240"},

		{n: "comp", src: "^0", res: "-1"},
		{n: "comp_neg1", src: "^-1", res: "0"},

		{n: "shl", src: "1 << 10", res: "1024"},
		{n: "shl_zero", src: "42 << 0", res: "42"},

		{n: "shr", src: "1024 >> 3", res: "128"},
		{n: "shr_neg", src: "-8 >> 1", res: "-4"},

		{n: "shl_var", src: "var u uint64 = 1; var v uint32 = 10; u << v", res: "1024"},
		{n: "shr_var", src: "var u uint64 = 1024; var v uint32 = 3; u >> v", res: "128"},
		{n: "shl_assign", src: "a := 1; a <<= 4; a", res: "16"},
		{n: "shr_assign", src: "a := 16; a >>= 4; a", res: "1"},

		// Untyped float constant as left operand of shift (Go spec: treated as int).
		{n: "shl_float_const", src: "const a = 1.0; a << 2", res: "4"},
		{n: "shl_float_const_expr", src: "const a = 1.0; const b = a + 3; b << 1", res: "8"},
		{n: "shr_float_const", src: "const a = 8.0; a >> 1", res: "4"},

		{n: "and_assign", src: "a := 0xff; a &= 0x0f; a", res: "15"},
		{n: "or_assign", src: "a := 0xf0; a |= 0x0f; a", res: "255"},
		{n: "xor_assign", src: "a := 0xff; a ^= 0x0f; a", res: "240"},
		{n: "andnot_assign", src: "a := 0xff; a &^= 0x0f; a", res: "240"},

		// Typed-narrower bit ops must truncate .num to the operand's type width
		// so .num stays in sync with the reflect-backed value. Reproduces an
		// x/text/internal/language EncodeM49 corruption: shifted-uint16 .num
		// leaked into comparisons via Lower/Equal, which read .num directly.
		{n: "shl_uint16_truncate", src: "n := uint16(840); v := n << 9; w := uint16(0x9000); v == w", res: "true"},
		{n: "shl_uint16_cmp", src: "n := uint16(840); v := n << 9; x := uint16(0x9136); x >= v", res: "true"},
		{n: "comp_uint16", src: "v := ^uint16(0); v == 0xffff", res: "true"},
		{n: "comp_int16", src: "v := ^int16(0); v == -1", res: "true"},
		{n: "shr_int16_signed", src: "n := int16(-8); v := n >> 1; v == -4", res: "true"},

		// AndNot must bind tighter than ==. Previously the token had no
		// Precedence entry, defaulting to 0 (below ==), so `a &^ b == c`
		// parsed as `a &^ (b == c)`.
		{n: "andnot_prec_eq", src: "a := uint16(0x10); b := uint16(0x10); c := uint16(0x20); (a &^ b) == c == (a&^b == c)", res: "true"},
		{n: "andnot_prec_neq", src: "a := uint16(0x7c20); b := uint16(0x1ff); c := uint16(0x600); a&^b == c", res: "false"},
	})
}

func TestString(t *testing.T) {
	run(t, []etest{
		{n: "concat", src: `"hello" + " " + "world"`, res: "hello world"},
		{n: "concat_var", src: `a := "foo"; b := "bar"; a + b`, res: "foobar"},
		{n: "concat_empty", src: `"hello" + ""`, res: "hello"},

		{n: "add_assign", src: `a := "hello"; a += " world"; a`, res: "hello world"},

		{n: "slice", src: `a := "hello world"; a[0:5]`, res: "hello"},
		{n: "slice_mid", src: `a := "hello world"; a[6:11]`, res: "world"},
		{n: "slice_open_high", src: `a := "hello"; a[1:]`, res: "ello"},
		{n: "slice_open_low", src: `a := "hello"; a[:3]`, res: "hel"},

		{n: "index_var", src: `a := "hello"; a[1]`, res: "101"},
		{n: "index_const", src: `const s = "hello"; s[1]`, res: "101"},

		{n: "rune_lit", src: `'a'`, res: "97"},
		{n: "rune_lit_escape", src: `'\n'`, res: "10"},
		{n: "string_lit_escape", src: `"hello\nworld"`, res: "hello\nworld"},
		{n: "raw_string_lit", src: "`hello\\nworld`", res: `hello\nworld`},
		{n: "rune_compare", src: `var r rune = 97; r == 'a'`, res: "true"},
	})
}

func TestArithUint(t *testing.T) {
	run(t, []etest{
		{n: "add", src: "var a, b uint = 3, 4; a + b", res: "7"},
		{n: "sub", src: "var a, b uint = 10, 3; a - b", res: "7"},
		{n: "mul", src: "var a, b uint = 6, 7; a * b", res: "42"},
		{n: "div", src: "var a, b uint = 10, 3; a / b", res: "3"},
		{n: "rem", src: "var a, b uint = 10, 3; a % b", res: "1"},

		{n: "gt_large", src: "var a uint = 18446744073709551615; var b uint = 0; a > b", res: "true"},
		{n: "lt_large", src: "var a uint = 0; var b uint = 18446744073709551615; a < b", res: "true"},

		{n: "max_uint", src: "var a uint = 18446744073709551615; a", res: "18446744073709551615"},

		{n: "uint8_max", src: "var a uint8 = 255; a", res: "255"},
		{n: "uint8_add_wrap", src: "var a uint8 = 255; var b uint8 = 1; a + b", res: "0"},

		{n: "uint16_max", src: "var a uint16 = 65535; a", res: "65535"},
		{n: "uint32_max", src: "var a uint32 = 4294967295; a", res: "4294967295"},

		{n: "shr_logical", src: "var a uint = 18446744073709551615; a >> 60", res: "15"},
	})
}

func TestArithFloat(t *testing.T) {
	run(t, []etest{
		{n: "add", src: "var a, b float64 = 1.5, 2.5; a + b", res: "4"},
		{n: "sub", src: "var a, b float64 = 5.5, 2.0; a - b", res: "3.5"},
		{n: "mul", src: "var a, b float64 = 2.5, 4.0; a * b", res: "10"},
		{n: "div", src: "var a, b float64 = 7.0, 2.0; a / b", res: "3.5"},
		{n: "negate", src: "var a float64 = 3.14; -a", res: "-3.14"},

		{n: "gt_true", src: "var a, b float64 = 3.14, 2.71; a > b", res: "true"},
		{n: "gt_false", src: "var a, b float64 = 2.71, 3.14; a > b", res: "false"},
		{n: "lt_true", src: "var a, b float64 = 2.71, 3.14; a < b", res: "true"},
		{n: "eq_true", src: "var a, b float64 = 3.14, 3.14; a == b", res: "true"},
		{n: "ne_true", src: "var a, b float64 = 3.14, 2.71; a != b", res: "true"},
		{n: "ge_true", src: "var a, b float64 = 3.14, 3.14; a >= b", res: "true"},
		{n: "le_true", src: "var a, b float64 = 2.71, 3.14; a <= b", res: "true"},

		{n: "lit_add", src: "1.5 + 2.5", res: "4"},
		{n: "lit_sub", src: "5.0 - 1.5", res: "3.5"},
		{n: "lit_mul", src: "2.5 * 4.0", res: "10"},
		{n: "lit_div", src: "7.0 / 2.0", res: "3.5"},
		{n: "lit_neg", src: "-3.14", res: "-3.14"},

		{n: "int_div_float_const", src: "13/4.0", res: "3.25"},
		{n: "float_div_int_const", src: "4.0/3", res: "1.3333333333333333"},
		{n: "float_mul_int_const", src: "2.5*2", res: "5"},
		{n: "float_add_int_const", src: "1.5+1", res: "2.5"},
		{n: "float_sub_int_const", src: "4.0-3", res: "1"},

		{n: "f32_add", src: "var a, b float32 = 1.5, 2.5; a + b", res: "4"},

		{n: "div_zero_pos", src: "var a, b float64 = 1.0, 0.0; a / b", res: "+Inf"},
		{n: "div_zero_neg", src: "var a, b float64 = -1.0, 0.0; a / b", res: "-Inf"},

		{n: "add_assign", src: "var a float64 = 1.5; a += 2.5; a", res: "4"},
		{n: "sub_assign", src: "var a float64 = 5.0; a -= 1.5; a", res: "3.5"},
		{n: "mul_assign", src: "var a float64 = 2.5; a *= 4.0; a", res: "10"},
		{n: "div_assign", src: "var a float64 = 7.0; a /= 2.0; a", res: "3.5"},
	})
}

func TestConvert(t *testing.T) {
	run(t, []etest{
		{n: "float64_to_int", src: "var a float64 = 3.14; int(a)", res: "3"},
		{n: "float64_to_int_neg", src: "var a float64 = -3.14; int(a)", res: "-3"},

		{n: "int_to_float64", src: "var a int = 42; float64(a)", res: "42"},
		{n: "int_to_int8", src: "var a int = 200; int8(a)", res: "-56"},
		{n: "int_to_uint8", src: "var a int = 256; uint8(a)", res: "0"},
		{n: "int_to_int16", src: "var a int = 40000; int16(a)", res: "-25536"},
		{n: "int_to_string", src: `string(65)`, res: "A"},
		{n: "int_to_int64", src: "var a int = 42; int64(a)", res: "42"},
		{n: "int_to_int", src: "var a int = 42; int(a)", res: "42"},

		{n: "uint_to_int", src: "var a uint = 5; int(a)", res: "5"},

		{n: "float32_to_float64", src: "var a float32 = 1.5; float64(a)", res: "1.5"},
		{n: "float64_to_float32", src: "var a float64 = 1.5; float32(a)", res: "1.5"},

		{n: "conv_in_expr", src: "var a float64 = 3.14; int(a) + 1", res: "4"},

		// Implicit numeric conversion in typed variable declarations.
		{n: "var_float64_int", src: "var x float64 = 5; x", res: "5"},
		{n: "var_float64_neg_int", src: "var x float64 = -5; x", res: "-5"},
		{n: "var_int32_int", src: "var x int32 = 42; x", res: "42"},
		{n: "var_uint8_int", src: "var x uint8 = 255; x", res: "255"},

		// Implicit conversion for math intrinsic calls.
		{n: "math_abs_int", src: `import "math"; math.Abs(-5)`, res: "5"},
		{n: "math_abs_pos_int", src: `import "math"; math.Abs(5)`, res: "5"},
		{n: "math_sqrt_int", src: `import "math"; math.Sqrt(4)`, res: "2"},
		{n: "math_min_int", src: `import "math"; math.Min(3, 5)`, res: "3"},
		{n: "math_copysign_int", src: `import "math"; math.Copysign(1, -5)`, res: "-1"},

		// interface{} type conversion.
		{n: "iface_convert", src: `import "fmt"; v := interface{}(0); fmt.Sprint(v)`, res: "0"},

		// Pointer-to-array type conversion: (*[N]T)(ptr).
		{n: "ptr_array_conv", src: `b := [4]byte{1, 2, 3, 4}; p := (*[4]byte)(&b); p[2]`, res: "3"},
		{n: "ptr_array_conv_named", src: `
type MyInt int
var x MyInt = 7
p := (*MyInt)(&x)
*p`, res: "7"},
	})
}

func TestArithTypedInt(t *testing.T) {
	run(t, []etest{
		{n: "int8_add", src: "var a, b int8 = 100, 20; a + b", res: "120"},
		{n: "int8_max", src: "var a int8 = 127; a", res: "127"},
		{n: "int8_min", src: "var a int8 = -128; a", res: "-128"},
		{n: "int8_wrap", src: "var a int8 = 127; var b int8 = 1; a + b", res: "-128"},

		{n: "int16_add", src: "var a, b int16 = 1000, 2000; a + b", res: "3000"},
		{n: "int16_max", src: "var a int16 = 32767; a", res: "32767"},
		{n: "int16_min", src: "var a int16 = -32768; a", res: "-32768"},

		{n: "int32_add", src: "var a, b int32 = 100000, 200000; a + b", res: "300000"},
		{n: "int32_max", src: "var a int32 = 2147483647; a", res: "2147483647"},

		{n: "int64_add", src: "var a, b int64 = 100, 200; a + b", res: "300"},
		{n: "int64_max", src: "var a int64 = 9223372036854775807; a", res: "9223372036854775807"},

		{n: "int8_mul", src: "var a, b int8 = 10, 12; a * b", res: "120"},
		{n: "int16_mul", src: "var a, b int16 = 200, 100; a * b", res: "20000"},
		{n: "int32_div", src: "var a, b int32 = 100, 3; a / b", res: "33"},
		{n: "int64_rem", src: "var a, b int64 = 100, 7; a % b", res: "2"},
	})
}

func TestDefer(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: `
			a := 0
			func f() { defer func() { a = 1 }() }
			f()
			a`, res: "1"},
		{n: "#01", src: `
			// Multiple defers run LIFO.
			s := ""
			func f() {
				defer func() { s = s + "a" }()
				defer func() { s = s + "b" }()
				defer func() { s = s + "c" }()
			}
			f()
			s`, res: "cba"},
		{n: "#02", src: `
			// Args evaluated at defer time, not call time.
			x := 0
			func add(a, b int) { x = a + b }
			func f() {
				i := 1
				defer add(i, 2)
				i = 10
			}
			f()
			x`, res: "3"},
		{n: "#03", src: `
			// Args evaluated at defer time in a loop (not call time).
			s := 0
			func add(n int) { s = s + n }
			func f() {
				for i := 0; i < 3; i++ {
					defer add(i)
				}
			}
			f()
			s`, res: "3"},
		{n: "#04", src: `
			// Defer runs after return value is computed.
			a := 0
			func f() int {
				defer func() { a = 1 }()
				return 42
			}
			r := f()
			r`, res: "42"},
		{n: "#05", src: `
			// Deferred closure sees modified value (capture by reference).
			r := 0
			func f() {
				i := 12
				defer func() { r = i }()
				i = 20
			}
			f()
			r`, res: "20"},
		{n: "defer_native_func_arg", src: `
import "sort"
func f() []int {
	s := []int{3, 1, 2}
	defer sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s
}
s := f()
s[0]*100 + s[1]*10 + s[2]`, res: "123"},
		{n: "defer_native_via_var", src: `
import "context"
r := 0
func f() {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	r = 1
}
f()
r`, res: "1"},
		{n: "defer_variadic_vm", src: `
// A deferred variadic VM func must pack its trailing args into a slice,
// not bind the variadic param to the last argument.
s := 0
func sum(xs ...int) { for _, v := range xs { s += v } }
func f() { defer sum(1, 2, 3) }
f()
s`, res: "6"},
		{n: "defer_variadic_vm_var", src: `
// Same, when the func is held in a local variable.
s := 0
g := func(xs ...int) { for _, v := range xs { s += v } }
func f() { defer g(4, 5, 6) }
f()
s`, res: "15"},
		{n: "defer_variadic_fixed_plus_rest", src: `
s := 0
func add(base int, xs ...int) { s = base; for _, v := range xs { s += v } }
func f() { defer add(100, 1, 2, 3) }
f()
s`, res: "106"},
		{n: "defer_variadic_spread", src: `
// Explicit spread defer must not re-pack the slice.
s := 0
func sum(xs ...int) { for _, v := range xs { s += v } }
func f() { r := []int{7, 8, 9}; defer sum(r...) }
f()
s`, res: "24"},
		{n: "defer_blank_func", src: `
// Multiple blank funcs are legal and never run.
func _() { panic("never") }
func _() { panic("never either") }
42`, res: "42"},
		{n: "defer_call_result_vm", src: `
// defer <call>(): the func expression is itself a call returning a VM
// closure. The closure is evaluated now and deferred; it must dispatch
// as a VM func, not via reflect (it is not native). See go-homedir.
r := 0
func makeFn() func() { return func() { r = 7 } }
func f() { defer makeFn()() }
f()
r`, res: "7"},
		{n: "defer_slice_elem_vm", src: `
// A VM closure reached via a slice index (symbol.Value) must also
// dispatch as a VM func.
r := 0
func f() { fns := []func(){func() { r = 9 }}; defer fns[0]() }
f()
r`, res: "9"},
		{n: "defer_func_field_captured", src: `
// defer captures the func value at the statement: nilling the field
// afterward (as a sync.Pool reset does) must not nil the deferred call.
type E struct{ done func(string) }
out := ""
func run(e *E, s string) { defer e.done(s); e.done = nil }
run(&E{done: func(s string) { out = s }}, "hi")
out`, res: "hi"},
		{n: "defer_func_var_reassigned", src: `
// Same for a func var reassigned to nil after the defer statement.
out := ""
done := func(s string) { out = s }
func run(s string) { defer done(s); done = nil }
run("hi")
out`, res: "hi"},
	})
}

func TestPanic(t *testing.T) {
	run(t, []etest{
		{n: "#00", src: `
			// Unrecovered panic propagates as error.
			func f() { panic("boom") }
			f()`, err: "panic: boom"},
		{n: "#01", src: `
			// Recover in deferred function stops the panic.
			a := 0
			func f() {
				defer func() { recover(); a = 1 }()
				panic("boom")
			}
			f()
			a`, res: "1"},
		{n: "#02", src: `
			// Recover returns the panic value.
			s := ""
			func f() {
				defer func() {
					r := recover()
					s = r.(string)
				}()
				panic("hello")
			}
			f()
			s`, res: "hello"},
		{n: "defer_panic_unrecovered", src: `
			// defer panic(x): fires on return, propagates as error.
			func f() { defer panic("boom") }
			f()`, err: "panic: boom"},
		{n: "defer_panic_recovered", src: `
			// defer panic(x) fires on return; an earlier-registered deferred
			// recover catches it.
			func f() (s string) {
				defer func() { if r := recover(); r != nil { s = r.(string) } }()
				defer panic("late")
				return "normal"
			}
			f()`, res: "late"},
		{n: "panic_in_deferred_field_call", src: `
			// A panic inside a deferred func value (dispatched as a native
			// func through a field) must be catchable by an enclosing deferred
			// recover, like Go -- not escape the VM as a raw Go panic.
			type E struct{ done func(string) }
			func step(e *E, s string) { defer e.done(s) }
			got := ""
			func f() {
				defer func() { if r := recover(); r != nil { got = r.(string) } }()
				step(&E{done: func(s string) { panic(s) }}, "boom")
			}
			f()
			got`, res: "boom"},
		{n: "#03", src: `
			// Unrecovered panic still runs defers, but propagates error.
			s := ""
			func f() {
				defer func() { s = s + "a" }()
				defer func() { s = s + "b" }()
				panic("x")
			}
			f()
			s`, err: "panic: x"},
		{n: "#04", src: `
			// Recover outside panic returns nil (as empty value).
			func f() {
				defer func() { recover() }()
			}
			f()
			0`, res: "0"},
		{n: "#05", src: `
			// Panic with int value.
			n := 0
			func f() {
				defer func() {
					r := recover()
					n = r.(int)
				}()
				panic(42)
			}
			f()
			n`, res: "42"},
		{n: "#06", src: `
			// Panic propagates through multiple frames.
			s := ""
			func g() { panic("deep") }
			func f() {
				defer func() {
					r := recover()
					s = r.(string)
				}()
				g()
			}
			f()
			s`, res: "deep"},
		{n: "#07", src: `
			// Code after panic does not execute.
			a := 1
			func f() {
				defer func() { recover() }()
				panic("x")
				a = 2
			}
			f()
			a`, res: "1"},
		{n: "#08", src: `
			// Panic with native deferred function.
			x := 0
			func add(n int) { x = x + n }
			func f() {
				defer add(10)
				panic("boom")
			}
			f()
			x`, err: "panic: boom"},
		{n: "#09", src: `
			// Multiple defers: first recovers, rest still run.
			s := ""
			func f() {
				defer func() { s = s + "a" }()
				defer func() { recover(); s = s + "b" }()
				defer func() { s = s + "c" }()
				panic("x")
			}
			f()
			s`, res: "cba"},
		{n: "nil_iface_call", src: `
			// Calling a method on a nil interface variable produces the same
			// runtime.Error Go emits, recoverable via defer.
			type Stringer interface { String() string }
			s := ""
			func f() {
				defer func() {
					r := recover()
					if e, ok := r.(error); ok {
						s = e.Error()
					}
				}()
				var x Stringer
				x.String()
			}
			f()
			s`, res: "runtime error: invalid memory address or nil pointer dereference"},
		{n: "nil_iface_call_unrecovered", src: `
			// Same panic propagates as a Go error string when unrecovered.
			type Stringer interface { String() string }
			var x Stringer
			x.String()`, err: "panic: runtime error: invalid memory address or nil pointer dereference"},
		{n: "nil_func_call", src: `
			// Calling a nil func value must panic, not jump to code address 0
			// (the program-entry Jump) and re-run the program / recurse.
			var f func()
			f()`, err: "panic: runtime error: invalid memory address or nil pointer dereference"},
		{n: "nil_defer_func", src: `
			func() { var f func(); defer f() }()`, err: "panic: runtime error: invalid memory address or nil pointer dereference"},
		{n: "nil_go_func", src: `
			func() { var f func(); go f() }()`, err: "panic: runtime error: invalid memory address or nil pointer dereference"},
		{n: "nil_defer_during_panic", src: `
			// Nil deferred func reached while already unwinding a panic.
			func() { var f func(); defer f(); panic("boom") }()`, err: "panic: runtime error: invalid memory address or nil pointer dereference"},
		{n: "panic_in_native_callback", src: `
			// A panic raised in an mvm method that native code invokes through an
			// interface (here io.Reader, via bytes.Buffer.ReadFrom) must be catchable
			// by an interpreted recover().
			import "bytes"
			type panicReader struct{}
			func (panicReader) Read(p []byte) (int, error) { panic("oops") }
			s := ""
			func f() {
				defer func() { s = recover().(string) }()
				var buf bytes.Buffer
				buf.ReadFrom(panicReader{})
			}
			f()
			s`, res: "oops"},
		{n: "panic_error_from_native", src: `
			// A native function that panics with a plain error value (not a runtime.Error)
			// must stay catchable by an interpreted recover().
			import "bytes"
			type negReader struct{}
			func (negReader) Read(p []byte) (int, error) { return -1, nil }
			s := ""
			func f() {
				defer func() {
					if e, ok := recover().(error); ok { s = e.Error() }
				}()
				var buf bytes.Buffer
				buf.ReadFrom(negReader{})
			}
			f()
			s`, res: "bytes.Buffer: reader returned negative count from Read"},
		{n: "native_recover_sees_raw_panic", src: `
			// Native code recovering an interpreted panic and reformatting %v sees
			// the raw value, not mvm's decorated stack (tabwriter: "during X (<raw>)").
			import ("strings"; "text/tabwriter")
			type pw struct{}
			func (pw) Write([]byte) (int, error) { panic("boom") }
			got := "no panic"
			func() {
				defer func() { if e := recover(); e != nil { got = e.(string) } }()
				w := new(tabwriter.Writer)
				w.Init(pw{}, 0, 0, 1, ' ', 0)
				w.Write([]byte("x\ty\n"))
				w.Flush()
			}()
			strings.Contains(got, "(boom)") && !strings.Contains(got, "mvm stack")`, res: "true"},
	})
}

// TestPanicDiagnostics locks in that a staged panic escaping to the host
// carries the source position and mvm call stack (via the PanicError snapshot
// captured at stage time), not just a bare "panic: <val>" line.
func TestPanicDiagnostics(t *testing.T) {
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	_, err := intp.Eval("test", `
		func g() { panic("boom") }
		func f() { g() }
		f()`)
	if err == nil {
		t.Fatal("expected a panic error, got nil")
	}
	got := err.Error()
	for _, want := range []string{"panic: boom", "at g (", "test:", "mvm stack:"} {
		if !strings.Contains(got, want) {
			t.Errorf("panic diagnostic missing %q in:\n%s", want, got)
		}
	}
}

// TestPanicDiagnosticsCrossBoundary locks in that a panic inside an mvm
// callback invoked by native code (here a sort.Slice comparator) produces a
// single mvm stack spanning interp -> native -> interp: the comparator frame,
// a native boundary row, and the interp frames that called sort.Slice.
func TestPanicDiagnosticsCrossBoundary(t *testing.T) {
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	_, err := intp.Eval("test", `
		import "sort"
		func cmp(a, b int) bool { panic("cmp boom") }
		func doSort(s []int) {
			sort.Slice(s, func(i, j int) bool { return cmp(s[i], s[j]) })
		}
		doSort([]int{3, 1, 2})`)
	if err == nil {
		t.Fatal("expected a panic error, got nil")
	}
	got := err.Error()
	for _, want := range []string{"panic: cmp boom", "at cmp (", "-- via sort.Slice [native] --", "doSort"} {
		if !strings.Contains(got, want) {
			t.Errorf("cross-boundary diagnostic missing %q in:\n%s", want, got)
		}
	}
}

func TestStructFuncField(t *testing.T) {
	run(t, []etest{
		{n: "assign_call", src: `
type S struct { F func(int) int }
var s S
s.F = func(n int) int { return n * 2 }
s.F(7)`, res: "14"},

		{n: "literal", src: `
type S struct { F func(int) int }
s := S{F: func(n int) int { return n + 1 }}
s.F(10)`, res: "11"},

		{n: "closure_capture", src: `
type S struct { F func() int }
x := 42
var s S
s.F = func() int { return x }
s.F()`, res: "42"},

		{n: "reassign", src: `
type S struct { F func() int }
var s S
s.F = func() int { return 1 }
s.F = func() int { return 2 }
s.F()`, res: "2"},

		{n: "iface_param", src: `
type I interface { Hello() string }
type T struct{ name string }
func (t T) Hello() string { return t.name }
type S struct { Handler func(I) string }
var s S
s.Handler = func(i I) string { return i.Hello() }
s.Handler(T{name: "world"})`, res: "world"},

		{n: "native_call", src: `
type S struct { F func(int) int }
var s S
s.F = func(n int) int { return n * 3 }
s.F(5)`, res: "15"},

		// Assigning a named func to a func field: nil check must see non-nil (struct35).
		{n: "named_func_nil_check", src: `
type T struct { f func(*T) }
func f1(t *T) { t.f = f1 }
t := &T{}
f1(t)
t.f != nil`, res: "true"},

		// Closure in struct func field survives append (struct copy to new backing array).
		{n: "append_copy", src: `
type T struct{ F func() int }
func g() int {
	var foos []T
	for i := 0; i < 3; i++ {
		a := i
		foos = append(foos, T{func() int { return a }})
	}
	return foos[0].F() + foos[1].F()*10 + foos[2].F()*100
}
g()`, res: "210"},
	})
}

func TestBuiltin(t *testing.T) {
	run(t, []etest{
		{n: "len_slice", src: `a := []int{1, 2, 3}; len(a)`, res: "3"},
		{n: "len_string", src: `len("hello")`, res: "5"},
		{n: "cap_slice", src: `a := make([]int, 2, 5); cap(a)`, res: "5"},
		{n: "make_slice", src: `a := make([]int, 3); len(a)`, res: "3"},
		{n: "make_slice_cap", src: `a := make([]int, 2, 10); cap(a)`, res: "10"},
		{n: "make_map", src: `m := make(map[string]int); m["x"] = 5; m["x"]`, res: "5"},
		{n: "make_pkg_type", src: `import "net/http"; h := make(http.Header); len(h)`, res: "0"},
		{n: "append_basic", src: `a := []int{1, 2}; a = append(a, 3); a`, res: "[1 2 3]"},
		{n: "append_multi", src: `a := []int{1}; a = append(a, 2, 3, 4); a`, res: "[1 2 3 4]"},
		{n: "append_spread", src: `a := []int{1, 2}; b := []int{3, 4}; a = append(a, b...); a`, res: "[1 2 3 4]"},
		{n: "append_spread_string", src: `a := append([]byte("Hello"), " World"...); string(a)`, res: "Hello World"},
		{n: "append_nil", src: `import "fmt"; a := []*int{}; a = append(a, nil); fmt.Sprint(a)`, res: "[<nil>]"},
		{n: "copy_basic", src: `a := []int{1, 2, 3}; b := make([]int, 2); copy(b, a); b`, res: "[1 2]"},
		{n: "copy_retval", src: `a := []int{1, 2, 3}; b := make([]int, 5); n := copy(b, a); n`, res: "3"},
		{n: "copy_ptr_array", src: `a := []int{10, 20, 30}; b := &[4]int{}; c := b[:]; copy(c, a); c`, res: "[10 20 30 0]"},
		{n: "delete_map", src: `m := map[string]int{"a": 1, "b": 2}; delete(m, "a"); len(m)`, res: "1"},
		{n: "new_int", src: `p := new(int); *p`, res: "0"},
		{n: "new_string", src: `p := new(string); *p`, res: ""},

		// min/max builtins.
		{n: "min_int2", src: `min(3, 1)`, res: "1"},
		{n: "min_int3", src: `min(5, 2, 8)`, res: "2"},
		{n: "min_int1", src: `min(42)`, res: "42"},
		{n: "min_string", src: `min("b", "a", "c")`, res: "a"},
		{n: "min_float", src: `min(3.0, 1.5)`, res: "1.5"},
		{n: "max_int2", src: `max(3, 1)`, res: "3"},
		{n: "max_int3", src: `max(5, 2, 8)`, res: "8"},
		{n: "max_int1", src: `max(42)`, res: "42"},
		{n: "max_string", src: `max("b", "a", "c")`, res: "c"},
		{n: "max_float", src: `max(3.0, 1.5)`, res: "3"},
		// Untyped const idents carry Type nil; result is the first concretely
		// typed arg, or the default const type when all args are untyped.
		{n: "min_const_var", src: `const k = 64; var n int = 5; min(k, n)`, res: "5"},
		{n: "min_const_const", src: `const a, b = 1, 2; min(a, b)`, res: "1"},
		{n: "max_const_const_float", src: `const a, b = 1.5, 2.5; max(a, b)`, res: "2.5"},
		// Float min/max signed-zero/NaN per Go spec: max prefers +0, min prefers
		// -0 (they compare equal under </>), and any NaN operand yields NaN.
		{n: "max_signed_zero", src: `import "math"; math.Signbit(max(math.Copysign(0, -1), 0.0))`, res: "false"},
		{n: "min_signed_zero", src: `import "math"; math.Signbit(min(0.0, math.Copysign(0, -1)))`, res: "true"},
		{n: "min_nan", src: `import "math"; math.IsNaN(min(math.NaN(), 1.0))`, res: "true"},
		{n: "max_nan", src: `import "math"; math.IsNaN(max(1.0, math.NaN()))`, res: "true"},

		// complex
		{n: "complex64", src: `complex(float32(12),float32(34))`, res: "(12+34i)"},
		{n: "complex64_lit_int_f32", src: `complex(12,float32(34))`, res: "(12+34i)"},
		{n: "complex64_lit_float_f32", src: `complex(12.0,float32(34))`, res: "(12+34i)"},
		{n: "complex128", src: `complex(float64(12),float64(34))`, res: "(12+34i)"},
		{n: "complex128_lit_int", src: `complex(12,34)`, res: "(12+34i)"},
		{n: "complex128_lit_int_float", src: `complex(12,34.0)`, res: "(12+34i)"},
		{n: "complex128_lit_int_f64", src: `complex(12,float64(34))`, res: "(12+34i)"},
		{n: "complex128_lit_float", src: `complex(12.0,34.0)`, res: "(12+34i)"},
		{n: "complex64_real", src: `real(complex(float32(12),float32(34)))`, res: "12"},
		{n: "complex64_imag", src: `imag(complex(float32(12),float32(34)))`, res: "34"},
		{n: "complex128_real", src: `real(complex(float64(12),float64(34)))`, res: "12"},
		{n: "complex128_imag", src: `imag(complex(float64(12),float64(34)))`, res: "34"},

		// Untyped const operands (no concrete Type on the symbol).
		{n: "complex128_const_real", src: `const b = -1.0 / 4.0; complex(b, 0)`, res: "(-0.25+0i)"},
		{n: "complex128_const_both", src: `const r, i = 0.5, 1.5; complex(r, i)`, res: "(0.5+1.5i)"},
		{n: "complex128_const_mixed", src: `const r, i = 1, 0.5; complex(r, i)`, res: "(1+0.5i)"},
		// Degenerate const with no Type, Cval, or Value must error, not crash.
		{n: "complex_bare_iota", src: `complex(iota, 0)`, err: "expected floating-point"},

		{n: "complex128_promotion_0", src: `complex(1, 'A')`, res: "(1+65i)"},
		{n: "complex128_promotion_1", src: `complex(1.2, 'A')`, res: "(1.2+65i)"},
		{n: "complex128_promotion_2", src: `complex('A', 1)`, res: "(65+1i)"},
		{n: "complex128_promotion_3", src: `complex('A', 1.2)`, res: "(65+1.2i)"},

		// Constant conversion of an int/float to a complex type (Go folds these;
		// reflect.Convert rejects int->complex, so execConvert builds it).
		{n: "complex64_conv_int", src: `complex64(7)`, res: "(7+0i)"},
		{n: "complex128_conv_float", src: `var a complex128 = 2.5; a`, res: "(2.5+0i)"},
		{n: "complex64_slice_lit", src: `[]complex64{1, 2, 3}`, res: "[(1+0i) (2+0i) (3+0i)]"},
		// Runtime (non-const) complex arithmetic.
		{n: "complex128_runtime_add", src: `var a complex128 = 2 + 3i; var b complex128 = 4 - 1i; a + b`, res: "(6+2i)"},
		{n: "complex128_runtime_sub", src: `var a complex128 = 2 + 3i; var b complex128 = 4 - 1i; a - b`, res: "(-2+4i)"},
		{n: "complex128_runtime_mul", src: `var a complex128 = 2 + 3i; var b complex128 = 4 - 1i; a * b`, res: "(11+10i)"},
		{n: "complex128_runtime_div", src: `var a complex128 = 8 + 4i; var b complex128 = 2; a / b`, res: "(4+2i)"},
		{n: "complex128_runtime_neg", src: `var a complex128 = 2 + 3i; -a`, res: "(-2-3i)"},
		{n: "complex128_runtime_mixed_const", src: `var a complex128 = 2 + 3i; 2 * a`, res: "(4+6i)"},
		{n: "complex64_runtime_mul", src: `var a complex64 = 2 + 3i; var b complex64 = 1 + 1i; a * b`, res: "(-1+5i)"},
		{n: "complex64_runtime_div", src: `var a complex64 = 2 + 3i; var b complex64 = 1 + 1i; a / b`, res: "(2.5+0.5i)"},
		{n: "complex64_runtime_neg", src: `var a complex64 = 2 + 3i; -a`, res: "(-2-3i)"},
		{n: "complex128_runtime_addassign", src: `func f() complex128 { var a, b complex128 = 1 + 2i, 3 + 4i; a += b; return a }; f()`, res: "(4+6i)"},
		{n: "complex128_runtime_eq", src: `var a, b complex128 = 1 + 2i, 1 + 2i; a == b`, res: "true"},
		{n: "complex128_rem_err", src: `var a, b complex128 = 1, 2; a % b`, err: "not defined on"},

		// An interpreted String/GoString/Format that panics propagates to fmt's
		// catchPanic (synth dispatch re-raises the original value via raiseMethodErr).
		{n: "stringer_panic_to_fmt", src: `import "fmt"; type T struct{}; func(t T) String() string { panic("boom") }; func f() string { return fmt.Sprintf("%s", T{}) }; f()`, res: "%!s(PANIC=String method: boom)"},
		// A method that derefs a nil pointer receiver panics; fmt prints <nil>.
		{n: "stringer_nil_recv_to_fmt", src: `import "fmt"; type T struct{ s string }; func(t T) String() string { return t.s }; func f() string { return fmt.Sprintf("%s", (*T)(nil)) }; f()`, res: "<nil>"},
		// A struct embedding a native non-empty interface satisfies it via promotion
		// at the native boundary (struct field keeps the real io.Reader rtype).
		{n: "embed_native_iface", src: `import ("io"; "strings"); func f() string { var r io.Reader = struct{ io.Reader }{strings.NewReader("hi")}; b, _ := io.ReadAll(r); return string(b) }; f()`, res: "hi"},

		{n: "complex_err0", src: `complex()`, err: "invalid operation: not enough arguments for complex (expected 2, found 0)"},
		{n: "complex_err1", src: `complex(1)`, err: "invalid operation: not enough arguments for complex (expected 2, found 1)"},
		{n: "complex_err12", src: `complex(1,2,3)`, err: "invalid operation: too many arguments for complex (expected 2, found 3)"},
		{n: "complex128_lit_err1", skip: true, src: `complex(int(12),34)`, err: "invalid argument: type int, expected floating-point"},
		{n: "complex128_lit_err2", skip: true, src: `complex(12,int(34))`, err: "invalid argument: type int, expected floating-point"},
		{n: "complex128_lit_err3", skip: true, src: `complex(float32(12),float64(34))`, err: "invalid operation: mismatched types float32 and float64"},
		{n: "complex128_lit_err4", src: `complex(12, "34")`, err: "invalid argument: type string, expected floating-point"}, // FIXME(sbinet): compiled Go has different error string.

		{n: "complex_lit_add", src: `1 + 2i`, res: "(1+2i)"},
		{n: "complex_lit_sub", src: `(1+2i) - (3+4i)`, res: "(-2-2i)"},
		{n: "complex_lit_mul", src: `(1+2i) * (3-4i)`, res: "(11+2i)"},
		{n: "complex_lit_neg", src: `-(1+2i)`, res: "(-1-2i)"},
		{n: "complex_lit_conv64", src: `complex64(1+2i)`, res: "(1+2i)"},
		{n: "complex_lit_wide_im", src: `1e10 + 1.11e100i`, res: "(1e+10+1.11e+100i)"},
		{n: "float_trailing_dot", src: `11.`, res: "11"},
		{n: "float_trailing_dot_neg", src: `-11.`, res: "-11"},
		{n: "float_trailing_dot_add", src: `11. + 1`, res: "12"},
		{n: "float_trailing_dot_complex", src: `-11. + 7e+1i`, res: "(-11+70i)"},
	})
}

func TestGoroutine(t *testing.T) {
	run(t, []etest{
		{n: "buffered_chan", src: `ch := make(chan int, 1); ch <- 42; <-ch`, res: "42"},
		{n: "goroutine_func_lit", src: `ch := make(chan int, 1); go func() { ch <- 42 }(); <-ch`, res: "42"},
		{n: "goroutine_with_arg", src: `ch := make(chan int, 1); go func(n int) { ch <- n * 2 }(21); <-ch`, res: "42"},
		{n: "close_and_recv", src: `ch := make(chan int, 1); ch <- 5; close(ch); v, ok := <-ch; ok`, res: "true"},
		{n: "recv_closed_ok_false", src: `ch := make(chan int, 1); close(ch); _, ok := <-ch; ok`, res: "false"},
		{n: "make_chan_buffered", src: `ch := make(chan int, 3); ch <- 1; ch <- 2; ch <- 3; (<-ch) + (<-ch) + (<-ch)`, res: "6"},
		// GoCallImm path: named func called via go, parent must still push to stack after goroutine launch.
		{n: "goroutine_named_func_unbuffered", src: `func send(c chan string) { c <- "ping" }; ch := make(chan string); go send(ch); <-ch`, res: "ping"},
		// A go-spawned variadic VM func must pack its trailing args into a slice.
		{n: "goroutine_variadic", src: `func sum(ch chan int, xs ...int) { s := 0; for _, v := range xs { s += v }; ch <- s }; ch := make(chan int); go sum(ch, 1, 2, 3, 4); <-ch`, res: "10"},
		// Directional channel types: chan<- (send-only) and <-chan (recv-only).
		{n: "send_only_chan_param", src: `func send(c chan<- string) { c <- "ping" }; ch := make(chan string); go send(ch); <-ch`, res: "ping"},
		{n: "recv_only_chan_param", src: `func recv(c <-chan string) string { return <-c }; ch := make(chan string, 1); ch <- "ping"; recv(ch)`, res: "ping"},
		// Non-default element type coercion on send.
		{n: "send_int32_chan", src: `func send(c chan<- int32) { c <- 123 }; ch := make(chan int32); go send(ch); <-ch`, res: "123"},
		// Named channel type embedded in struct.
		{n: "named_chan_type", src: `type Channel chan string; func send(c Channel) { c <- "ping" }; ch := make(Channel); go send(ch); <-ch`, res: "ping"},
		{n: "embedded_named_chan", src: `type Channel chan string; type T struct { Channel }; t := T{make(Channel)}; go func() { t.Channel <- "ping" }(); <-t.Channel`, res: "ping"},
		// Inline end-of-line comment after a go statement (was: "go requires a function call").
		{n: "go_inline_comment", src: `
ch := make(chan int, 1)
go func() { ch <- 7 }() // launch
<-ch`, res: "7"},

		{n: "chan_reassign_after_goroutine", src: `
func sendTo(ch chan<- int, v int) { ch <- v }
ch := make(chan int)
go sendTo(ch, 42)
orig := ch
ch = make(chan int)
<-orig`, res: "42"},

		{n: "chan_func_named", src: `func f() int { return 42 }; ch := make(chan func() int, 1); ch <- f; g := <-ch; g()`, res: "42"},

		{n: "chan_func_closure", src: `x := 10; f := func() int { return x }; ch := make(chan func() int, 1); ch <- f; g := <-ch; g()`, res: "10"},

		{n: "chan_func_goroutine", src: `func f(n int) int { return n * 2 }; ch := make(chan func(int) int, 1); go func() { ch <- f }(); g := <-ch; g(21)`, res: "42"},

		{n: "goroutine_chan_pipeline", src: `
func filter(in <-chan int, out chan<- int, prime int) {
	for { i := <-in; if i%prime != 0 { out <- i } }
}
func generate(ch chan<- int) { for i := 2; ; i++ { ch <- i } }
ch := make(chan int)
go generate(ch)
prime := <-ch
ch1 := make(chan int)
go filter(ch, ch1, prime)
ch = ch1
<-ch`, res: "3"},

		{n: "go_native_func_arg", src: `
import "sort"
ch := make(chan bool, 1)
s := []int{3, 1, 2}
go func() {
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	ch <- true
}()
<-ch
s[0]*100 + s[1]*10 + s[2]`, res: "123"},
	})
}

func TestSelect(t *testing.T) {
	run(t, []etest{
		{n: "select_recv_buffered", src: `ch := make(chan int, 1); ch <- 42; r := 0; select { case v := <-ch: r = v }; r`, res: "42"},

		{n: "select_default", src: `ch := make(chan int); r := 0; select { case v := <-ch: r = v; default: r = 99 }; r`, res: "99"},

		{n: "select_send", src: `
ch := make(chan int, 1)
select { case ch <- 7: }
<-ch`, res: "7"},
		{n: "select_recv_ok", src: `
ch := make(chan int, 1)
ch <- 5
close(ch)
r := false
select { case _, ok := <-ch: r = ok }
r`, res: "true"},

		{n: "select_recv_closed_ok_false", src: `
ch := make(chan int)
close(ch)
r := false
select { case _, ok := <-ch: r = ok }
r`, res: "false"},

		{n: "select_with_goroutine", src: `
ch := make(chan string)
go func() { ch <- "hello" }()
r := ""
select { case v := <-ch: r = v }
r`, res: "hello"},

		{n: "select_in_loop", src: `
ch := make(chan int, 3)
ch <- 10; ch <- 20; ch <- 30
sum := 0
for i := 0; i < 3; i++ { select { case v := <-ch: sum = sum + v } }
sum`, res: "60"},

		{n: "select_bare_recv", src: `ch := make(chan int, 1); ch <- 1; select { case <-ch: }; 42`, res: "42"},

		// reflect.Select returns an unaddressable value; the recv var must land
		// in addressable storage so a later field assignment works (gonum diff/fd).
		{n: "select_recv_struct_field_set", src: `
type S struct{ n int; f float64 }
ch := make(chan S, 1)
ch <- S{n: 1}
r := 0.0
select { case v := <-ch: v.f = 2.5; r = v.f }
int(r * 10)`, res: "25"},

		{n: "select_multiple_cases", src: `
ch1 := make(chan int, 1)
ch2 := make(chan string, 1)
ch2 <- "ok"
r := ""
select {
case v := <-ch1: r = "int"
case v := <-ch2: r = v
}
r`, res: "ok"},

		{n: "select_empty_block_comment", src: `
ch := make(chan int, 1)
ch <- 1
r := 0
go func() { select {} // block forever
}()
r = <-ch
r`, res: "1"},

		{n: "select_native_chan", src: `
import "time"
ticker := time.NewTicker(time.Millisecond)
r := false
select { case t := <-ticker.C: r = t.Unix() > 0 }
ticker.Stop()
r`, res: "true"},

		{n: "select_native_chan_goroutine", src: `
import "time"
ticker := time.NewTicker(time.Millisecond)
ch := make(chan bool)
go func() {
	select { case t := <-ticker.C: ch <- t.Unix() > 0 }
}()
r := <-ch
ticker.Stop()
r`, res: "true"},

		{n: "select_default_in_range", src: `
ch := make(chan int, 10)
r := 0
for _, c := range "abc" {
	select {
	case ch <- int(c):
	default:
	}
	r++
}
r`, res: "3"},
	})
}

func TestCommentAfterBlock(t *testing.T) {
	run(t, []etest{
		{n: "if_comment", src: `a := 1; if true {} // comment
a`, res: "1"},
		{n: "for_comment", src: `a := 0; for i := 0; i < 3; i++ {} // comment
a`, res: "0"},
		{n: "switch_comment", src: `a := 1; switch {} // comment
a`, res: "1"},
	})
}

func TestTimeSleep(t *testing.T) {
	run(t, []etest{
		{n: "sleep_duration", src: `import "time"; time.Sleep(time.Nanosecond); 1`, res: "1"},
		{n: "sleep_int_coerce", src: `import "time"; time.Sleep(1); 1`, res: "1"},
	})
}

func TestPackageDecl(t *testing.T) {
	run(t, []etest{
		{n: "comment_before_package", src: `
// A file-level comment
package main
func answer() int { return 42 }
answer()`, res: "42"},
	})
}

func TestReflectMethodByNameConcurrentMachines(t *testing.T) {
	const goroutines = 8
	const iters = 50
	for i := 0; i < goroutines; i++ {
		name := fmt.Sprintf("g%d", i)
		want := fmt.Sprintf("hello-%d", i)
		src := `import "reflect"
type Namer interface { Name() string }
type T struct{}
func (T) Name() string { return "` + want + `" }
func run() string {
	var n Namer = T{}
	rv := reflect.ValueOf(n)
	return rv.MethodByName("Name").Call(nil)[0].String()
}
out := ""
for i := 0; i < ` + strconv.Itoa(iters) + `; i++ { out = run() }
out`
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			r, err := intp.Eval(name, src)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			if got := fmt.Sprintf("%v", r); got != want {
				t.Fatalf("got %q, want %q (likely a cross-Machine method leak)", got, want)
			}
		})
	}
}

func TestRuntimeCallersConcurrentMachines(t *testing.T) {
	const goroutines = 8
	for i := 0; i < goroutines; i++ {
		name := fmt.Sprintf("g%d", i)
		fnName := fmt.Sprintf("capture_%d", i)
		src := `import (
	"runtime"
	"strings"
)
func ` + fnName + `() string {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(0, pcs)
	var names []string
	for i := 0; i < n; i++ {
		fn := runtime.FuncForPC(pcs[i])
		if fn == nil { continue }
		names = append(names, fn.Name())
	}
	return strings.Join(names, "|")
}
out := ""
for i := 0; i < 25; i++ { out = ` + fnName + `() }
out`
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			r, err := intp.Eval(name, src)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			got := fmt.Sprintf("%v", r)
			if !strings.Contains(got, fnName) {
				t.Fatalf("got %q, want a frame name containing %q (likely cross-Machine Callers leak)", got, fnName)
			}
			for j := 0; j < goroutines; j++ {
				if j == i {
					continue
				}
				other := fmt.Sprintf("capture_%d", j)
				if strings.Contains(got, other) {
					t.Fatalf("got %q -- contains another goroutine's frame name %q (cross-Machine Callers leak)", got, other)
				}
			}
		})
	}
}

// runtime.CallersFrames is virtualized to return *mvmFrames; a struct field or
// variable typed *runtime.Frames must therefore resolve to *mvmFrames, else the
// assignment fails reflect.Set ("*stdlib.mvmFrames is not assignable to
// *runtime.Frames"). Repro of go.uber.org/zap internal/stacktrace.Stack.frames
// (TestConfig/development), which holds the iterator in a field across calls.
func TestRuntimeFramesFieldType(t *testing.T) {
	src := `import "runtime"

type stack struct {
	frames *runtime.Frames
}

func capture() int {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(0, pcs)
	s := &stack{}
	s.frames = runtime.CallersFrames(pcs[:n]) // the field assign that failed
	count := 0
	for {
		_, more := s.frames.Next()
		count++
		if !more {
			break
		}
	}
	return count
}
capture()`
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	r, err := intp.Eval("frames_field", src)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got == "0" || got == "<invalid reflect.Value>" {
		t.Fatalf("got %q, want a positive frame count", got)
	}
}

// runtime.Caller/Callers with a single-PC buffer and a skip past the
// interpreted stack must reach the genuine host frames below the VM boundary
// (runtime.goexit), matching Go. Repro of zap's getCallerFrame caller-skip
// tests (TestLoggerAddCaller/Function, TestSugarAddCaller), which feed a large
// AddCallerSkip so the caller lands in the runtime.
func TestRuntimeCallerReachesHostGoexit(t *testing.T) {
	src := `import "runtime"
found := false
for skip := 0; skip < 40; skip++ {
	pc := make([]uintptr, 1)
	if runtime.Callers(skip, pc) < 1 {
		continue
	}
	f, _ := runtime.CallersFrames(pc).Next()
	if f.Function == "runtime.goexit" {
		found = true
	}
}
found`
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	r, err := intp.Eval("caller_host", src)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got := fmt.Sprintf("%v", r); got != "true" {
		t.Fatalf("runtime.Caller never reached host runtime.goexit (got %q); host tail not appended", got)
	}
}

// TestRepl exercises the re-entrant interpreter (REPL mode), where a single
// Interp is used across multiple sequential Eval calls.
func TestRepl(t *testing.T) {
	// Global data from a prior Eval must not occupy the same slots as new
	// constants from subsequent Evals.
	t.Run("stale_data", func(t *testing.T) {
		intp := interp.NewInterpreter(golang.GoSpec)
		if _, err := intp.Eval("1", "12/5.1"); err != nil {
			t.Fatal(err)
		}
		r, err := intp.Eval("2", "13/4.0")
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%v", r); got != "3.25" {
			t.Errorf("got %v, want 3.25", got)
		}
	})
}

// Two files of one main package alias the same name to different packages:
// "strings" is the real strings in a.go and an alias for strconv in b.go.
// Imports must be file-scoped, else the shared bare key collides (last file
// wins) and one file resolves the wrong package.
func TestFileScopedImports(t *testing.T) {
	files := []goparser.PackageSource{
		{Name: "a.go", Content: `package main
import "strings"
func useA() string { return strings.ToUpper("hi") }`},
		{Name: "b.go", Content: `package main
import strings "strconv"
func useB() string { return strings.Itoa(42) }
func main() { print(useA(), " ", useB()) }`},
	}
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	var out bytes.Buffer
	intp.SetIO(nil, &out, &out)
	if _, err := intp.EvalFiles(files); err != nil {
		t.Fatalf("EvalFiles: %v", err)
	}
	if got, want := out.String(), "HI 42"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
