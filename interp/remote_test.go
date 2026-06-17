package interp

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mvm-sh/mvm/lang/golang"
	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
	_ "github.com/mvm-sh/mvm/stdlib/all"
)

// remoteModule represents a single module/version served by startFakeProxy.
type remoteModule struct {
	path    string
	version string
	files   map[string]string
}

// startFakeProxy stands up an httptest server speaking enough of the Go
// module proxy protocol to satisfy modfs: @latest -> {Version}, and
// /@v/<ver>.zip -> the module zip. Returns the server URL and a counter
// of handled requests for use in assertions.
func startFakeProxy(t *testing.T, modules ...remoteModule) (string, *int64) {
	t.Helper()
	byPath := map[string]remoteModule{}
	for _, m := range modules {
		byPath[m.path] = m
	}
	var requests int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&requests, 1)
		path := strings.TrimPrefix(r.URL.Path, "/")
		switch {
		case strings.HasSuffix(path, "/@latest"):
			modPath := strings.TrimSuffix(path, "/@latest")
			m, ok := byPath[modPath]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": m.version})
		case strings.HasSuffix(path, ".zip"):
			rest := strings.TrimSuffix(path, ".zip")
			i := strings.LastIndex(rest, "/@v/")
			if i < 0 {
				http.NotFound(w, r)
				return
			}
			modPath, ver := rest[:i], rest[i+len("/@v/"):]
			m, ok := byPath[modPath]
			if !ok || m.version != ver {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(buildZip(t, modPath, ver, m.files))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &requests
}

func TestRemoteImport(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/foo/bar",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod":         "module example.com/foo/bar\n",
			"greet/greet.go": "package greet\nfunc Hello() string { return \"hello from remote\" }\n",
		},
	})

	src := `package main

import "example.com/foo/bar/greet"

func main() {
	println(greet.Hello())
}
`

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "hello from remote\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteTransitiveImport(t *testing.T) {
	url, requests := startFakeProxy(t,
		remoteModule{
			path:    "example.com/lib/a",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/lib/a\n",
				"a.go": `package a

import "example.com/lib/b"

func Greet() string { return "A says: " + b.Name() }
`,
			},
		},
		remoteModule{
			path:    "example.com/lib/b",
			version: "v2.3.4",
			files: map[string]string{
				"go.mod": "module example.com/lib/b\n",
				"b.go":   "package b\nfunc Name() string { return \"B\" }\n",
			},
		},
	)

	src := `package main

import "example.com/lib/a"

func main() {
	println(a.Greet())
}
`

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "A says: B\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}

	// Two modules x (latest + zip) = 4 successful requests, plus probe
	// misses for example.com/lib (shortest-first probing tries the
	// 2-component prefix first). Assert at least 4 to confirm both
	// modules were fetched.
	if got := atomic.LoadInt64(requests); got < 4 {
		t.Errorf("expected >= 4 proxy requests (2 modules x latest+zip), got %d", got)
	}
}

func TestRemoteTypeNameCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod":   "module example.com/x/inner\n",
				"inner.go": "package inner\n\ntype Code uint16\n\nconst English Code = 1\n",
			},
		},
		remoteModule{
			path:    "example.com/x/outer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/outer\n",
				"outer.go": `package outer

import "example.com/x/inner"

type Code struct {
	c inner.Code
}

var Zero Code
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	// Pre-fix this Eval faulted with SIGSEGV inside vm.patchRtype, taking down
	// the test binary; the fix makes it return cleanly.
	if _, err := i.Eval("test", `import "example.com/x/outer"; _ = outer.Zero`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
}

func TestRemoteTypedConstMethod(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/idx",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/idx\n",
			"idx.go": `package idx

type Index string

func (s Index) Elem(i int) byte { return s[i] }

const Tab Index = "abc"

func Get() byte { return Tab.Elem(1) }
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", `import "example.com/x/idx"; println(idx.Get())`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "98\n"; got != want { // 'b'
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteMethodReceiverTypeCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod":   "module example.com/x/inner\n",
				"inner.go": "package inner\n\ntype Tag struct{ N int }\n\nfunc (t Tag) Get() int { return t.N }\n",
			},
		},
		remoteModule{
			path:    "example.com/x/shadow",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod":    "module example.com/x/shadow\n",
				"shadow.go": "package shadow\n\ntype Tag struct{ S string }\n",
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	src := `import "example.com/x/inner"; import "example.com/x/shadow"; var _ = shadow.Tag{}; var x inner.Tag; x.N = 7; println(x.Get())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteVarNameCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner\n",
				"inner.go": `package inner

type rec struct{ n int }

var data = [3]rec{{n: 2}, {n: 3}, {n: 5}}

func Sum() int {
	s := 0
	for i := range data {
		s += data[i].n
	}
	return s
}
`,
			},
		},
		remoteModule{
			path:    "example.com/x/shadow",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/shadow\n",
				"shadow.go": `package shadow

var data = []int{1, 2, 3}

func Len() int { return len(data) }
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/inner"; import "example.com/x/shadow"; println(inner.Sum(), shadow.Len())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "10 3\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}
func TestRemoteTypeNameStructInterfaceCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner\n",
				"inner.go": `package inner

type Foo struct{ v [8]byte }

func Make() Foo {
	var f Foo
	f.v[0] = 'x'
	return f
}

func First(f Foo) byte { return f.v[0] }
`,
			},
		},
		remoteModule{
			path:    "example.com/x/shadow",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/shadow\n",
				"shadow.go": `package shadow

type Foo interface {
	Subtag() string
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	// Importing shadow after inner used to flip inner.Foo from struct to
	// interface; inner.Make's body would then fail "undefined: v".
	src := `import "example.com/x/inner"; import _ "example.com/x/shadow"; println(inner.First(inner.Make()))`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "120\n"; got != want { // 'x'
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteFuncNameCollisionAcrossPkgs(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/lo",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/lo\n",
				"lo.go":  "package lo\n\nfunc Make(s string) int { _ = s; return 1 }\n",
			},
		},
		remoteModule{
			path:    "example.com/x/mid",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/mid\n",
				"mid.go": `package mid

import "example.com/x/lo"

type Tag struct{ field string }

func Make() (tag Tag) {
	_ = lo.Make("hi")
	tag.field = "ok"
	return tag
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/mid"; t := mid.Make(); println(t.field)`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "ok\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteTransitiveImportBareKeyClobber(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod":   "module example.com/x/inner\n",
				"inner.go": "package inner\n\ntype Foo uint16\n",
			},
		},
		remoteModule{
			path:    "example.com/x/outer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/outer\n",
				// Order matters: the method declaration that returns the bare
				// name `Foo` must come BEFORE the `type Foo struct` in source
				// order, so Phase-1 signature parsing captures whatever bare
				// `Foo` is at that moment.
				"a_method.go": `package outer

type Bar struct{}

func (b Bar) Make() (Foo, int) { return Foo{val: 7}, 0 }
`,
				"b_type.go": `package outer

import "example.com/x/inner"

type Foo struct {
	dummy inner.Foo
	val   int
}

func Use() int {
	var b Bar
	f, _ := b.Make()
	return f.val
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("test", `import "example.com/x/outer"; println(outer.Use())`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteTargetBareAliasShadowsImportLocal(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/aliaser",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/aliaser\n",
				// Compiled as the target: aliasTargetTopLevel exposes Shared at the
				// bare key "Shared" (an interface).
				"aliaser.go": "package aliaser\n\ntype Shared interface{ Mark() }\n",
			},
		},
		remoteModule{
			path:    "example.com/victim",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/victim\n",
				// a_wrap.go sorts first, so `type Wrap Shared` is parsed before
				// b_shared.go registers the local Shared -- the window in which the
				// leaked bare alias would otherwise win. len() forces a map, so a
				// resolution to the interface alias fails to compile.
				"a_wrap.go": `package victim

type Wrap Shared

func Use() int { w := Wrap{"a": 1, "b": 2}; return len(w) }
`,
				"b_shared.go": "package victim\n\ntype Shared map[string]int\n",
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	// Compile the target package first so its exports leak to bare keys.
	if _, err := i.Eval("example.com/aliaser", ""); err != nil {
		t.Fatalf("Eval aliaser: %v", err)
	}
	if _, err := i.Eval("test", `import "example.com/victim"; println(victim.Use())`); err != nil {
		t.Fatalf("Eval victim: %v", err)
	}
	if got, want := stdout.String(), "2\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteTargetReimportPublishesExports(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/grpcx/status",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/grpcx/status\n",
				"status.go": `package status

import istatus "example.com/grpcx/internalstatus"

type Status = istatus.Status

func New(msg string) *Status { return istatus.New(msg) }

func Error(msg string) string { return New(msg).Msg }
`,
			},
		},
		remoteModule{
			path:    "example.com/grpcx/internalstatus",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/grpcx/internalstatus\n",
				"status.go": `package status

type Status struct{ Msg string }

func New(msg string) *Status { return &Status{Msg: msg} }

type Error struct{ e string }

func (e *Error) Error() string { return e.e }
`,
			},
		},
		remoteModule{
			path:    "example.com/grpcx/consumer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/grpcx/consumer\n",
				"consumer.go": `package consumer

import "example.com/grpcx/status"

func Run() string { return status.Error("boom") }
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	// Compile the public package as the target first (qualified keys, no Package).
	if _, err := i.Eval("example.com/grpcx/status", ""); err != nil {
		t.Fatalf("Eval target status: %v", err)
	}
	// A separate consumer importing the target drives importSrc(target).
	if _, err := i.Eval("test", `import "example.com/grpcx/consumer"; println(consumer.Run())`); err != nil {
		t.Fatalf("Eval consumer: %v", err)
	}
	if got, want := stdout.String(), "boom\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteGenericMethodNestedTypeInForeignPkg(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/gmap",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/gmap\n",
				"gmap.go": `package gmap

type Inner struct{ X int }

type entry[T any] struct {
	k Inner
	v T
}

type Box[T any] struct {
	m map[string]entry[T]
}

func New[T any]() *Box[T] { return &Box[T]{m: map[string]entry[T]{}} }

func (b *Box[T]) Set(key string, val T) {
	b.m[key] = entry[T]{k: Inner{X: 1}, v: val}
}

func (b *Box[T]) Len() int { return len(b.m) }
`,
			},
		},
		remoteModule{
			path:    "example.com/gconsumer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/gconsumer\n",
				"consumer.go": `package gconsumer

import "example.com/gmap"

type Thing struct{ N int }

type Holder struct {
	b *gmap.Box[Thing]
}

func Run() int {
	h := Holder{b: gmap.New[Thing]()}
	h.b.Set("a", Thing{N: 7})
	h.b.Set("b", Thing{N: 8})
	return h.b.Len()
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("test", `import "example.com/gconsumer"; println(gconsumer.Run())`); err != nil {
		t.Fatalf("Eval consumer: %v", err)
	}
	if got, want := stdout.String(), "2\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemotePkgAliasCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/lang", // outer "lang" pkg.
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/lang\n",
				"lang.go": `package lang

import "example.com/x/inner/lang"

// X here is the OUTER lang.X (a wrapper).
type X struct {
	inner lang.X
}

// Use the inner lang's X composite literal inside a method body so the
// resolution happens in Phase 2 (deferred body), after main has finished
// running its own imports and overwritten the bare ` + "`lang`" + ` key.
func Wrap(v int) X { return X{inner: lang.X{V: v}} }

func (x X) Get() int { return x.inner.V }
`,
			},
		},
		remoteModule{
			path:    "example.com/x/inner/lang", // inner "lang" pkg (same short name as outer).
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner/lang\n",
				"lang.go": `package lang

type X struct{ V int }
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	// Main imports both outer and inner. Both have short name `lang`;
	// the LAST SymSet wins for the bare key.
	src := `import inner "example.com/x/inner/lang"; import "example.com/x/lang"; println(lang.Wrap(42).Get(), inner.X{V: 3}.V)`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "42 3\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemotePkgQualifiedConstUnsetAddr(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner\n",
				"inner.go": `package inner

type AliasType int8

const (
	Deprecated AliasType = iota
	Macro
	Legacy
)
`,
			},
		},
		remoteModule{
			path:    "example.com/x/outer",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/outer\n",
				"outer.go": `package outer

import "example.com/x/inner"

func Classify(t inner.AliasType) string {
	switch t {
	case inner.Legacy:
		return "legacy"
	case inner.Macro:
		return "macro"
	}
	return "other"
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	src := `import (
		"example.com/x/inner"
		"example.com/x/outer"
	)
	outer.Classify(inner.Legacy)`
	v, err := i.Eval("test", src)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got := v.Interface(); got != "legacy" {
		t.Fatalf("Classify(inner.Legacy) = %v, want %q", got, "legacy")
	}
}

func TestRemoteTestTargetImportingPkg(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/target",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/target\n",
			"target.go": `package target

type hidden struct {
	X int
}

type Outer struct {
	hidden
}

func (o *Outer) Get() int {
	return o.X
}

func Make() *Outer { return &Outer{hidden: hidden{X: 42}} }
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	// Direct-target load.
	i.SetIncludeTests(true)
	if _, err := i.Eval("example.com/x/target", ""); err != nil {
		t.Fatalf("loading target: %v", err)
	}
}

