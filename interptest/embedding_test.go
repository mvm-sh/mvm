package interptest

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// TestRemoteGoEmbed verifies //go:embed of a single sibling file into a
// package-level []byte and a string var: the parser reads the file from the
// package FS and installs its bytes as the var's initial value. Before embed
// support both vars were empty, which is why protobuf's editiondefaults
// panicked "unsupported edition" (its embedded Defaults was nil).
func TestRemoteGoEmbed(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/assets",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod":    "module example.com/x/assets\n",
			"data.bin":  "hello\x00\x01\x02bytes", // 13 bytes incl. NUL
			"greet.txt": "hi there",
			"assets.go": `package assets

import _ "embed"

//go:embed data.bin
var Blob []byte

//go:embed greet.txt
var Greeting string

func BlobLen() int  { return len(Blob) }
func Greet() string { return Greeting }
`,
		},
	})

	var stdout bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/assets"
println(assets.BlobLen(), assets.Greet())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "13 hi there\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteGoEmbedNamedType verifies //go:embed into a NAMED string type with a
// value-receiver method (golang.org/x/net/publicsuffix's uint40String pattern).
// Two bugs were exercised: (1) the embed scanner rejected named string/[]byte types,
// leaving the var empty; (2) the embed materialized the var's rtype before its methods
// were registered, reserving a method-less identity that broke ptr-method attach.
func TestRemoteGoEmbedNamedType(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/table",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod":    "module example.com/x/table\n",
			"nodes.bin": "\x00\x00\x00\x00\x01\x00\x00\x00\x00\x02", // two 5-byte nodes
			"table.go": `package table

import _ "embed"

type uint40String string

func (u uint40String) get(i uint32) uint64 {
	off := uint64(i * 5)
	u = u[off:]
	return uint64(u[4]) | uint64(u[3])<<8 | uint64(u[2])<<16 | uint64(u[1])<<24 | uint64(u[0])<<32
}

//go:embed nodes.bin
var nodes uint40String

func Node(i uint32) uint64 { return nodes.get(i) }
func Len() int            { return len(nodes) }
`,
		},
	})

	var stdout bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/table"
println(table.Len(), table.Node(0), table.Node(1))`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "10 1 2\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// A method promoted through an embedded interface returns a named interface;
// chaining a call on that result must compile.
func TestEmbedIfaceMethodChain(t *testing.T) {
	src := `
		type Oneof interface{ Name() string }
		type Field interface{ ContainingOneof() Oneof }
		type Fields interface { Len() int; Get(i int) Field }
		type Message interface{ Fields() Fields }
		type ranger struct{ Message }
		func (m ranger) firstOneof() string {
			fds := m.Fields()
			for i := 0; i < fds.Len(); i++ {
				fd := fds.Get(i)
				if o := fd.ContainingOneof(); o != nil {
					return o.Name()
				}
			}
			return ""
		}
		1 + 1
	`
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	if _, err := intp.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
}

// A struct embedding a method-bearing interface needs a synth reservation so the
// promoted EmbedIface methods can be attached (a multi-field struct gets no
// reflect.StructOf method promotion). The reserve gate skipped interface embeds,
// betting on StructOf promotion; when the struct was materialized before
// propagateEmbeddedMethods filled its method table (here FileImport is reached
// via the FileImports.Get signature during materializeIfaceMethods, ahead of the
// first propagate), it stayed unreserved and attach failed with
// "synth: value-method type ... has no reservation at attach".
// Was google.golang.org/protobuf/reflect/protoreflect (FileImport).
func TestEmbedIfaceReservedBeforePropagate(t *testing.T) {
	src := `
		type isDesc interface{ ProtoType() }
		type baseA interface { Name() string; Path() string }
		type baseB interface { Index() int; FullName() string }
		type FileDescriptor interface {
			baseA
			baseB
			isDesc
		}
		type FileImport struct {
			FileDescriptor
			IsPub  bool
			IsWeak bool
		}
		type FileImports interface {
			Len() int
			Get(i int) FileImport
		}
		var gFI = FileImport{IsWeak: true}
		func get() bool { return gFI.IsWeak }
		get()
	`
	intp := interp.NewInterpreter(golang.GoSpec)
	intp.ImportPackageValues(stdlib.Values)
	r, err := intp.Eval("test", src)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got := r.Bool(); got != true {
		t.Errorf("got %v, want true", got)
	}
}

// A struct embeds a named slice type whose own element type is forward
// referenced (defined later in the unit). The embedded field's type is cloned
// at parse time while still an empty placeholder; its Base is only materialized
// afterwards. Indexing the embedded field in a method body used to read the
// thin clone (Kind=Invalid) and nil-deref in Type.Elem. resolveFieldByPath now
// adopts the field type's materialized Base. Shape mirrors gonum kdtree's
// nbPlane{Dim; nbPoints} with `type nbPoint Point` defined across files.
func TestEmbeddedForwardFieldIndex(t *testing.T) {
	src := `package main

