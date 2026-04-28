package interp_test

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
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
		// Out of scope: skipped tests for known unsupported features.
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
		// Interface-with-union type constraints (cmp.Ordered-style).
		{n: "union_iface_ok", src: `type Ord interface { ~int | ~string }; func F[T Ord](x T) T { return x }; F[int](42)`, res: "42"},
		{n: "union_iface_ok_str", src: `type Ord interface { ~int | ~string }; func F[T Ord](x T) T { return x }; F[string]("hi")`, res: "hi"},
		{n: "union_iface_reject", src: `type Ord interface { ~int | ~string }; func F[T Ord](x T) T { return x }; F[float64](1.0)`, err: "does not satisfy constraint"},
		{n: "approx_named", src: `type Ord interface { ~int }; type MyInt int; func F[T Ord](x T) T { return x }; F[MyInt](MyInt(1))`, res: "1"},
		// Inter-param reference in constraints (e.g. slices.Sort-style [S ~[]E, E Ord]).
		{n: "typeparam_ref", src: `func F[T any, U T](x U) U { return x }; F[int, int](1)`, res: "1"},
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
		// Inference unwraps compound parameter shapes to bind type params.
		{n: "infer_ptr", src: `func F[T any](x *T) T { return *x }; a := 42; F(&a)`, res: "42"},
		{n: "infer_slice_of", src: `func F[T any](x []T) T { return x[0] }; F([]int{7, 8})`, res: "7"},
		{n: "infer_ptr_slice", src: `func F[T any](x *[]T) int { return len(*x) }; a := []int{1, 2}; F(&a)`, res: "2"},
		{n: "infer_map", src: `func F[K comparable, V any](m map[K]V, k K) V { return m[k] }; F(map[string]int{"a": 1}, "a")`, res: "1"},
		{n: "infer_slice_ptr", src: `func F[T any](x []*T) T { return *x[0] }; a := 9; F([]*int{&a})`, res: "9"},
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

		{n: "uint64_complement", src: `const a = ^uint64(0); a`, res: "18446744073709551615"},
		{n: "uint64_maxint", src: `const a = int64(^uint64(0) >> 1); a`, res: "9223372036854775807"},
		{n: "uint8_complement", src: `import "fmt"; const a = ^uint8(0); fmt.Sprintf("%T %v", a, a)`, res: "uint8 255"},
		{n: "uint64_nested_conv", src: `const a = int64(int64(^uint64(0) >> 1)); a`, res: "9223372036854775807"},
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

		{n: "2d_array", src: `a := [3][2]int{{1, 2}, {3, 4}, {5, 6}}; a[1][0]`, res: "3"},
		{n: "2d_slice", src: `a := [][]int{{1, 2}, {3, 4}}; a[1][0]`, res: "3"},
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

		// struct with embedded type that has methods and additional fields
		// (reflect.StructOf panics if Anonymous=true on a type with methods in a multi-field struct)
		{n: "embed_with_methods", src: `
import "bytes"
type Buf struct {
	*bytes.Buffer
	size int
}
b := &Buf{Buffer: bytes.NewBufferString("hello"), size: 5}
b.size`, res: "5"},
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

		// Naked block creates a new scope; inner variable shadows outer.
		{n: "block_scope", src: `
func f() int {
	a := 1
	{ a := 3; _ = a }
	return a
}
f()`, res: "1"},
		// Multiple nested blocks each shadow independently.
		{n: "block_scope_nested", src: `
func f() int {
	a := 1
	{ a := 2; { a := 3; _ = a }; _ = a }
	return a
}
f()`, res: "1"},

		// Parameter name shadows an imported package.
		{n: "param_shadows_pkg", src: `
import "time"
func test(time string, t time.Time) string { return time }
test("ok", time.Now())`, res: "ok"},

		// Struct field name shadows a builtin type (e.g. rune).
		{n: "field_shadows_type", src: `
type P struct { pos uint8; size uint8 }
type buf struct { rune [3]P }
len(buf{}.rune)`, res: "3"},

		// Local type aliases chained inside a function, used in composite literal.
		{n: "local_alias_composite", src: `
type original struct { Field string }
func f() string {
	type alias original
	type alias2 alias
	a := &alias2{Field: "test"}
	return a.Field
}
f()`, res: "test"},
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

		// embedding a builtin interface (error) and a method name that shadows a user-defined type name
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

		// return concrete value from named interface func, then call method
		{n: "iface_return_method", src: `
type I interface { A() string }
type s struct{}
func NewS() (I, error) { return &s{}, nil }
func (c *s) A() string { return "a" }
v, _ := NewS()
v.A()`, res: "a"},

		// chained method call on interface-typed struct field returned as interface
		{n: "iface_struct_field_method", src: `
type Enabler interface { Enabled() bool }
type Logger struct { core Enabler }
func (log *Logger) GetCore() Enabler { return log.core }
type T struct{}
func (t *T) Enabled() bool { return true }
base := &Logger{&T{}}
base.GetCore().Enabled()`, res: "true"},

		// nil error interface: short-circuit prevents call on nil receiver
		{n: "error_nil_shortcircuit", src: `
var a error = nil
r := ""
if a == nil || a.Error() == "nil" { r = "nil" }
r`, res: "nil"},

		// explicit interface type conversion T(x) where T is an interface type
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

		// sort.Interface bridge: interpreted type passed to native sort.Sort
		{n: "sort_iface", src: `
import "sort"
type byLen []string
func (b byLen) Len() int           { return len(b) }
func (b byLen) Less(i, j int) bool { return len(b[i]) < len(b[j]) }
func (b byLen) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }
s := byLen{"bbb", "a", "cc"}
sort.Sort(s)
s[0] + " " + s[1] + " " + s[2]`, res: "a cc bbb"},

		// heap.Interface bridge: interpreted type passed to native heap functions
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

		// sort.Interface bridge with pointer receiver
		{n: "sort_iface_ptr", src: `
import "sort"
type S struct { vals []int }
func (s *S) Len() int           { return len(s.vals) }
func (s *S) Less(i, j int) bool { return s.vals[i] < s.vals[j] }
func (s *S) Swap(i, j int)      { s.vals[i], s.vals[j] = s.vals[j], s.vals[i] }
s := &S{vals: []int{3, 1, 2}}
sort.Sort(s)
s.vals[0]*100 + s.vals[1]*10 + s.vals[2]`, res: "123"},

		// flag.Value bridge: interpreted type passed to native flag.Var
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

		// sort.Slice with interpreted less function
		{n: "sort_slice", src: `
import "sort"
s := []int{3, 1, 4, 1, 5}
sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
s[0]*10000 + s[1]*1000 + s[2]*100 + s[3]*10 + s[4]`, res: "11345"},

		// native interface conversion: wrap interpreted *T in native io.Reader
		{n: "native_iface_conv", src: `
import "io"
type T struct { r io.Reader }
func (t *T) Read(p []byte) (int, error) { return t.r.Read(p) }
x := io.LimitedReader{}
y := io.Reader(&x)
y = &T{y}
n, _ := y.Read([]byte(""))
n`, res: "0"},

		// io.Writer bridge: interpreted type with Write passed to fmt.Fprint
		{n: "writer_bridge", src: `
import "fmt"
type T []byte
func (t *T) Write(p []byte) (n int, err error) { *t = append(*t, p...); return len(p), nil }
x := T{}
fmt.Fprint(&x, "hello")
fmt.Sprintf("%s", x)`, res: "hello"},

		// io.Writer bridge after type assertion
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

		// append concrete value to interface-typed slice
		{n: "append_iface_slice", src: `
import "io"
type B []byte
func (b B) Write(p []byte) (int, error) { return len(p), nil }
b := B{}
a := make([]io.Writer, 0)
a = append(a, b)
len(a)`, res: "1"},

		// interface embedding a package-qualified interface
		{n: "embed_pkg_iface", src: `
import "io"
type Transport interface { io.Reader }
type T struct{}
func (t *T) Read(p []byte) (int, error) { return 0, nil }
var tr Transport = &T{}
_, err := tr.Read(nil)
err == nil`, res: "true"},

		// interface embedding multiple package-qualified interfaces
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

		// user-defined interface embedding native interfaces, concrete type is native
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

		// method promoted from embedded interface called on struct (direct and through field chain)
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

		// method promoted from embedded native interface with return value assignment
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

		// struct field of interpreted type implementing io.Reader passed to native io.ReadAll
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

		// bridge callback with named returns and interface return type (yaegi-issue-558)
		{n: "bridge_named_return_iface", src: `
import "io"
import "strings"
type myReader struct{ r io.Reader }
func (m myReader) Read(b []byte) (n int, err error) {
	n, err = m.r.Read(b)
	return n, err
}
r := myReader{strings.NewReader("hello")}
b, _ := io.ReadAll(r)
string(b)`, res: "hello"},

		{n: "bridge_writerto_upgrade", src: `
import "io"
import "strings"
type WR struct{ r io.Reader; used bool }
func (w *WR) Read(p []byte) (int, error) { return w.r.Read(p) }
func (w *WR) WriteTo(dst io.Writer) (int64, error) {
	w.used = true
	data, _ := io.ReadAll(w)
	n, err := dst.Write(data)
	return int64(n), err
}
wr := &WR{r: strings.NewReader("hello")}
io.Copy(io.Discard, wr)
wr.used`, res: "true"},

		// Variadic interface spread: both indexed element and spread (yaegi-issue-1205 scenario).
		{n: "iface_slice_spread_both", src: `
type Option interface { val() int }
type T struct{ v int }
func (t *T) val() int { return t.v }
func f(opts ...Option) int { return opts[0].val() }
opt := []Option{&T{v: 21}}
a := f(opt[0])
b := f(opt...)
a + b`, res: "42"},

		// Function values stored in map[string]interface{} must be callable by native code.
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

		// struct field name same as a type name, type-assert on interface field (yaegi-issue-1320)
		{n: "iface_field_same_name_as_type", src: `
type Pooler interface { Get() string }
type baseClient struct { connPool Pooler }
type connPool struct { name string }
func (c *connPool) Get() string { return c.name }
func newBaseClient(p Pooler) *baseClient { return &baseClient{connPool: p} }
func newConnPool() *connPool { return &connPool{name: "connPool"} }
b := newBaseClient(newConnPool())
b.connPool.(*connPool).name`, res: "connPool"},

		// type assertion on interface value stored in struct field
		{n: "iface_struct_field_assert", src: `
import "io"
import "strings"
type rac struct { r io.ReadCloser }
a := rac{io.NopCloser(strings.NewReader("test"))}
_, ok := a.r.(io.Closer)
ok`, res: "true"},

		// type assertion must return false when concrete type lacks the method
		{n: "iface_assert_wrong_iface", src: `
import "io"
import "strings"
func f(r io.Reader) bool { _, ok := r.(io.Closer); return ok }
f(strings.NewReader("test"))`, res: "false"},

		// type assertion to unrelated concrete type must not panic
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

		// Native interface in type switch: *os.File implements io.Reader.
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

		// Interface slice composite literal: element stored as vm.Iface, callable via IfaceCall.
		{n: "iface_slice_index", src: `
type Option interface { val() int }
type T struct{ v int }
func (t *T) val() int { return t.v }
opt := []Option{&T{v: 7}}
opt[0].val()`, res: "7"},

		// Variadic interface spread: pass []Option directly with opt...
		{n: "iface_spread", src: `
type Option interface { val() int }
type T struct{ v int }
func (t *T) val() int { return t.v }
func f(opts ...Option) int { return opts[0].val() }
opt := []Option{&T{v: 42}}
f(opt...)`, res: "42"},

		// Native interface values in type switch (e.g. from json.Unmarshal map).
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

		// yaegi-issue-1465: &genericParam through any(...).(type) must
		// propagate writes back to the original local via AddrLocal aliasing.
		{n: "generic_ptr_writeback", src: `
func F[T int | string](x T) T {
	switch v := any(&x).(type) {
	case *int: *v = 999
	case *string: *v = "Z"
	}
	return x
}
F(42)`, res: "999"},
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
		{n: "iface_slice_display", src: `import "fmt"; type T struct{name string}; fmt.Sprintf("%v", []interface{}{T{"hello"}})`, res: `[{hello}]`},
		// Inline closure as composite literal field value.
		{n: "func_field_closure", src: `type T struct{ F func() int }; s := T{F: func() int { return 42 }}; s.F()`, res: `42`},
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
	})
}

