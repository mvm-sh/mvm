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
	i := NewInterpreter(golang.GoSpec)
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