import "fmt"

type Dim int

type Comparable interface {
	Compare(Comparable, Dim) float64
}

var _ Comparable = nbPoint{}

type nbPoint Point

func (p nbPoint) Compare(c Comparable, d Dim) float64 { q := c.(nbPoint); return p[d] - q[d] }

type nbPoints []nbPoint

type nbPlane struct {
	Dim
	nbPoints
}

func (p nbPlane) Less(i, j int) bool { return p.nbPoints[i][p.Dim] < p.nbPoints[j][p.Dim] }

type Point []float64

func main() {
	pl := nbPlane{Dim: 0, nbPoints: nbPoints{{1, 2}, {3, 4}}}
	fmt.Println(pl.Less(0, 1))
}
`
	if got := evalOut(t, "embedfwd", src); got != "true\n" {
		t.Errorf("got %q, want %q", got, "true\n")
	}
}

// Regression for `mvm test github.com/samber/lo` -> x/text/unicode/norm
// "undefined: info".
//
// An elided `{...}` element of a []*T literal denotes &T{...}. The BraceBlock
// inference registered the composite under the pointer element type *T instead
// of the pointee T. Because a SymPtr carries the pointee's name (it doubles as
// the embedded-field name), *T's String() rendered as "T", so registerType
// re-keyed the *T type under "T" and OVERWROTE the real T struct symbol with a
// fieldless pointer type. A later field access on T (here through a method on
// *T) then failed "undefined: <field>". Fixed by derefing the pointer element
// so the composite type is the pointee T, which the compiler auto-addresses.
func TestElidedPointerSliceComposite(t *testing.T) {
	const src = `package main

import "fmt"

type formInfo struct {
	form int
	info func(int) int
}

func lookup(i int) int { return i + 1 }

var formTable = []*formInfo{{form: 1, info: lookup}}

func (f *formInfo) run(i int) int {
	info := f.info(i)
	return f.form + info
}

func main() {
	fmt.Print(formTable[0].run(5))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("repro.go", src); err != nil {
		t.Fatalf("eval: %v\nstderr: %s", err, stderr.String())
	}
	// formTable[0].run(5) = form(1) + info(lookup(5)=6) = 7.
	if got := strings.TrimSpace(stdout.String()); got != "7" {
		t.Errorf("got %q, want %q (stderr: %s)", got, "7", stderr.String())
	}
}

