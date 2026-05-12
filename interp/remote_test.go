package interp

import (
	"archive/zip"
	"bytes"
	"encoding/json"
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

// TestRemoteImport runs interpreted code that imports a package fetched
// dynamically over HTTP via modfs. No filesystem source for the package
// exists; the only path is through the network-backed FS.
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

// TestRemoteTransitiveImport verifies that a remote module's own imports
// of another remote module also resolve through modfs. Module A imports
// module B; the interpreted main only mentions A.
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

// TestRemoteTypeNameCollision exercises the bare-name type-collision path that
// used to SIGSEGV inside vm.patchRtype: package "outer" declares `type Code
// struct{...}` (pre-registered as a fresh struct placeholder) and imports
// package "inner" which declares `type Code uint16` (whose Rtype is the shared,
// read-only reflect.TypeOf(uint16(0))). Parsing inner overwrites the bare
// "Code" symbol with the static rtype; finalizing outer's struct then memcpy'd
// onto read-only memory. With the fix, outer's struct gets a fresh placeholder.
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

// TestRemoteTypedConstMethod checks that a typed string/basic constant keeps
// its named type's method set across a package boundary (x/text's
// internal/language does `const lang tag.Index = "..."; lang.Index(key)` where
// tag.Index is `type Index string`). mvm previously reported "undefined: Index"
// because symbol.MethodByName had no case for symbol.Const.
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

// TestRemoteMethodReceiverTypeCollision checks that a method body binds its
// receiver to the type it was declared against, even when a sibling import
// later shadows the bare type name. Here "inner" declares `type Tag struct{ N
// int }` with method Get; "shadow" also declares `type Tag struct{ S string }`.
// main imports inner then shadow, so by the time inner.Get's deferred body is
// compiled the bare name "Tag" points at shadow's struct. mvm previously bound
// inner.Get's receiver to that wrong Tag and failed with "undefined: N".
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

	// Importing shadow shadows the bare "Tag"; without the Phase-1 receiver-type
	// cache, inner.Get's body would bind t to shadow.Tag and fail "undefined: N".
	// (Note: `inner.Tag{N: 7}.Get()` would also work here for the receiver-type
	// fix but trips a separate composite-literal/shadowing bug, so use a var.)
	src := `import "example.com/x/inner"; import "example.com/x/shadow"; var _ = shadow.Tag{}; var x inner.Tag; x.N = 7; println(x.Get())`
	if _, err := i.Eval("test", src); err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got, want := stdout.String(), "7\n"; got != want {
		t.Errorf("stdout: got %q, want %q", got, want)
	}
}

// TestRemoteVarNameCollision: two imported packages each declare a top-level
// `var data` of a different type. Phase 2 compiles each package's var init and
// then a function body that reads its own `data`. Before package-qualified
// resolution of deferred declarations, the bare key "data" in the symbol table
// was shared and its symbol mutated in place by whichever var init compiled
// last, so inner.Sum's body would see shadow's `data` ([]int) and fail
// "undefined: n" on `data[i].n`.
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

// TestRemoteXTextCrash imports golang.org/x/text/language over the live proxy.
// It used to fault inside vm.patchRtype (a read-only static rtype was patched
// when the bare type names "Script"/"Region" collided between
// internal/language's `type Script uint16` and language's `type Script
// struct`); that crash is fixed -- see TestRemoteTypeNameCollision for a
// hermetic regression test. This case still fails on other unimplemented
// parser features in x/text (currently "undefined: LangID"), so it stays
// skipped; it also needs network access. Un-skip if/when x/text parses cleanly.
func TestRemoteXTextCrash(t *testing.T) {
	t.Skip("vm.patchRtype crash fixed; x/text still hits other parser limits and the test needs network")

	var stdout bytes.Buffer
	i := NewInterpreter(golang.GoSpec)
	i.ImportPackageValues(stdlib.Values)
	i.SetIO(os.Stdin, &stdout, os.Stderr)
	i.SetRemoteFS(modfs.New(modfs.Options{}))
	if _, err := i.Eval("test", `import "golang.org/x/text/language"; _ = language.English`); err != nil {
		t.Fatalf("Eval: %v", err)
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