func TestRemoteTestTargetForwardVarType(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/fwdvar",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/fwdvar\n",
			// "z_user.go" (z) is parsed after "fwdvar.go" but UUID's definition is
			// in "fwdvar.go" -- ordering matches the uuid pkg's hash.go/uuid.go.
			"fwdvar.go": `package fwdvar

type UUID [16]byte
`,
			"z_user.go": `package fwdvar

var (
	NameSpaceX = UUID{0xff}
	Nil        UUID
)

type Holder struct {
	UUID  UUID
	Valid bool
}

func (h *Holder) Reset() {
	h.UUID, h.Valid = Nil, false
}
`,
			"z_user_test.go": `package fwdvar

import "testing"

func TestReset(t *testing.T) {
	var h Holder
	h.Valid = true
	h.Reset()
	if h.Valid {
		t.Fatal("Reset should clear Valid")
	}
}
`,
		},
	})

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	i.SetIncludeTests(true)
	if _, err := i.Eval("example.com/x/fwdvar", ""); err != nil {
		t.Fatalf("loading fwdvar: %v\nstderr: %s", err, stderr.String())
	}
	// Also exercise the test-driver alias path: FuncNames must find TestReset.
	names := i.FuncNames("Test")
	if len(names) == 0 {
		t.Fatalf("FuncNames found no Test* funcs; expected at least TestReset")
	}
}

