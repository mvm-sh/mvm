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

// Two packages each declare a same-named func containing a closure: the
// closures share the anon name "#panics.func1", and a bare symbol-table key
// made the second parse reuse the first package's closure Symbol, leaking its
// FreeVars (gonum/mat's panics vs blas/testblas's panics: "undefined:
// panics/b"). Closure symbols are now keyed per-package (anonFuncKey).
func TestClosurePkgKeyCollision(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/check",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/check\n",
			"check.go": `package check

func panics(f func()) (b bool) {
	defer func() {
		b = recover() != nil
	}()
	f()
	return
}

func Probe() bool { return panics(func() { panic("x") }) }
`,
		},
	})

	src := `package main

import (
	"fmt"
	"example.com/x/check"
)

func panics(fn func()) (panicked bool, message string) {
	defer func() {
		r := recover()
		panicked = r != nil
		message = fmt.Sprint(r)
	}()
	fn()
	return
}

func main() {
	p, m := panics(func() { panic("boom") })
	fmt.Println(check.Probe(), p, m)
}
`

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "true true boom\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
	if s := stderr.String(); strings.Contains(s, "panic") {
		t.Errorf("stderr: %s", s)
	}
}
