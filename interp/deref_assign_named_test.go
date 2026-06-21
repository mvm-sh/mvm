package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

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
	i := NewInterpreter(golang.GoSpec)
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