func TestRemoteIfaceDispatchSignatureCollision(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/ifacecollide",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/ifacecollide\n",
			"ifacecollide.go": `package ifacecollide

import "reflect"

// Index is a string-typed defined type with its own Elem method whose
// signature (func(int) string) differs from reflect.Type.Elem (func() reflect.Type).
type Index string

func (i Index) Elem(_ int) string { return string(i) }

type S struct {
	X *int
}

// Probe chains reflect.Type.Elem().Kind().
func Probe() reflect.Kind {
	t := reflect.TypeOf(S{})
	f := t.Field(0)
	return f.Type.Elem().Kind()
}

var _ = Index("seed")
`,
		},
	})

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("example.com/x/ifacecollide", ""); err != nil {
		t.Fatalf("loading ifacecollide: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRemotePromotedFieldAndMethodThroughAnon(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/cldrlike",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/cldrlike\n",
			"cldrlike.go": `package cldrlike

type rule struct {
	Value  string
	Before string
}

func (r *rule) Read() string { return r.Before }

type wrap struct {
	Name string
	rule
}

type Holder struct {
	Rules struct {
		Items []*wrap
	}
}

func (h Holder) Process() string {
	var out string
	for _, r := range h.Rules.Items {
		out += r.Before + ":" + r.Read() + ";"
	}
	return out
}

func New() Holder {
	return Holder{Rules: struct{ Items []*wrap }{Items: []*wrap{{Name: "x", rule: rule{Before: "p"}}}}}
}
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("example.com/x/cldrlike", ""); err != nil {
		t.Fatalf("loading cldrlike: %v", err)
	}
}

func TestRemoteReflectTypeForCrossPkg(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/typefortarget",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/typefortarget\n",
			"types.go": `package typefortarget

import "reflect"

type MyKind struct {
	A uint64
	B uint16
}

func Size() uintptr {
	return reflect.TypeFor[MyKind]().Size()
}
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/typefortarget"; println(typefortarget.Size())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	// MyKind layout: uint64 (8) + uint16 (2) + 6-byte tail padding = 16.
	if got, want := stdout.String(), "16\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemotePlaceholderRtypeGrow(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/lang",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/lang\n",
			"lang.go": `package lang

type Tag struct {
	A uint16
	B uint16
	C uint16
	D byte
	E uint16
	S string
}

func (t Tag) String() string { return t.S }

var Und Tag
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/lang"
println(lang.Und.A, lang.Und.B, lang.Und.C, lang.Und.D, lang.Und.E, len(lang.Und.S))`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "0 0 0 0 0 0\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteZeroInitLocalShortNameCollision(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/x/dual",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/x/dual\n",
			"inner/inner.go": `package inner

type Tag struct {
	A uint16
	B uint16
	C uint16
	D byte
	E uint16
	S string
}
`,
			"outer.go": `package dual

import "example.com/x/dual/inner"

type Tag struct {
	X uint64
	Y uint64
}

func F() inner.Tag {
	var v inner.Tag
	v.A = 7
	return v
}
`,
		},
	})

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/dual"
println(dual.F().A)`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteMethodScopedLocalCollidesAcrossPkgs(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/outer/pkg",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/outer/pkg\n",
				"pkg.go": `package pkg