func TestMethod(t *testing.T) {
	run(t, []etest{
		// Value receiver, direct call.
		{n: "#00", src: `type I int; func(i I) F(a int) int { return a+i }; var i I = 1; i.F(2)`, res: "3"},
		// Multiple params.
		{n: "#01", src: `type I int; func(i I) Add(a, b int) int { return a + b }; var i I = 0; i.Add(3, 4)`, res: "7"},

		// Read single field.
		{n: "#02", src: `type T struct{n int}; func(t T) N() int { return t.n }; x := T{5}; x.N()`, res: "5"},
		// Read field, add param.
		{n: "#03", src: `type T struct{n int}; func(t T) Add(a int) int { return t.n + a }; x := T{3}; x.Add(4)`, res: "7"},
		// Two fields.
		{n: "#04", src: `type T struct{a, b int}; func(t T) Sum() int { return t.a + t.b }; x := T{2, 3}; x.Sum()`, res: "5"},

		// Store method value, call later.
		{n: "#05", src: `type I int; func(i I) F(a int) int { return a+i }; var i I = 2; f := i.F; f(3)`, res: "5"},
		// Two independent method values from different receivers.
		{n: "#06", src: `type I int; func(i I) Val() I { return i }; var a I = 1; var b I = 2; fa := a.Val; fb := b.Val; fa() + fb()`, res: "3"},
		// Pass method value to higher-order function.
		{n: "#07", src: `type I int; func(i I) F(a int) int { return a+i }; apply := func(f func(int) int, n int) int { return f(n) }; var i I = 5; apply(i.F, 3)`, res: "8"},
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

		// Method expression: value receiver.
		{n: "mexpr_val", src: `type T struct{n int}; func(t T) Add(a int) int { return t.n + a }; T.Add(T{3}, 4)`, res: "7"},
		// Method expression: pointer receiver.
		{n: "mexpr_ptr", src: `type T struct{n int}; func(t *T) Get() int { return t.n }; (*T).Get(&T{n: 5})`, res: "5"},

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
	})
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
		// Channel variable reassigned after goroutine start: goroutine must keep the original channel.
		{n: "chan_reassign_after_goroutine", src: `
func sendTo(ch chan<- int, v int) { ch <- v }
ch := make(chan int)
go sendTo(ch, 42)
orig := ch
ch = make(chan int)
<-orig`, res: "42"},
		// Function values through channels.
		{n: "chan_func_named", src: `func f() int { return 42 }; ch := make(chan func() int, 1); ch <- f; g := <-ch; g()`, res: "42"},
		{n: "chan_func_closure", src: `x := 10; f := func() int { return x }; ch := make(chan func() int, 1); ch <- f; g := <-ch; g()`, res: "10"},
		{n: "chan_func_goroutine", src: `func f(n int) int { return n * 2 }; ch := make(chan func(int) int, 1); go func() { ch <- f }(); g := <-ch; g(21)`, res: "42"},
		// Daisy-chain goroutines (sieve-style): channel pipeline where ch is reassigned each iteration.
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
