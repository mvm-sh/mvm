package interp

import (
	"bytes"
	"os"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
	"github.com/mvm-sh/mvm/stdlib/stdmod"
)

// Regression for grpc/internal/transport: a func-local variable's scoped symbol
// key ("New/if0/r") carries no package qualifier, so it leaked across compile
// units.
func TestExtUnitScopedLocalNoCrossUnitLeak(t *testing.T) {
	files := map[string]string{
		"go.mod": "module example.com/x/lvl\n",
		"lvl.go": `package lvl

type Target struct{ URL string }

type Resolver interface {
	Close()
}

func mk() Resolver { return nil }

// New declares an interface-typed local r in its first if-block, registering
// the scoped key "New/if0/r" during unit 1.
func New() Resolver {
	if r := mk(); r != nil {
		return r
	}
	return nil
}

func F() int { return 1 }
`,
		"lvl_internal_test.go": `package lvl

import "testing"

func TestInternal(t *testing.T) {}
`,
		"dep/dep.go": `package dep

import (
	"net/url"
	"sync"

	"example.com/x/lvl"
)

var HookFromEnv = defaultHook

func defaultHook() (*url.URL, error) { return nil, nil }

type nopResolver struct{}

func (nopResolver) Close() {}

type delegatingResolver struct {
	target         lvl.Target
	proxyURL       *url.URL
	mu             sync.Mutex
	targetResolver lvl.Resolver
	proxyResolver  lvl.Resolver
}

// New reads r.proxyURL in its first if-block (scope "New/if0"); r is the
// func-level var, so resolution of r there must not fall through to a stale
// "New/if0/r" left by lvl.New in unit 1.
func New(t lvl.Target) bool {
	r := &delegatingResolver{
		target:         t,
		proxyResolver:  nopResolver{},
		targetResolver: nopResolver{},
	}
	if r.proxyURL == nil {
		return true
	}
	return false
}
`,
		"lvl_test.go": `package lvl_test

import (
	"net/url"
	"testing"

	"example.com/x/lvl"
	"example.com/x/lvl/dep"
)

func TestExternal(t *testing.T) {
	dep.HookFromEnv = func() (*url.URL, error) { return nil, nil }
	if ok := dep.New(lvl.Target{URL: "x"}); !ok {
		t.Fatalf("New: ok=%v", ok)
	}
}
`,
	}
	url, _ := startFakeProxy(t, remoteModule{path: "example.com/x/lvl", version: "v1.0.0", files: files})
	var stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &bytes.Buffer{}, &stderr)
	mfs := modfs.New(modfs.Options{Proxy: url})
	if err := mfs.Inject(stdmod.ModulePath, stdmod.Version, stdlib.EmbeddedStd()); err != nil {
		t.Fatalf("inject std: %v", err)
	}
	i.SetStdlibFS(stdmod.FS(mfs))
	i.SetRemoteFS(mfs)
	i.SetIncludeTests(true)
	if _, err := i.Eval("example.com/x/lvl", ""); err != nil {
		t.Fatalf("load target: %v\nstderr: %s", err, stderr.String())
	}
	i.PublishCompiledPackage("example.com/x/lvl")
	if _, err := i.EvalFiles(i.ExternalTestSources()); err != nil {
		t.Fatalf("external unit: %v\nstderr: %s", err, stderr.String())
	}
}