import inner "example.com/inner/pkg"

type Tag struct {
	A int
	B int
}

// Mirrors language.Tag.UnmarshalText.
func (t *Tag) Do(text []byte) error {
	var x inner.Tag
	err := x.Do(text)
	*t = Tag{A: 1, B: 2}
	_ = x
	return err
}
`,
			},
		},
		remoteModule{
			path:    "example.com/inner/pkg",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/inner/pkg\n",
				"pkg.go": `package pkg

type Tag struct {
	A int
	B int
}

func (t *Tag) Do(text []byte) error {
	var a int
	err := set(t, 134)
	a = a + 1
	_ = a
	return err
}

func set(t *Tag, v int) error {
	t.A = v
	t.B = v
	return nil
}
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/outer/pkg"
var tag pkg.Tag
err := tag.Do([]byte("en"))
println(err == nil)`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "true\n"; got != want {
		t.Errorf("stdout: got %q want %q", got, want)
	}
}

func TestRemoteCrossPkgSameFuncLocalsCollision(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/x/inner/diff",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/inner/diff\n",
				"lang.go": `package diff

type Language uint16

func ParseBase(s string) (l Language, err error) {
	if len(s) == 2 && s[0] == 'e' && s[1] == 'n' {
		return Language(313), nil
	}
	return 0, nil
}
`,
			},
		},
		remoteModule{
			path:    "example.com/x/lang",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/x/lang\n",
				"lang.go": `package lang

import "example.com/x/inner/diff"

type Base struct {
	langID diff.Language
}

func ParseBase(s string) (Base, error) {
	l, err := diff.ParseBase(s)
	return Base{l}, err
}

func (b Base) ID() uint16 { return uint16(b.langID) }
`,
			},
		},
	)

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	src := `import "example.com/x/lang"
b, _ := lang.ParseBase("en")
println(b.ID())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "313\n"; got != want {
		t.Errorf("stdout: got %q want %q", got, want)
	}
}

func TestRemoteXTextLanguageImport(t *testing.T) {
	if testing.Short() {
		t.Skip("requires network access to the Go module proxy")
	}
	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{}))
	if _, err := i.Eval("test", `import "golang.org/x/text/language"; println(language.English.String())`); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "en\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func buildZip(t *testing.T, mod, ver string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	prefix := mod + "@" + ver + "/"
	for name, body := range files {
		w, err := zw.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRemoteImportedInitBeforeMainVarInit(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/dep",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/dep\n",
				"dep.go": `package dep

var Ready string

func init() { Ready = "INITIALIZED" }
`,
			},
		},
	)

	src := `package main

import "example.com/dep"

var got = dep.Ready

var local = "set"
var localSeenByInit string

func init() { localSeenByInit = local }

func main() {
	println(got)
	println(localSeenByInit)
}
`

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "INITIALIZED\nset\n"; got != want {
		t.Errorf("stdout: got %q, want %q (imported init must precede main var init; main init after main var init)", got, want)
	}
}