// A struct field of pointer type (`mat *BandDense`) is parsed as a clone of
// the *BandDense mtype carrying the FIELD name. When the pointer type also
// carries methods (registerMethods filled them for an interface check), the
// reserve gate minted a named rtype from the field name: reflect.TypeOf on a
// field select reported "main.mat" and DeepEqual against a plain *BandDense
// failed (gonum/mat TestNewBand and friends). A non-defined clone now keeps
// the base layout.
func TestFieldClonePtrKeepsBaseIdentity(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

type Matrix interface {
	Dims() (int, int)
}

type Band struct{ Rows int }

type BandDense struct{ mat Band }

func (m *BandDense) Dims() (int, int) { return m.mat.Rows, m.mat.Rows }

var _ Matrix = (*BandDense)(nil)

func main() {
	tests := []struct {
		mat *BandDense
	}{
		{mat: &BandDense{mat: Band{1}}},
	}
	b := &BandDense{mat: Band{1}}
	fmt.Println(reflect.TypeOf(tests[0].mat))
	fmt.Println(reflect.TypeOf(tests[0].mat) == reflect.TypeOf(b))
	fmt.Println(reflect.DeepEqual(b, tests[0].mat))
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "*main.BandDense\ntrue\ntrue\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// Self-referential named composites (type P *P / S []S / M map[int]M) must
// parse, materialize (donor layout + SetElem patch), and behave like gc.
// Was go-cmp cycleTests: "undefined: P".
func TestSelfRefNamedComposites(t *testing.T) {
	src := `
package main

import "fmt"

type (
	P *P
	S []S
	M map[int]M
)

func main() {
	x := new(P)
	*x = x
	fmt.Println(*x == x, **x == x)

	s := S{nil}
	s[0] = s
	fmt.Println(len(s[0][0][0]) == 1)

	m := M{0: nil}
	m[0] = m
	fmt.Println(len(m[0][0]) == 1)

	fmt.Printf("%T %T %T\n", x, s, m)
}
`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("selfref", src); err != nil {
		t.Fatalf("eval: %v", err)
	}
}

// Two same-named func-local self-ref types must get distinct identities; a
// shared-carrier cache keyed on the donor layout collided them.
func TestSelfRefSameNameNoCollision(t *testing.T) {
	src := `
package main

import "fmt"

func f1() any {
	type S []S
	s := S{nil}
	s[0] = s
	return s
}

func f2() any {
	type S [][]S
	return S{}
}

func main() {
	a, b := f1(), f2()
	fmt.Printf("%T %T\n", a, b)
}
`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("selfref_collision", src); err != nil {
		t.Fatalf("eval: %v", err)
	}
}

// Self-ref map with an array elem ([2]M is 2 ptr words; donor uses a
// same-shape stand-in). Was an internal "Type on zero Value" panic.
func TestSelfRefMapArrayElem(t *testing.T) {
	src := `
package main

import "fmt"

type M map[int][2]M

func main() {
	m := M{0: [2]M{nil, nil}}
	m[0] = [2]M{m, nil}
	if len(m) != 1 || len(m[0][0]) != 1 {
		panic("cycle broken")
	}
	fmt.Println("ok")
}
`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("selfref_map_array", src); err != nil {
		t.Fatalf("eval: %v", err)
	}
}

// Equality across mixed rtypes must match gc: same-pointee pointers compare
// by address (P vs *P), different pointees at the same address stay inequal,
// and named vs unnamed struct/array with identical underlying compare equal.
func TestMixedRtypeEquality(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"selfref_ptr", `type P *P; f := func() bool { x := new(P); *x = x; return *x == x && **x == x }; fmt.Sprint(f())`, "true"},
		{"diff_pointee_same_addr", `type S struct{ F int; G string }; f := func() bool { s := S{1, "x"}; var a any = &s; var b any = &s.F; return a == b }; fmt.Sprint(f())`, "false"},
		{"named_vs_unnamed_array", `type A [2]int; a1 := A{1, 2}; a2 := [2]int{1, 2}; fmt.Sprint(a1 == a2)`, "true"},
		{"named_vs_unnamed_struct", `type T struct{ X int }; s1 := T{1}; s2 := struct{ X int }{1}; fmt.Sprint(s1 == s2)`, "true"},
		{"named_vs_unnamed_struct_ne", `type T struct{ X int }; s1 := T{1}; s2 := struct{ X int }{2}; fmt.Sprint(s1 == s2)`, "false"},
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

// Elided composite literals in map literals: the key needs &T{} addressing
// and the value must re-infer its own type ident ({k}: {v} shared a stale
// ctype). Was go-cmp StringerMapKey: compile-stack underflow.
func TestElidedMapComposites(t *testing.T) {
	cases := []struct{ n, src, res string }{
		{"key_and_value", `type T struct{ X string }; m := map[*T]*T{{"hello"}: {"world"}}; r := ""; for k, v := range m { r = k.X + v.X }; r`, "helloworld"},
		{"key_only", `type T struct{ X string }; m := map[*T]string{{"x"}: "a"}; r := ""; for k, v := range m { r = k.X + v }; r`, "xa"},
		{"value_only", `type T struct{ X string }; m := map[string]*T{"a": {"x"}}; m["a"].X`, "x"},
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

// A map write whose key was read from an unexported field carries flagRO;
// MapSet must strip it. Was go-cmp resolveReferences SetMapIndex panic.
func TestMapSetUnexportedKey(t *testing.T) {
	src := `
package main

import (
	"fmt"
	"reflect"
)

type ptr struct {
	p uintptr
	t reflect.Type
}

type leaf struct{ p ptr }

type node struct{ Metadata any }

func main() {
	v := 42
	n := &node{Metadata: leaf{p: ptr{p: reflect.ValueOf(&v).Pointer(), t: reflect.TypeOf(&v)}}}
	seen := make(map[ptr]bool)
	seen[n.Metadata.(leaf).p] = true
	fmt.Println(len(seen))
}
`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("rokey", src); err != nil {
		t.Fatalf("eval: %v", err)
	}
}

// A synth *T boxed in an interface map key must be hashable: derived and
// reserved pointer rtypes carry tflagRegularMemory or runtime.typehash
// panics "hash of unhashable type" when the map crosses the bridge.
func TestSynthPtrIfaceKeyHash(t *testing.T) {
	src := `
package main

import (
	"fmt"
	"reflect"
)

type Stringer string

func (s Stringer) String() string { return string(s) }

func newStringer(s string) fmt.Stringer { return (*Stringer)(&s) }

func main() {
	y := map[interface{}]string{newStringer("hello"): "goodbye"}
	var i any = y
	fmt.Println(reflect.ValueOf(i).Len())
}
`
	i := newAutoImportInterp(t)
	if _, err := i.Eval("ptrhash", src); err != nil {
		t.Fatalf("eval: %v", err)
	}
}

// Regression for `mvm test golang.org/x/net/html` -> reflect.Set panic
// "value of type html.insertionMode is not assignable to type func()".
//
// insertionMode is `func(*parser) bool` and parser has insertionMode fields plus
// an insertionModeStack ([]insertionMode) field: a cycle parser -> im -> func(*parser)
// -> parser. Materializing the func type first leaves it served as the erased func()
// placeholder while parser's layout (struct field and slice elem) is built, baking
// func() in place of the real signature. The leak was materialization-order
// dependent (map iteration), so a fresh interp per iteration eventually hits the
// bad order; loop to make the regression deterministic.
func TestSelfRefFuncFieldMaterialize(t *testing.T) {
	src := `package main

import "fmt"

type token struct {
	kind int
	data string
}

type insertionMode func(*parser) bool

type imodeStack []insertionMode

type parser struct {
	tok        token
	stack      imodeStack
	im         insertionMode
	originalIM insertionMode
	scripting  bool
}

func (s *imodeStack) push(m insertionMode) { *s = append(*s, m) }
func (s *imodeStack) pop() (m insertionMode) {
	i := len(*s)
	m = (*s)[i-1]
	*s = (*s)[:i-1]
	return m
}
func (s *imodeStack) top() insertionMode {
	if i := len(*s); i > 0 {
		return (*s)[i-1]
	}
	return nil
}

func (p *parser) run() bool { return p.im(p) }

func initialIM(p *parser) bool { return true }
func inBodyIM(p *parser) bool  { return false }

func main() {
	p := &parser{scripting: true, im: initialIM}
	p.originalIM = p.im
	p.stack.push(initialIM)
	p.stack.push(inBodyIM)
	p.im = p.stack.top()
	got := p.run()
	m := p.stack.pop()
	p.im = p.stack.top()
	fmt.Println(got, m(p), p.run(), p.originalIM(p))
}
`
	const want = "false false true true\n"
	for iter := range 40 {
		var stdout, stderr bytes.Buffer
		i := interp.NewInterpreter(golang.GoSpec)
		i.ImportPackageValues(stdlib.Values)
		i.ImportPackageConsts(stdlib.ConstValues)
		i.SetIO(os.Stdin, &stdout, &stderr)
		if _, err := i.Eval("selfref_func.go", src); err != nil {
			t.Fatalf("iter %d: Eval: %v\nstderr: %s", iter, err, stderr.String())
		}
		if got := stdout.String(); !strings.HasSuffix(got, want) {
			t.Fatalf("iter %d: stdout: got %q, want suffix %q (stderr: %s)", iter, got, want, stderr.String())
		}
	}
}

// Calling a promoted method on a nil pointer receiver derefs nil to reach the
// embedded field. That is a recoverable nil-pointer dereference in Go, not a
// host reflect "Field on zero Value" panic that escapes the interpreter.
// Was github.com/kr/pretty TestGoSyntax ((*VGSWrapper)(nil)).
func TestPromotedMethodNilReceiver(t *testing.T) {
	const wantErr = "runtime error: invalid memory address or nil pointer dereference"
	cases := []struct{ name, src string }{
		// Concrete value embed -> promoted value-receiver method (IfaceCall path).
		{"concrete_embed", `
			type inner struct{ s string }
			func (i inner) Name() string { return "N:" + i.s }
			type outer struct{ inner }
			type namer interface{ Name() string }
			func run() (out string) {
				defer func() {
					if r := recover(); r != nil {
						if e, ok := r.(error); ok { out = e.Error() } else { out = "non-error" }
					}
				}()
				var p *outer = nil
				var n namer = p
				_ = n.Name()
				return "nopanic"
			}
			run()
		`},
		// Embedded interface -> promoted method (EmbedIface path).
		{"iface_embed", `
			type sounder interface{ Sound() string }
			type wrap struct{ sounder }
			func run() (out string) {
				defer func() {
					if r := recover(); r != nil {
						if e, ok := r.(error); ok { out = e.Error() } else { out = "non-error" }
					}
				}()
				var p *wrap = nil
				var s sounder = p
				_ = s.Sound()
				return "nopanic"
			}
			run()
		`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			intp := interp.NewInterpreter(golang.GoSpec)
			intp.ImportPackageValues(stdlib.Values)
			r, err := intp.Eval(c.name, c.src)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if got := r.String(); got != wantErr {
				t.Errorf("got %q, want %q", got, wantErr)
			}
		})
	}
}

// A pointer-receiver method (Push) is promoted from an embedded value field
// (Heap) and reached through the native container/heap bridge. The receiver
// must be a pointer to the real embedded field so append propagates back into
// the owning struct; makeMethodCell used to box a copy, so heap.Push silently
// dropped every element. Shape mirrors gonum kdtree's DistKeeper{Heap}.
func TestPromotedPtrRecvHeapWriteback(t *testing.T) {
	src := `package main

import (
	"container/heap"
	"fmt"
)

type Item struct{ Dist float64 }
type Heap []Item

func (h *Heap) Len() int             { return len(*h) }
func (h *Heap) Less(i, j int) bool   { return (*h)[i].Dist > (*h)[j].Dist }
func (h *Heap) Swap(i, j int)        { (*h)[i], (*h)[j] = (*h)[j], (*h)[i] }
func (h *Heap) Push(x interface{})   { (*h) = append(*h, x.(Item)) }
func (h *Heap) Pop() (i interface{}) { i, *h = (*h)[len(*h)-1], (*h)[:len(*h)-1]; return i }

type DistKeeper struct {
	Heap
}

func (k *DistKeeper) Keep(c Item) {
	if c.Dist <= k.Heap[0].Dist {
		heap.Push(k, c)
	}
}

func main() {
	k := &DistKeeper{Heap{{Dist: 100}}}
	k.Keep(Item{Dist: 5})
	k.Keep(Item{Dist: 8})
	k.Keep(Item{Dist: 3})
	fmt.Println(len(k.Heap))
}
`
	if got := evalOut(t, "promoheap", src); got != "4\n" {
		t.Errorf("got %q, want %q", got, "4\n")
	}
}
