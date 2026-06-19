package modfs

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeProxy serves Go module proxy responses backed by an in-memory map.
// modules maps "<modPath>@<version>" -> file -> contents.
type fakeProxy struct {
	t        *testing.T
	modules  map[string]map[string]string // key: modPath@version
	latest   map[string]string            // modPath -> version
	requests int64                        // counts handled requests for assertions
}

func (p *fakeProxy) handler(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&p.requests, 1)
	// URL: /<escaped-mod-path>/@latest or /<escaped-mod-path>/@v/<ver>.zip
	path := strings.TrimPrefix(r.URL.Path, "/")
	if mod, _, ok := splitAtSegment(path, "@latest"); ok {
		mod = unescapePath(mod)
		ver, has := p.latest[mod]
		if !has {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"Version": ver})
		return
	}
	mod, rest, ok := splitAtSegment(path, "@v")
	if !ok {
		http.NotFound(w, r)
		return
	}
	mod = unescapePath(mod)
	if !strings.HasSuffix(rest, ".zip") {
		http.NotFound(w, r)
		return
	}
	ver := strings.TrimSuffix(strings.TrimPrefix(rest, "/"), ".zip")
	files, has := p.modules[mod+"@"+ver]
	if !has {
		http.NotFound(w, r)
		return
	}
	zipBytes, err := buildZip(mod, ver, files)
	if err != nil {
		p.t.Fatalf("buildZip: %v", err)
	}
	w.Header().Set("Content-Type", "application/zip")
	_, _ = w.Write(zipBytes)
}

func splitAtSegment(path, seg string) (before, after string, ok bool) {
	idx := strings.Index(path, "/"+seg)
	if idx < 0 {
		return "", "", false
	}
	return path[:idx], path[idx+1+len(seg):], true
}