// Go init order across imported packages: reader's var init reads a var that
// setter's init() sets, and reader imports setter, so setter.init() must run
// first. mvm flattened to [all var inits][all init()s], so reader read the unset
// (nil) var and the assertion panicked. Mirrors grpc internal/transport's
// metadataFromOutgoingContextRaw <- metadata.init(); needs per-package
// interleaving (comp.VarDeferral), not just the target-package deferral.
func TestRemoteImportedInitBeforeImportedVarInit(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/dep",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/dep\n",
				"dep.go": `package dep

var Sink any // set by setter.init, read by reader's var init
`,
			},
		},
		remoteModule{
			path:    "example.com/setter",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/setter\n",
				"setter.go": `package setter

import "example.com/dep"

func init() { dep.Sink = func() string { return "OK" } }
`,
			},
		},
		remoteModule{
			path:    "example.com/reader",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/reader\n",
				"reader.go": `package reader

import (
	"example.com/dep"
	_ "example.com/setter"
)

var fn = dep.Sink.(func() string)

func Get() string { return fn() }
`,
			},
		},
	)

	src := `package main

import "example.com/reader"

func main() { println(reader.Get()) }
`
	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "OK\n"; got != want {
		t.Errorf("stdout: got %q, want %q (imported setter.init must run before imported reader var init)", got, want)
	}
}

func TestRemoteGenericIfaceConstraintForeignArg(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/gen",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/gen\n",
				"gen.go": `package gen

type Msg interface { Tag() int }

func Clone[M Msg](m M) M { return m }
`,
			},
		},
		remoteModule{
			path:    "example.com/msg",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/msg\n",
				"msg.go": `package msg

type T struct{ N int }

func (t *T) Tag() int { return t.N }

func New() *T { return &T{N: 7} }
`,
			},
		},
	)

	src := `package main

import (
	"example.com/gen"
	"example.com/msg"
)

func main() {
	got := gen.Clone(msg.New())
	println(got.Tag())
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
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

func TestRemoteImportedSliceCompositeNilFnew(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/wire",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/wire\n",
			"wire.go": `package wire

type token interface{ isToken() }

type (
	Token        token
	Message      []Token
	Tag          struct{ N, T int }
	Bool         bool
	Varint       int64
	Svarint      int64
	Uvarint      uint64
	Int32        int32
	Uint32       uint32
	Int64        int64
	Uint64       uint64
	Str          string
	Bytes        []byte
	LengthPrefix Message
	Raw          []byte
)

func (Message) isToken()      {}
func (Tag) isToken()          {}
func (Bool) isToken()         {}
func (Varint) isToken()       {}
func (Svarint) isToken()      {}
func (Uvarint) isToken()      {}
func (Int32) isToken()        {}
func (Uint32) isToken()       {}
func (Int64) isToken()        {}
func (Uint64) isToken()       {}
func (Str) isToken()          {}
func (Bytes) isToken()        {}
func (LengthPrefix) isToken() {}
func (Raw) isToken()          {}

func (m Message) Size() int {
	n := 0
	for _, v := range m {
		switch v := v.(type) {
		case Tag:
			n += 2
		case LengthPrefix:
			n += Message(v).Size()
		case Bytes:
			n += len(v)
		default:
			n++
		}
	}
	return n
}

func (m Message) Marshal() []int {
	var out []int
	for _, v := range m {
		switch v := v.(type) {
		case Tag:
			out = append(out, v.N, v.T)
		case Bool:
			out = append(out, 1)
		case Varint:
			out = append(out, int(v))
		case Svarint:
			out = append(out, int(v))
		case Uvarint:
			out = append(out, int(v))
		case Int32:
			out = append(out, int(v))
		case Uint32:
			out = append(out, int(v))
		case Int64:
			out = append(out, int(v))
		case Uint64:
			out = append(out, int(v))
		case Str:
			out = append(out, len(v))
		case Bytes:
			out = append(out, len(v))
		case LengthPrefix:
			out = append(out, Message(v).Marshal()...)
		case Raw:
			out = append(out, v...)
		}
	}
	return out
}

