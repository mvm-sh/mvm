package interptest

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/interp"
	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Regression for `mvm test github.com/bmatcuk/doublestar/v4` ->
// TestSkipDirInGlobWalk corrupted the package-level SkipDir sentinel.
//
// The compiler materializes a local slot's own storage (vm.New) only at the
// first TEXTUAL assignment. A named return assigned first in a later branch
// reached SetLocal with an unmaterialized slot, so assignSlot adopted the
// source ref verbatim. When the source was a global's value returned by a
// callee, the slot aliased the global's storage and a following `e = nil`
// wrote through it, nulling the global for the rest of the run.
// Fixed in vm.assignSlot: never adopt a settable ref; detach into fresh storage.
func TestNamedReturnAssignNoGlobalAlias(t *testing.T) {
	src := `package main

import (
	"errors"
	"fmt"
)

var sentinel = errors.New("skip")
var word = "hello"

func getErr() error  { return sentinel }
func getStr() string { return word }

// The if-branch is the first textual assignment to e (it gets the slot-
// materializing vm.New); the else branch, executed here, does not.
func walk(which bool) (e error) {
	if which {
		if e = getErr(); e != nil {
			return
		}
	} else {
		if e = getErr(); e != nil {
			e = nil
			return
		}
	}
	return
}

func wstr(which bool) (s string) {
	if which {
		if s = getStr(); s != "" {
			return
		}
	} else {
		if s = getStr(); s != "" {
			s = ""
			return
		}
	}
	return
}

func main() {
	walk(false)
	wstr(false)
	fmt.Println(sentinel, word)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("named_return_alias.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "skip hello\n"
	if got := stdout.String(); got != want {
		t.Errorf("globals clobbered through a named-return slot: got %q, want %q", got, want)
	}
}

// Regression for `mvm test golang.org/x/text/language` -> ExampleMatcher.
//
// Named-return slots were zero-initialized only for struct/array/slice/map
// kinds; an unassigned scalar/string/interface named return left the slot an
// invalid Value{}, which crashed when the caller boxed the result into an
// interface (fmt.Println variadic pack): "reflect: Set on zero Value".
func TestNamedReturnUnassignedZero(t *testing.T) {
	src := `package main

import "fmt"

func bareInt() (i int)       { return }
func explInt() (i int)       { return i }
func bareStr() (s string)    { return }
func bareErr() (err error)   { return }
func bareMulti() (t string, index int, b byte) { return t, index, b }

func main() {
	fmt.Println(bareInt(), explInt(), bareStr(), bareErr())
	fmt.Println(bareMulti())
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("named_return_zero.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "0 0  <nil>\n 0 0\n"
	if got := stdout.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Regression for `mvm test github.com/samber/lo` -> ExampleNewDebounce panic
// `reflect.Value.Convert: *int cannot be converted to *int32`.
//
// A local captured by a closure is promoted to a heap cell. Taking its address
// (`&i`) was broken two ways: (1) for a `:=` var the cell inherited the value's
// generic ref, so a sized-numeric (`i := int32(0)`) yielded *int not *int32; the
// non-cell path masks this by typing the slot via vm.New, skipped for cells.
// (2) the cell's ref was non-addressable, so &i pointed at a transient copy and
// writes through it (atomic ops here) never reached the cell -- the closure and
// later reads saw the stale value. Fixed by converting `:=` numeric values to
// the declared type before HeapAlloc, and by making HeapAlloc detach numeric
// values into fresh addressable storage (enabling CellGet's num<-ref resync).
func TestCapturedVarAddressTypeAndWrite(t *testing.T) {
	src := `package main

import (
	"fmt"
	"sync/atomic"
)

func main() {
	i := int32(0)
	seen := func() int32 { return atomic.LoadInt32(&i) }
	atomic.AddInt32(&i, 1)
	atomic.AddInt32(&i, 1)
	fmt.Println(i, seen())

	// Plain pointer write-through to a captured sized-numeric var.
	j := int32(5)
	_ = func() int32 { return j }
	p := &j
	*p = 9
	fmt.Println(j)
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("captured_addr.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "2 2\n9\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout: got %q, want %q (stderr: %s)", got, want, stderr.String())
	}
}

// Taking the address of a function parameter must yield a pointer independent
// of the caller's argument variable: a Go parameter is a copy. detachByValueArgs
// only detached struct/array args, so a slice (or other reference-kind) param's
// &param aliased the caller's slot. Reassigning the caller's variable then
// mutated an already-escaped &param -- this corrupted sync.Pool workspaces in
// gonum/mat (putFloat64s(&w) keeps a stale header after Inverse reslices work).
func TestParamAddrDetach(t *testing.T) {
	src := `package main

import "fmt"

var saved *[]float64

func store(w []float64) { saved = &w } // address of the parameter

func main() {
	x := make([]float64, 10, 64)
	store(x)
	before := cap(*saved)
	x = make([]float64, 20, 128) // reassign the caller's variable
	_ = x
	after := cap(*saved)
	fmt.Println(before, after)
}
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "64 64\n"; got != want {
		t.Errorf("&param aliased caller: got %q, want %q", got, want)
	}
}

// A nil reference-kind param that is cell-boxed for &param still needs its own
// addressable cell storage, or a write through the pointer is lost vs a plain
// read (HeapAlloc only re-homed addressable values; a nil header is not
// addressable).
func TestParamAddrDetachNil(t *testing.T) {
	src := `package main

import "fmt"

func f(s []int) {
	p := &s
	*p = []int{1, 2}
	fmt.Println(s)
}

func main() { f(nil) }
`

	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "[1 2]\n"; got != want {
		t.Errorf("write through &param lost on nil arg: got %q, want %q", got, want)
	}
}

// &funcvar used to yield *interface{} (func slots are interface{} boxes),
// breaking reflect.MakeFunc (fn.Type() reported interface) and *p=f through a
// *func. AddrLocal now retypes the slot to its func type.
func TestAddrFuncVar(t *testing.T) {
	src := `package main

import (
	"fmt"
	"reflect"
)

func main() {
	// reflect.MakeFunc round-trip through a *func target.
	swap := func(in []reflect.Value) []reflect.Value {
		return []reflect.Value{in[1], in[0]}
	}
	var intSwap func(int, int) (int, int)
	fn := reflect.ValueOf(&intSwap).Elem()
	fmt.Println(fn.Type().Kind())
	intSwap = reflect.MakeFunc(fn.Type(), swap).Interface().(func(int, int) (int, int))
	a, b := intSwap(1, 2)
	fmt.Println(a, b)

	// &f reports the declared func type, not *interface{}.
	var f func(int) int
	fmt.Printf("%T\n", &f)

	// Assignment through a *func pointer dispatches to the new closure, and a
	// captured closure stays callable after its address is taken.
	base := 10
	g := func(x int) int { return x + base }
	p := &g
	*p = func(x int) int { return x - base }
	fmt.Println(g(1))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("addr_func.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "func\n2 1\n*func(int) int\n-9\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// `*p = ""` where p points to a named string/bool type stores an untyped const
// that kept its base type (string), and DerefSet's reflect.Set rejected it:
// "value of type string is not assignable to type main.ns". numSet now adopts
// the named slot type, as MapSet already does. Minimized from `mvm test
// google.golang.org/protobuf/reflect/protodesc` (TestNewFile -> nameSuffix.Pop:
// `name, *s = protoreflect.Name((*s)), ""`).
func TestDerefAssignNamedConst(t *testing.T) {
	src := `package main

import "fmt"

type ns string

func (s *ns) pop() (head string) {
	head, *s = string(*s), ""
	return head
}

func main() {
	x := ns("hello")
	fmt.Printf("%q %q\n", x.pop(), string(x))
}
`
	var stdout, stderr bytes.Buffer
	i := interp.NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.ImportPackageConsts(stdlib.ConstValues)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("deref_assign_named.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	want := "\"hello\" \"\"\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout:\n got %q\nwant %q", got, want)
	}
}
