package interp

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Minimal repro for the pflag IPNet dispatch bug: a value-receiver method
// defined in a sub-package on a named type whose underlying is a
// stdlib-bridged struct, called via interface dispatch on *T.
//
// Before the fix in vm.go IfaceCall, the method body received `ipnet`
// as `*net.IPNet` (the stdlib type, with extra leading pointer) instead
// of `ipNetValue` (the value receiver expects). `net.IPNet(ipnet)` then
// panicked with "value of type *net.IPNet cannot be converted to type
// net.IPNet". The fix derefs ifc.Val when ResolveMethodType walked from
// *T to T and the method has a value receiver (PtrRecv=false).
func TestRemoteIPNetIfaceDirect(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/pflag",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/pflag\n",
			"pflag.go": `package pflag

import "net"

type ipNetValue net.IPNet

func (ipnet ipNetValue) String() string {
	n := net.IPNet(ipnet)
	return n.String()
}

type Stringer interface { String() string }

func Use(s Stringer) string { return s.String() }

func Make(v net.IPNet) Stringer {
	p := new(net.IPNet)
	*p = v
	return (*ipNetValue)(p)
}
`,
		},
	})

	src := `package main

import (
	"net"
	"example.com/x/pflag"
)

func main() {
	s := pflag.Make(net.IPNet{})
	println("got:", pflag.Use(s))
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "panic") {
		t.Errorf("got panic: %s", stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "<nil>") {
		t.Errorf("stdout: got %q", got)
	}
}

// Same fix, exercised through a pflag-shaped call chain (multi-method
// interface, several layers of method calls before the iface dispatch
// reaches the value-receiver method body).
func TestRemoteIPNetIfaceDispatch(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/pflag",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/pflag\n",
			"pflag.go": `package pflag

import "net"

type ipNetValue net.IPNet

func (ipnet ipNetValue) String() string {
	n := net.IPNet(ipnet)
	return n.String()
}

func (ipnet *ipNetValue) Set(value string) error { return nil }
func (*ipNetValue) Type() string                 { return "ipNet" }

func NewIPNetValue(val net.IPNet, p *net.IPNet) *ipNetValue {
	*p = val
	return (*ipNetValue)(p)
}

type Value interface {
	String() string
	Set(string) error
	Type() string
}

type FlagSet struct{}

func (f *FlagSet) VarPF(value Value, name string) string {
	return value.String()
}

func (f *FlagSet) VarP(value Value, name string) string { return f.VarPF(value, name) }

func (f *FlagSet) IPNetVarP(p *net.IPNet, name string, value net.IPNet) string {
	return f.VarP(NewIPNetValue(value, p), name)
}

func (f *FlagSet) IPNet(name string, value net.IPNet) string {
	p := new(net.IPNet)
	return f.IPNetVarP(p, name, value)
}
`,
		},
	})

	src := `package main

import (
	"net"
	"example.com/x/pflag"
)

func main() {
	fs := &pflag.FlagSet{}
	s := fs.IPNet("IPNet", net.IPNet{})
	println("got:", s)
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	got := stdout.String()
	if strings.Contains(stderr.String(), "panic") {
		t.Errorf("got panic: %s", stderr.String())
	}
	if !strings.Contains(got, "<nil>") {
		t.Errorf("stdout: got %q", got)
	}
}

// mvm registers pointer-receiver methods on the value type T with
// PtrRecv=true. The native-bridge layer (vm.wrapIface / wrapIfaceMulti)
// must NOT expose those methods on T's method set: in Go semantics they
// only belong to *T. Otherwise, passing a T value to native fmt would
// build a Stringer bridge with the int Value as receiver, and the
// pointer-receiver body would panic at the first `*recv` deref with
// "reflect: call of reflect.Value.Elem on int Value". Reproducer:
// pflag's TestPrintDefaults via `type customValue int` with a
// pointer-receiver String() that calls fmt.Sprintf("%v", *cv).
func TestNativeBridgeSkipsPtrRecvOnValue(t *testing.T) {
	src := `package main

import "fmt"

type customValue int

func (cv *customValue) String() string { return fmt.Sprintf("%v", *cv) }

func main() {
	cv2 := customValue(10)
	fmt.Println(&cv2)
	fmt.Println(cv2)
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "PANIC=") {
		t.Errorf("got PANIC marker in output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if got, want := stdout.String(), "10\n10\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
