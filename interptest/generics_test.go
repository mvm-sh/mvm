package interptest

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Inside a non-main package, `fn := func(any) any {...}; generic(i, fn)`
// failed "cannot infer type parameter T": postfixType's closure-operand
// lookup used the bare anon name while closure symbols are keyed per-package
// (anonFuncKey), so fn parsed with a nil type (spf13/cast ToStringMapE).
func TestGenericInferClosureVarInPkg(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/conv",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/conv\n",
			"conv.go": `package conv

func toMapE[T any](i any, fn func(any) T) map[string]T {
	m := map[string]T{}
	if mm, ok := i.(map[string]any); ok {
		for k, v := range mm {
			m[k] = fn(v)
		}
	}
	return m
}

func ToMap(i any) map[string]any {
	fn := func(i any) any { return i }
	return toMapE(i, fn)
}
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/conv"
)

func main() {
	fmt.Println(conv.ToMap(map[string]any{"a": 1}))
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "map[a:1]\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// Regression for `mvm test github.com/samber/lo` -> retry.go
// "cannot infer type parameter T".
//
// `callbacks := Map(f, func(...) func(struct{}) {...})` then
// `NewThrottleByWithCount(interval, count, callbacks...)` must infer T=struct{}
// from callbacks's type. The inference reads the arg type via callFuncType /
// postfixType, which walk a parsed postfix right-to-left to locate the callee.
// A closure argument is emitted inline as its whole definition block
// (`Goto X_end; Label X; ...body...; Label X_end` then the value Ident "X"),
// and postfixType consumed only the trailing closure ident, derailing the arg
// walk. callFuncType then returned nil, leaving callbacks's type nil, so the
// later generic call could not infer T. Fixed by consuming the entire closure
// block as one operand in postfixType.
func TestGenericInferFromGenericCallResult(t *testing.T) {
	src := `package main

import "fmt"

func Map[T, R any](collection []T, transform func(item T, index int) R) []R {
	result := make([]R, len(collection))
	for i := range collection {
		result[i] = transform(collection[i], i)
	}
	return result
}

func Collect[T comparable](count int, f ...func(key T)) int {
	return len(f) + count
}

func main() {
	f := []func(){func() {}, func() {}}
	callbacks := Map(f, func(item func(), _ int) func(struct{}) {
		return func(struct{}) { item() }
	})
	fmt.Println(Collect(1, callbacks...))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("infer_call_result.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	// len(callbacks)=2 + count(1) = 3.
	if got := stdout.String(); got != "3\n" {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, "3\n", stderr.String())
	}
}

// Regression for `mvm test github.com/microcosm-cc/bluemonday` ->
// golang.org/x/net/html/escape.go `slices.Index(b, '&')`:
// "type []uint8 does not satisfy constraint".
//
// Go infers generic type params from typed args and constraints first, using an
// untyped constant's default type only as a fallback. mvm bound E from '&'
// (default rune) in the first pass, shadowing E=byte from the S ~[]E constraint,
// so the [S ~[]E] check then saw []uint8 ~ []int32 and failed. Untyped-const
// args are now deferred to a fallback pass after constraint inference.
func TestGenericInferUntypedConstDeferred(t *testing.T) {
	src := `package main

import "fmt"

func Index[S ~[]E, E comparable](s S, v E) int {
	for i := range s {
		if s[i] == v {
			return i
		}
	}
	return -1
}

func First[T any](x T) T { return x }

func main() {
	b := []byte("a&b")
	fmt.Println(Index(b, '&')) // E=byte from S, not rune from '&'
	i8 := []int8{1, 2, 3}
	fmt.Println(Index(i8, 2)) // E=int8 from S, not int from 2
	fmt.Println(First(5))     // fallback: T=int from the untyped const
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("infer_untyped_const.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "1\n1\n5\n"; got != want {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

// An inline constraint that begins with a composite type literal (`[]byte`,
// `map[K]V`, ...) was rejected as "undefined: f": parseTypeParamList only
// accepted Ident/Interface/Tilde as the first constraint token, so the function
// never registered as generic (go.uber.org/zap safeAppendStringLike).
func TestGenericCompositeTypeConstraint(t *testing.T) {
	src := `package main

import "fmt"

type Buf struct{ b []byte }

func (z *Buf) AppendString(s string) { z.b = append(z.b, s...) }

func appendLike[S []byte | string](appendTo func(*Buf, S), buf *Buf, s S) {
	appendTo(buf, s)
}

func mapID[M map[string]int](m M) M { return m }

func main() {
	b := &Buf{}
	appendLike((*Buf).AppendString, b, "hi") // S inferred from the func-typed arg
	fmt.Println(string(b.b))
	fmt.Println(mapID(map[string]int{"a": 1})["a"])
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("composite_constraint.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "hi\n1\n"; got != want {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

// `a, _ := iface.Method()` must type `a` from the interface method's return
// tuple so a later generic call can infer from it. Repro of jwt v5's
// verifyAudience: `aud, _ := claims.GetAudience(); slices.Contains(aud, x)`
// where claims is an interface and GetAudience returns a named []string + error.
// callFuncType returned nil for method calls (only pkg-qualified/free funcs),
// and postfixType's bare method-value path missed interface methods.
func TestGenericInferFromInterfaceMethodMultiReturn(t *testing.T) {
	src := `package main

import (
	"fmt"
	"slices"
)

type ClaimStrings []string

type Claims interface {
	GetAudience() (ClaimStrings, error)
}

type myClaims struct{}

func (myClaims) GetAudience() (ClaimStrings, error) { return ClaimStrings{"a", "b"}, nil }

func check(c Claims, want string) bool {
	aud, _ := c.GetAudience()
	return slices.Contains(aud, want)
}

func main() {
	fmt.Println(check(myClaims{}, "b"))
	fmt.Println(check(myClaims{}, "z"))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("iface_method_multireturn_infer.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "true\nfalse\n"; got != want {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

// Constraint type inference: a type param appearing only in its own constraint's
// core type (P in [T any, P PtrMarshaler[T]] where PtrMarshaler[T] = interface{*T;...})
// is constructed by substituting the inferred T -> *T. Repro of zap's
// ObjectValues[T any, P ObjectMarshalerPtr[T]] (array_test.go, example_test.go),
// covering both partial-explicit (Values[obj]) and full inference (Values(arg)).
func TestGenericConstraintTypeInference(t *testing.T) {
	src := `package main

import "fmt"

type Marshaler interface{ Marshal() string }

type PtrMarshaler[T any] interface {
	*T
	Marshaler
}

func Values[T any, P PtrMarshaler[T]](values []T) string {
	var p P
	_ = p
	return fmt.Sprint(len(values))
}

func Direct[T any, P *T](values []T) int { return len(values) }

type obj struct{ x int }

func (o *obj) Marshal() string { return "obj" }

func main() {
	fmt.Println(Values[obj](nil))         // P inferred from *T (T explicit)
	fmt.Println(Values([]obj{{1}, {2}}))  // T from arg, then P from *T
	fmt.Println(Direct([]obj{{1}, {2}})) // direct [P *T] core-type constraint
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("constraint_type_inference.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "0\n2\n2\n"; got != want {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

// `type T[P *int]` is NOT a generic type: gc parses the brackets as an array size
// `[P * int]`, so P is undefined in the body. Allowing Mul as a constraint-start
// in the type-decl path wrongly accepted it as generic; beginsTypeConstraint must
// exclude Mul there (arrayAmbiguous) while still accepting `func f[P *int]`, which
// is valid Go (a func type-param list is never an array).
func TestPointerConstraintTypeDeclIsArray(t *testing.T) {
	bad := `package main

type Ptr[P *int] struct{ v P }

func main() { _ = Ptr[*int]{} }
`
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	if _, err := i.Eval("ptr_type.go", bad); err == nil {
		t.Errorf("expected error for `type Ptr[P *int]` (gc parses it as an array), got none")
	}

	// The func form is valid Go and must still work.
	good := `package main

import "fmt"

func deref[P *int](p P) int { return *p }

func main() {
	x := 5
	fmt.Println(deref(&x))
}
`
	var stdout, stderr bytes.Buffer
	j := interp.NewInterpreter(golang.GoSpec)
	j.ImportPackageValues(stdlib.Values)
	j.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := j.Eval("ptr_func.go", good); err != nil {
		t.Fatalf("func[P *int] Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "5\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// Two functions each declare a local type named Person; both are passed to the
// same generic function. The types share a PkgPath.Name (main.Person), so the
// generic instance used to be cached under one mangled name, binding the first
// instantiation's rtype to the body's swap temps. The second call then tripped
// `reflect.Set: value of type main.Person is not assignable to type
// main.Person`. Each distinct declaration must get its own monomorphization.
func TestGenericLocalTypeCollision(t *testing.T) {
	src := `package main

import "fmt"

func swapFirst[E any](data []E) {
	data[0], data[1] = data[1], data[0]
}

func first() {
	type Person struct {
		Name string
		Age  int
	}
	p := []Person{{"A", 1}, {"B", 2}}
	swapFirst(p)
	fmt.Println(p)
}

func second() {
	type Person struct {
		Name string
		Age  int
	}
	p := []Person{{"C", 3}, {"D", 4}}
	swapFirst(p)
	fmt.Println(p)
}

func main() {
	first()
	second()
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("generic_local.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "[{B 2} {A 1}]\n[{D 4} {C 3}]\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A generic type instantiated with a named type in a method SIGNATURE
// (gjson: func (t Result) All() iter.Seq2[Result, Result]) must not
// materialize the type argument at parse time: that runs before
// preregisterMethods, so the reserve gate would see no methods and stamp a
// methodless identity that AttachSynthMethods cannot fill ("has no
// reservation at attach"). Was github.com/tidwall/gjson.
func TestGenericMethodSigReserve(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/j",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/j\n",
			"j.go": `package j

type Seq2[K, V any] func(yield func(K, V) bool)

type Type int

func (t Type) String() string { return "x" }

type Result struct {
	Type Type
	Raw  string
}

func (t Result) String() string { return t.Raw }

func (t Result) All() Seq2[Result, Result] {
	return func(yield func(Result, Result) bool) {}
}
`,
		},
	})

	var stdout bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	src := `import "example.com/x/j"; var r j.Result; r.Raw = "hi"; println(r.String())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "hi\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A type parameter is inferred from an argument whose type is a named generic
// instantiation (here *node[T], a pointer to a generic struct). Inference used
// to only descend Pointer/Slice/Map/Func and leaf-match on Name, so a struct
// instantiation node[T] left T unbound -> "cannot infer type parameter T".
// This is the shape google/btree relies on: func max[T](n *node[T]) called as
// max(t.root) where t.root is *node[T].
func TestGenericInferFromNamedInstanceArg(t *testing.T) {
	src := `package main

import "fmt"

type node[T any] struct{ items []T }

func first[T any](n *node[T]) (_ T, found bool) {
	if n == nil {
		return
	}
	return n.items[0], true
}

type Tree[T any] struct{ root *node[T] }

func (t *Tree[T]) Min() (_ T, _ bool) {
	return first(t.root)
}

func main() {
	t := &Tree[int]{root: &node[int]{items: []int{7, 8}}}
	v, ok := t.Min()
	fmt.Println(v, ok)
}
`
	if got, want := evalOut(t, "infer_named_instance.go", src), "7 true\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A generic struct with a self-referential field whose type is itself a generic
// instantiation (children items[*node[T]]) gets instantiated from two distinct
// function contexts. The nested instance items[*node[int]] was registered under
// one mangled name but its method receiver was re-mangled later under a drifted
// name (a type arg's PkgPath is populated lazily), leaving the method's receiver
// type unresolved -> "cannot range over <nil>". The instance now snapshots its
// mangled name at registration, so method emission matches.
func TestGenericNestedInstanceMethodNoDrift(t *testing.T) {
	src := `package main

import "fmt"

type items[T any] []T

func (s items[T]) first() (T, bool) {
	for i := range s {
		return s[i], true
	}
	var z T
	return z, false
}

type node[T any] struct {
	items    items[T]
	children items[*node[T]]
}

func (n *node[T]) get() bool {
	_, ok := n.items.first()
	return ok
}

// references node[int] from a second function context (not just main)
func mk() *node[int] { return &node[int]{} }

func main() {
	_ = mk()
	n := &node[int]{items: items[int]{1, 2}}
	fmt.Println(n.get())
}
`
	if got, want := evalOut(t, "nested_instance_drift.go", src), "true\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A generic method body (Tree.Min) calls a generic free function (leftmost) whose
// own signature forward-references a type (ctx) declared later. leftmost's
// registration defers, but a defined type (ItemTree) eagerly parses Min's body
// first, so the call used to compile to a bare ref to the codeless template -> nil
// func panic at run time. This is the google/btree shape (BTreeG.Min -> min(t.root),
// copyOnWriteContext declared after node).
func TestGenericForwardRefFreeFuncInMethod(t *testing.T) {
	src := `package main

import "fmt"

type Item interface{ Less(o Item) bool }

type node[T any] struct {
	items    []T
	children []*node[T]
	cow      *ctx[T] // forward ref to ctx, declared below
}

func leftmost[T any](n *node[T]) (_ T, ok bool) {
	if n == nil {
		return
	}
	return n.items[0], true
}

type Tree[T any] struct {
	root *node[T]
	cow  *ctx[T]
}

func (t *Tree[T]) Min() (_ T, _ bool) { return leftmost(t.root) }

func NewG[T any]() *Tree[T] { return &Tree[T]{cow: &ctx[T]{}} }

type ctx[T any] struct{ less func(a, b T) bool }

type ItemTree Tree[Item]

func New() *ItemTree { return (*ItemTree)(NewG[Item]()) }

func main() {
	t := New()
	mn, ok := (*Tree[Item])(t).Min()
	fmt.Println(mn, ok)
}
`
	if got, want := evalOut(t, "fwdref_freefunc.go", src), "<nil> false\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// Regression for `mvm test github.com/samber/lo` -> "undefined: Must2".
//
// When a package is loaded as a test target, importingPkg is set to its path,
// so symGet prefers a canonical "<pkg>.<name>" symbol over a bare binding. A
// generic func's type parameter is installed only at its bare name while the
// signature is parsed, so a type param colliding with a package-level symbol of
// the same name (lo's `Must2[T1, T2 any]` next to a package func `T2`) resolved
// to that symbol: the signature parse failed with "T2 is not a type", leaving
// the template's Type nil, and any call reported "undefined: Must2". Fixed by
// installing the placeholders at the pkg-qualified keys too
// (bindTypeParamPlaceholders), mirroring bindTypeParams at instantiation.
func TestGenericTypeParamNameCollidesWithPackageSymbol(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/coll",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/coll\n",
			// Package-level func T2 collides with Must2's type parameter T2.
			"a.go": `package coll

func T2[A, B any](a A, b B) any { return a }

func Must2[T1, T2 any](v1 T1, v2 T2, err any) (T1, T2) {
	if err != nil {
		panic(err)
	}
	return v1, v2
}
`,
			// A separate file calls Must2, forcing inference + registration.
			"use.go": `package coll

func Use() (int, string) {
	return Must2(42, "hello", nil)
}
`,
		},
	})

	var stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.SetIncludeTests(true)

	// Direct-target load (mirrors test_cmd's `i.Eval(target, "")`), which sets
	// importingPkg = "example.com/x/coll".
	if _, err := i.Eval("example.com/x/coll", ""); err != nil {
		t.Fatalf("loading target: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "undefined") {
		t.Errorf("unexpected undefined error: %s", stderr.String())
	}
}

// Companion to the above for the constraint-parsing path: a type param named in
// a composite constraint (E in `[S ~[]E, E int]`) colliding with a package-level
// symbol of the same name. parseTypeParamList installed its placeholders at the
// bare key only, so `E` in `~[]E` resolved to the package func `E`, the
// signature parse failed, and the generic was reported undefined at its callsite.
// Fixed by routing parseTypeParamList through bindTypeParamPlaceholders too.
func TestGenericConstraintTypeParamNameCollidesWithPackageSymbol(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/coll2",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/coll2\n",
			// Package-level func E collides with the type param E used in ~[]E.
			"a.go": `package coll2

func E() int { return 0 }

func Min[S ~[]E, E int](s S) E { return s[0] }
`,
			"use.go": `package coll2

func Use() int { return Min([]int{3, 1, 2}) }
`,
		},
	})

	var stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.SetIncludeTests(true)

	if _, err := i.Eval("example.com/x/coll2", ""); err != nil {
		t.Fatalf("loading target: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "undefined") {
		t.Errorf("unexpected undefined error: %s", stderr.String())
	}
}

// A defined type over a generic instance (type BTree BTreeG[int]) shares the
// instance's canonical type, and both declare a same-named method.
// MethodByName's unnamed-receiver scan matched either by map order, so calls
// nondeterministically hit the wrong Delete: a 2-value := from a 1-result
// method underflowed codegen, and the other direction returned extra values
// (google/btree's backwards-compat wrappers).
func TestDefinedTypeOverGenericInstanceMethod(t *testing.T) {
	src := `package main

import "fmt"

type BTreeG[T any] struct{ n int }

func (t *BTreeG[T]) Delete(item T) (T, bool) {
	return item, true
}

type BTree BTreeG[int]

func (t *BTree) Delete(item int) int {
	i, _ := (*BTreeG[int])(t).Delete(item)
	return i
}

func main() {
	t := &BTree{}
	fmt.Println(t.Delete(5))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	if _, err := i.Eval("a.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "5\n"; got != want {
		t.Errorf("stdout = %q, want %q\nstderr: %s", got, want, stderr.String())
	}
}