// unescapePath reverses the case-encoding (!a -> A) used by the proxy
// protocol. Test-only helper.
func unescapePath(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '!' && i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z' {
			b.WriteByte(s[i+1] - ('a' - 'A'))
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func buildZip(mod, ver string, files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	prefix := mod + "@" + ver + "/"
	for name, body := range files {
		w, err := zw.Create(prefix + name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func newTestFS(t *testing.T, p *fakeProxy) *FS {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	t.Cleanup(srv.Close)
	return New(Options{Proxy: srv.URL})
}

func TestOpenFile(t *testing.T) {
	p := &fakeProxy{
		t:      t,
		latest: map[string]string{"github.com/foo/bar": "v1.2.3"},
		modules: map[string]map[string]string{
			"github.com/foo/bar@v1.2.3": {
				"go.mod":     "module github.com/foo/bar\n",
				"hello.go":   "package bar\nfunc Hello() string { return \"hi\" }\n",
				"sub/sub.go": "package sub\nvar X = 1\n",
			},
		},
	}
	f := newTestFS(t, p)

	data, err := fs.ReadFile(f, "github.com/foo/bar/hello.go")
	if err != nil {
		t.Fatalf("read hello.go: %v", err)
	}
	if !strings.Contains(string(data), "func Hello") {
		t.Errorf("unexpected hello.go: %q", data)
	}

	// Subdirectory inside the same module should be served by the cached
	// module download (no extra @latest fetch).
	subData, err := fs.ReadFile(f, "github.com/foo/bar/sub/sub.go")
	if err != nil {
		t.Fatalf("read sub/sub.go: %v", err)
	}
	if !strings.Contains(string(subData), "var X = 1") {
		t.Errorf("unexpected sub.go: %q", subData)
	}
}

func TestReadDir(t *testing.T) {
	p := &fakeProxy{
		t:      t,
		latest: map[string]string{"github.com/foo/bar": "v0.0.1"},
		modules: map[string]map[string]string{
			"github.com/foo/bar@v0.0.1": {
				"go.mod":   "module github.com/foo/bar\n",
				"a.go":     "package bar\n",
				"b.go":     "package bar\n",
				"sub/c.go": "package sub\n",
			},
		},
	}
	f := newTestFS(t, p)

	entries, err := fs.ReadDir(f, "github.com/foo/bar")
	if err != nil {
		t.Fatalf("ReadDir root: %v", err)
	}
	got := names(entries)
	want := []string{"a.go", "b.go", "go.mod", "sub"}
	if !slices.Equal(got, want) {
		t.Errorf("root entries: got %v, want %v", got, want)
	}
	for _, e := range entries {
		if e.Name() == "sub" && !e.IsDir() {
			t.Error("sub should be a directory")
		}
		if e.Name() == "a.go" && e.IsDir() {
			t.Error("a.go should not be a directory")
		}
	}

	subEntries, err := fs.ReadDir(f, "github.com/foo/bar/sub")
	if err != nil {
		t.Fatalf("ReadDir sub: %v", err)
	}
	if got, want := names(subEntries), []string{"c.go"}; !slices.Equal(got, want) {
		t.Errorf("sub entries: got %v, want %v", got, want)
	}
}

func TestStat(t *testing.T) {
	p := &fakeProxy{
		t:      t,
		latest: map[string]string{"github.com/foo/bar": "v1.0.0"},
		modules: map[string]map[string]string{
			"github.com/foo/bar@v1.0.0": {
				"a.go":     "package bar\n",
				"sub/c.go": "package sub\n",
			},
		},
	}
	f := newTestFS(t, p)

	fi, err := fs.Stat(f, "github.com/foo/bar")
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if !fi.IsDir() {
		t.Error("module root should be a dir")
	}

	fi, err = fs.Stat(f, "github.com/foo/bar/a.go")
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if fi.IsDir() {
		t.Error("a.go should not be a dir")
	}
	if fi.Size() == 0 {
		t.Error("a.go size should be > 0")
	}

	if _, err := fs.Stat(f, "github.com/foo/bar/missing.go"); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestCaseEncoding(t *testing.T) {
	p := &fakeProxy{
		t:      t,
		latest: map[string]string{"github.com/Foo/Bar": "v1.0.0"},
		modules: map[string]map[string]string{
			"github.com/Foo/Bar@v1.0.0": {
				"main.go": "package bar\n",
			},
		},
	}
	f := newTestFS(t, p)

	data, err := fs.ReadFile(f, "github.com/Foo/Bar/main.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "package bar") {
		t.Errorf("unexpected: %q", data)
	}
}

func TestNotFound(t *testing.T) {
	p := &fakeProxy{t: t, latest: map[string]string{}, modules: map[string]map[string]string{}}
	f := newTestFS(t, p)

	if _, err := fs.ReadFile(f, "github.com/nope/nope/x.go"); err == nil {
		t.Error("expected error for missing module")
	}
}

func TestNegativeCache(t *testing.T) {
	// Probe should not retry the same missing module on subsequent calls.
	p := &fakeProxy{t: t, latest: map[string]string{}, modules: map[string]map[string]string{}}
	f := newTestFS(t, p)

	for range 3 {
		_, _ = fs.ReadFile(f, "example.com/missing/pkg/a.go")
	}
	// First call probes 3 candidates (example.com/missing,
	// example.com/missing/pkg, example.com/missing/pkg/a.go) -> 3 fetches.
	// Subsequent calls hit the negative cache and issue 0 fetches.
	if got := atomic.LoadInt64(&p.requests); got != 3 {
		t.Errorf("expected 3 proxy requests across 3 lookups, got %d", got)
	}
}

func TestProbeFindsModule(t *testing.T) {
	// Shortest-first probing must find the module even when the import
	// path has more components than the module path.
	p := &fakeProxy{
		t:      t,
		latest: map[string]string{"github.com/foo/bar": "v1.0.0"},
		modules: map[string]map[string]string{
			"github.com/foo/bar@v1.0.0": {"deep/nested/x.go": "package nested\n"},
		},
	}
	f := newTestFS(t, p)
	if _, err := fs.ReadFile(f, "github.com/foo/bar/deep/nested/x.go"); err != nil {
		t.Fatalf("read: %v", err)
	}
	// Expected: 1 probe miss (github.com/foo) + @latest + zip = 3 requests.
	if got := atomic.LoadInt64(&p.requests); got != 3 {
		t.Errorf("expected 3 proxy requests (1 probe miss + latest + zip), got %d", got)
	}
}

func TestMajorVersionSuffix(t *testing.T) {
	// The v1-era module path also answers 200 but lacks the "v4" sub-path,
	// so probing must skip it and resolve the v4 module instead.
	p := &fakeProxy{
		t: t,
		latest: map[string]string{
			"github.com/blang/semver":    "v2.2.0+incompatible",
			"github.com/blang/semver/v4": "v4.0.0",
		},
		modules: map[string]map[string]string{
			"github.com/blang/semver@v2.2.0+incompatible": {"semver.go": "package semver\n"},
			"github.com/blang/semver/v4@v4.0.0":           {"semver.go": "package semver\n// v4\n"},
		},
	}
	f := newTestFS(t, p)
	data, err := fs.ReadFile(f, "github.com/blang/semver/v4/semver.go")
	if err != nil {
		t.Fatalf("read semver/v4: %v", err)
	}
	if !strings.Contains(string(data), "// v4") {
		t.Errorf("resolved wrong module; got %q", data)
	}
}

func TestEscapePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"github.com/foo/bar", "github.com/foo/bar"},
		{"github.com/Foo/Bar", "github.com/!foo/!bar"},
		{"v1.2.3", "v1.2.3"},
		{"AB", "!a!b"},
	}
	for _, c := range cases {
		got, err := escapePath(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("escapePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := escapePath("é"); err == nil {
		t.Error("expected error for non-ASCII")
	}
}

func TestInject(t *testing.T) {
	zipBytes, err := buildZip("github.com/mvm-sh/std", "v0.1.0", map[string]string{
		"go.mod":           "module github.com/mvm-sh/std\n",
		"errors/errors.go": "package errors\nfunc New(s string) error { return nil }\n",
	})
	if err != nil {
		t.Fatalf("buildZip: %v", err)
	}
	// Use Offline so any miss returns ErrNotExist instead of dialing the
	// (unreachable) DefaultProxy.
	f := New(Options{Offline: true})
	if err := f.Inject("github.com/mvm-sh/std", "v0.1.0", zipBytes); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	data, err := fs.ReadFile(f, "github.com/mvm-sh/std/errors/errors.go")
	if err != nil {
		t.Fatalf("read errors.go: %v", err)
	}
	if !strings.Contains(string(data), "package errors") {
		t.Errorf("unexpected: %q", data)
	}

	// Non-injected lookup must fail fast in offline mode.
	if _, err := fs.ReadFile(f, "github.com/other/mod/x.go"); err == nil {
		t.Error("expected ErrNotExist for non-injected module in offline mode")
	}
}

func TestOfflineNoFetch(t *testing.T) {
	// Offline FS pointed at a counting proxy must never issue a request.
	p := &fakeProxy{t: t, latest: map[string]string{}, modules: map[string]map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	t.Cleanup(srv.Close)
	f := New(Options{Proxy: srv.URL, Offline: true})

	if _, err := fs.ReadFile(f, "github.com/anything/at/all.go"); err == nil {
		t.Error("expected error in offline mode")
	}
	if got := atomic.LoadInt64(&p.requests); got != 0 {
		t.Errorf("offline FS made %d proxy requests, want 0", got)
	}
}

func TestDiskCacheRoundTrip(t *testing.T) {
	p := &fakeProxy{
		t:       t,
		modules: map[string]map[string]string{"example.com/foo@v1.0.0": {"foo.go": "package foo\n"}},
		latest:  map[string]string{"example.com/foo": "v1.0.0"},
	}
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	t.Cleanup(srv.Close)
	dir := t.TempDir()

	// First load over the network populates the cache.
	f1 := New(Options{Proxy: srv.URL, CacheDir: dir})
	if _, err := fs.ReadFile(f1, "example.com/foo/foo.go"); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if atomic.LoadInt64(&p.requests) == 0 {
		t.Fatal("expected proxy requests on first load")
	}
	for _, rel := range []string{"example.com/foo/@v/v1.0.0.zip", "example.com/foo/@latest"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("cache file %s missing: %v", rel, err)
		}
	}

	// A fresh offline FS over the same cache serves without any network.
	p.requests = 0
	f2 := New(Options{Proxy: srv.URL, CacheDir: dir, Offline: true})
	data, err := fs.ReadFile(f2, "example.com/foo/foo.go")
	if err != nil {
		t.Fatalf("cached offline load: %v", err)
	}
	if !strings.Contains(string(data), "package foo") {
		t.Errorf("unexpected content: %q", data)
	}
	if got := atomic.LoadInt64(&p.requests); got != 0 {
		t.Errorf("offline cached load made %d proxy requests, want 0", got)
	}
	if cs := f2.CacheStats(); cs.ZipHits != 1 || cs.ZipBytes == 0 || cs.ReadThroughHits != 0 {
		t.Errorf("CacheStats = %+v, want 1 hit, >0 bytes, 0 read-through", cs)
	}
	if cs := f1.CacheStats(); cs.ZipWrites != 1 {
		t.Errorf("first FS ZipWrites = %d, want 1", cs.ZipWrites)
	}
}

func TestReadThroughGoCache(t *testing.T) {
	// Proxy resolves @latest but has no zip; the zip lives only in a
	// read-only root, mimicking an existing Go module download cache.
	p := &fakeProxy{
		t:       t,
		modules: map[string]map[string]string{},
		latest:  map[string]string{"example.com/bar": "v2.1.0"},
	}
	srv := httptest.NewServer(http.HandlerFunc(p.handler))
	t.Cleanup(srv.Close)

	goCache := t.TempDir()
	zb, err := buildZip("example.com/bar", "v2.1.0", map[string]string{"bar.go": "package bar\n"})
	if err != nil {
		t.Fatal(err)
	}
	zp := filepath.Join(goCache, filepath.FromSlash("example.com/bar/@v/v2.1.0.zip"))
	if err := os.MkdirAll(filepath.Dir(zp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zp, zb, 0o644); err != nil {
		t.Fatal(err)
	}

	f := New(Options{Proxy: srv.URL, CacheDir: t.TempDir(), ReadDirs: []string{goCache}})
	if _, err := fs.ReadFile(f, "example.com/bar/bar.go"); err != nil {
		t.Fatalf("read via Go-cache read-through: %v", err)
	}
	if cs := f.CacheStats(); cs.ZipHits != 1 || cs.ReadThroughHits != 1 || cs.ZipWrites != 0 {
		t.Errorf("CacheStats = %+v, want 1 hit, 1 read-through, 0 writes", cs)
	}
}

func names(entries []fs.DirEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name()
	}
	return out
}

// closeTrackingTransport records whether CloseIdleConnections was called.
type closeTrackingTransport struct{ closed bool }

func (t *closeTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fs.ErrNotExist // never used by the test
}
func (t *closeTrackingTransport) CloseIdleConnections() { t.closed = true }

// nil client, default client (nil Transport), and a custom transport whose
// CloseIdleConnections must be reached.
func TestCloseIdleConnections(t *testing.T) {
	(&FS{}).CloseIdleConnections()        // nil client: no panic
	New(Options{}).CloseIdleConnections() // default client, Transport nil: no panic

	ct := &closeTrackingTransport{}
	New(Options{Client: &http.Client{Transport: ct}}).CloseIdleConnections()
	if !ct.closed {
		t.Error("CloseIdleConnections did not reach the client transport")
	}
}
