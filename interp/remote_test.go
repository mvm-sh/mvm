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

// TestRemoteXTextCrash is a skipped repro: importing golang.org/x/text via the
// live proxy faults inside vm.patchRtype on the go1.26 toolchain. See the
// memory note "patchRtype faults on go1.26". Un-skip only on a build where
// the fix is being verified -- the fault is a fatal SIGSEGV that takes down
// the whole test binary, and the test needs network access.
func TestRemoteXTextCrash(t *testing.T) {
	t.Skip("known crash: vm.patchRtype faults on go1.26 when importing golang.org/x/text/...; needs network")

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
