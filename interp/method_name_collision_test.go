package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// Regression for go-cmp TestDiff/Project2: a promoted method resolved through
// an embedded field probed the BARE "<recvName>.<method>" symbol key, so a
// same-named unit-local type hijacked an imported receiver (cmp_test.Stringer's
// String, body `string(s)`, ran with a testprotos.Stringer struct receiver ->
// reflect.Value.Convert panic). promotedMethod/MethodByName now verify the bare
// key's Type symbol is the receiver's own type before trusting the bare probe.
func TestPromotedMethodSameNameOtherPkg(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/protos",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/protos\n",
			"protos.go": `package protos

type Stringer struct{ X string }

func (s *Stringer) String() string { return s.X }

type Germ struct {
	Stringer
}
`,
		},
	})
	src := `package main

import (
	"fmt"

	"example.com/x/protos"
)

type Stringer string

func (s Stringer) String() string { return string(s) }

func main() {
	g := &protos.Germ{Stringer: protos.Stringer{X: "germ1"}}
	fmt.Println(g.String(), Stringer("ok").String())
}
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("main.go", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "germ1 ok\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