var (
	Seed  = LengthPrefix(Message{Varint(1)})
	Seed2 = Message{LengthPrefix{Varint(1)}, Bytes{1}, Raw{2}}
)
`,
		},
	})

	var b strings.Builder
	b.WriteString("import \"example.com/wire\"\n\nvar msgs = [][]int{\n")
	// Nine flat composites accumulate code/Fnews ahead of the nested one.
	for i := 0; i < 9; i++ {
		b.WriteString("\twire.Message{wire.Tag{1, 0}, wire.Varint(1), wire.Tag{2, 0}, wire.Varint(2)}.Marshal(),\n")
	}
	// One composite nesting fourteen LengthPrefix literals (the trigger).
	b.WriteString("\twire.Message{\n")
	for i := 1; i <= 14; i++ {
		fmt.Fprintf(&b, "\t\twire.Tag{%d, 2}, wire.LengthPrefix{wire.Varint(%d), wire.Varint(%d)},\n", i, i*10, i*10+1)
	}
	b.WriteString("\t}.Marshal(),\n}\n\n")
	b.WriteString("func main() {\n\tn := 0\n\tfor _, w := range msgs {\n\t\tn += len(w)\n\t}\n\tprintln(n)\n}\n")

	var stdout, stderr bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, &stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{Proxy: url}))

	if _, err := i.Eval("test", b.String()); err != nil {
		t.Fatalf("Eval: %v\nstderr: %s", err, stderr.String())
	}
	// 9 flat msgs: 6 ints each = 54. Nested: 14 tags*2 + 14*2 varints = 56.
	if got, want := stdout.String(), "110\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteUnexportedMethodInterfaceSatisfaction is the grpc registration
// pattern: a server embeds a foreign Unimplemented base to satisfy an interface
// with an unexported method. Native reflect.Implements matches that method by
// name and declaring package, so its promoted synth name must carry "svc".
func TestRemoteUnexportedMethodInterfaceSatisfaction(t *testing.T) {
	url, _ := startFakeProxy(t, remoteModule{
		path:    "example.com/svc",
		version: "v1.0.0",
		files: map[string]string{
			"go.mod": "module example.com/svc\n",
			"svc.go": `package svc

type Svc interface {
	Name() string
	mustEmbedUnimplementedSvc()
}

type UnimplementedSvc struct{}

func (UnimplementedSvc) Name() string                { return "base" }
func (UnimplementedSvc) mustEmbedUnimplementedSvc() {}
`,
		},
	})

	src := `package main

import (
	"fmt"
	"reflect"

	"example.com/svc"
)

type server struct {
	svc.UnimplementedSvc
}

func (server) Name() string { return "server" }

func main() {
	st := reflect.TypeOf(server{})
	it := reflect.TypeOf((*svc.Svc)(nil)).Elem()
	fmt.Println(st.Implements(it))
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
	if got, want := stdout.String(), "true\n"; got != want {
		t.Errorf("Implements: got %q, want %q", got, want)
	}
}

// TestRemoteUnexportedMethodMultiLevelPromotion is the two-level variant: the
// unexported method is declared in "inner" and reaches main through "mid". Its
// synth name must carry "inner" (the deepest embed), not "mid" (the first hop),
// so the pkgPath walk must follow the full method.Path.
func TestRemoteUnexportedMethodMultiLevelPromotion(t *testing.T) {
	url, _ := startFakeProxy(t,
		remoteModule{
			path:    "example.com/inner",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/inner\n",
				"inner.go": `package inner

type Svc interface {
	Name() string
	mustEmbedInner()
}

type Base struct{}

func (Base) Name() string   { return "base" }
func (Base) mustEmbedInner() {}
`,
			},
		},
		remoteModule{
			path:    "example.com/mid",
			version: "v1.0.0",
			files: map[string]string{
				"go.mod": "module example.com/mid\n",
				"mid.go": `package mid

import "example.com/inner"

type Mid struct{ inner.Base }
`,
			},
		},
	)

	src := `package main

import (
	"fmt"
	"reflect"

	"example.com/inner"
	"example.com/mid"
)

type server struct {
	mid.Mid
}

func (server) Name() string { return "server" }

func main() {
	st := reflect.TypeOf(server{})
	it := reflect.TypeOf((*inner.Svc)(nil)).Elem()
	fmt.Println(st.Implements(it))
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
	if got, want := stdout.String(), "true\n"; got != want {
		t.Errorf("Implements: got %q, want %q", got, want)
	}
}
