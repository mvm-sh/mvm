// Package modfs implements an io/fs.FS backed by the Go module proxy
// protocol. It fetches modules over HTTP on demand and caches them in
// memory, so no module sources are written to disk. This makes it usable
// from WASM and other restricted environments.
//
// The filesystem is plugged into goparser.Parser as a third-tier fallback
// after the user pkgfs and the embedded stdlib source FS, so any import
// path the parser cannot resolve locally is fetched dynamically.
package modfs

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultProxy is the public Go module proxy.
const DefaultProxy = "https://proxy.golang.org"

// Options configures FS construction.
type Options struct {
	// Proxy is the module proxy base URL. Empty means DefaultProxy.
	Proxy string
	// Client is the HTTP client used for proxy requests. Empty means
	// http.DefaultClient.
	Client *http.Client
	// Offline disables proxy fetches. Only modules added via Inject are
	// served; any other lookup returns fs.ErrNotExist. Useful for
	// playground/WASM and GOPROXY=off, where the embedded stdlib zip is the
	// sole source.
	Offline bool
}

// FS is an in-memory filesystem that resolves Go import paths against a
// module proxy. It implements fs.FS, fs.StatFS and fs.ReadDirFS.
type FS struct {
	proxy   string
	client  *http.Client
	offline bool

	mu      sync.Mutex
	modules map[string]*module  // module path -> loaded module
	missing map[string]struct{} // module-path candidates known to be absent
	stats   NetStats            // proxy traffic counters; guarded by mu
}

// NetStats summarizes the network work performed by an FS instance:
// proxy requests issued (200s and failures), bytes consumed from response
// bodies, and total wall-clock time spent in proxyGet.
//
// Cache hits, Inject calls, and offline lookups do not contribute -- only
// real HTTP requests do. Snapshot the counters with FS.NetStats.
type NetStats struct {
	Requests     int
	BytesFetched int64
	FetchTime    time.Duration
}

// NetStats returns a snapshot of the FS's proxy counters.
func (f *FS) NetStats() NetStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats
}

// countingReader wraps an io.Reader and tallies bytes consumed. Used by
// proxyGet to count what the consumer actually reads from the response body.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// module is a loaded module with files indexed by relative path.
type module struct {
	files      map[string][]byte        // path within module -> file bytes
	dirEntries map[string][]fs.DirEntry // dir path within module -> entries (sorted)
}

// New returns an FS configured from opts.
func New(opts Options) *FS {
	proxy := opts.Proxy
	if proxy == "" {
		proxy = DefaultProxy
	}
	proxy = strings.TrimRight(proxy, "/")
	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &FS{
		proxy:   proxy,
		client:  client,
		offline: opts.Offline,
		modules: map[string]*module{},
		missing: map[string]struct{}{},
	}
}

// Inject installs a pre-fetched module into the in-memory cache. The
// zipBytes must use the standard Go module proxy layout (entries rooted at
// "<modPath>@<version>/"). After Inject, lookups under modPath are served
// from memory without network access. Any prior negative cache entry for
// modPath itself is cleared so locate() can find it.
func (f *FS) Inject(modPath, version string, zipBytes []byte) error {
	mod, err := newModule(modPath, version, zipBytes)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modules[modPath] = mod
	delete(f.missing, modPath)
	return nil
}

// Open implements fs.FS. The name is an import path or a path inside one
// (e.g. "github.com/foo/bar" or "github.com/foo/bar/sub/file.go").
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	mod, sub, err := f.locate(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if data, ok := mod.files[sub]; ok {
		return &file{
			Reader: bytes.NewReader(data),
			info:   &fileInfo{name: path.Base(sub), size: int64(len(data))},
		}, nil
	}
	if entries, ok := mod.dirEntries[sub]; ok {
		return &dir{name: path.Base(sub), entries: entries}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// Stat implements fs.StatFS.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	fi, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fi.Close() }()
	return fi.Stat()
}

// ReadDir implements fs.ReadDirFS.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	mod, sub, err := f.locate(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	entries, ok := mod.dirEntries[sub]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]fs.DirEntry, len(entries))
	copy(out, entries)
	return out, nil
}

// locate returns the owning module and the path within that module for
// the given import path. The mutex is held across proxy requests; the
// parser is single-threaded so contention is not a concern.
func (f *FS) locate(importPath string) (*module, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Probe shortest-first (a module path has >=2 components). Take the first
	// prefix the proxy answers 200 for AND that owns the sub-path; misses are
	// negative-cached. The sub-path check handles semantic import versioning:
	// "github.com/blang/semver" also answers 200 for ".../semver/v4", so
	// without it we would pick the v1 module and miss the v4 subdir.
	parts := strings.Split(importPath, "/")
	for i := 2; i <= len(parts); i++ {
		cand := strings.Join(parts[:i], "/")
		if _, neg := f.missing[cand]; neg {
			continue
		}
		m, err := f.fetchModulePath(cand)
		if err != nil {
			f.missing[cand] = struct{}{}
			continue
		}
		sub := strings.TrimPrefix(strings.TrimPrefix(importPath, cand), "/")
		if !m.has(sub) {
			// Real module, wrong one for this import: try a longer prefix.
			// Not negative-cached, since the module itself exists.
			continue
		}
		return m, sub, nil
	}
	return nil, "", fs.ErrNotExist
}

// has reports whether the module contains sub as a file or dir ("" = root).
func (m *module) has(sub string) bool {
	if sub == "" {
		return true
	}
	if _, ok := m.files[sub]; ok {
		return true
	}
	_, ok := m.dirEntries[sub]
	return ok
}

func (f *FS) fetchModulePath(modPath string) (*module, error) {
	if mod, ok := f.modules[modPath]; ok {
		return mod, nil
	}
	if f.offline {
		return nil, fs.ErrNotExist
	}
	version, err := f.fetchLatest(modPath)
	if err != nil {
		return nil, err
	}
	mod, err := f.fetchModule(modPath, version)
	if err != nil {
		return nil, err
	}
	f.modules[modPath] = mod
	return mod, nil
}

// proxyGet fetches the given path under the proxy base URL and invokes
// consume on the response body. It handles status checking and body close.
//
// Every call -- including failures -- contributes to NetStats: a request
// is counted, bytes are tallied from a counting wrapper around the body,
// and wall-clock time is added. Callers already hold f.mu (via locate), so
// the counter writes are safe without separate locking.
func (f *FS) proxyGet(path string, consume func(io.Reader) error) error {
	url := f.proxy + "/" + path
	start := time.Now()
	f.stats.Requests++
	defer func() { f.stats.FetchTime += time.Since(start) }()
	resp, err := f.client.Get(url)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("modfs: GET %s: %s", url, resp.Status)
	}
	cr := &countingReader{r: resp.Body}
	err = consume(cr)
	f.stats.BytesFetched += cr.n
	return err
}

func (f *FS) fetchLatest(modPath string) (string, error) {
	esc, err := escapePath(modPath)
	if err != nil {
		return "", err
	}
	var info struct{ Version string }
	if err := f.proxyGet(esc+"/@latest", func(r io.Reader) error {
		return json.NewDecoder(r).Decode(&info)
	}); err != nil {
		return "", err
	}
	if info.Version == "" {
		return "", fmt.Errorf("modfs: empty version for %s", modPath)
	}
	return info.Version, nil
}

func (f *FS) fetchModule(modPath, version string) (*module, error) {
	esc, err := escapePath(modPath)
	if err != nil {
		return nil, err
	}
	escVer, err := escapePath(version)
	if err != nil {
		return nil, err
	}
	var data []byte
	if err := f.proxyGet(esc+"/@v/"+escVer+".zip", func(r io.Reader) error {
		var err error
		data, err = io.ReadAll(r)
		return err
	}); err != nil {
		return nil, err
	}
	return newModule(modPath, version, data)
}

// newModule parses a module zip into an in-memory module. The Go module
// proxy zips have all entries under "<modPath>@<version>/".
func newModule(modPath, version string, zipBytes []byte) (*module, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("modfs: zip %s@%s: %w", modPath, version, err)
	}
	prefix := modPath + "@" + version + "/"
	files := map[string][]byte{}
	// dir -> name -> isDir; later turned into sorted dirEntries.
	children := map[string]map[string]bool{"": {}}

	for _, zf := range zr.File {
		if !strings.HasPrefix(zf.Name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(zf.Name, prefix)
		if rel == "" || strings.HasSuffix(rel, "/") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		files[rel] = data

		// Walk parents and record (parent, child, isDir) tuples.
		cur := rel
		isDir := false
		for {
			i := strings.LastIndex(cur, "/")
			parent := ""
			name := cur
			if i >= 0 {
				parent = cur[:i]
				name = cur[i+1:]
			}
			if children[parent] == nil {
				children[parent] = map[string]bool{}
			}
			if isDir {
				children[parent][name] = true
			} else {
				if _, exists := children[parent][name]; !exists {
					children[parent][name] = false
				}
			}
			if i < 0 {
				break
			}
			cur = parent
			isDir = true
		}
	}

	dirEntries := map[string][]fs.DirEntry{}
	for d, kids := range children {
		entries := make([]fs.DirEntry, 0, len(kids))
		for name, isDir := range kids {
			full := name
			if d != "" {
				full = d + "/" + name
			}
			var size int64
			if !isDir {
				size = int64(len(files[full]))
			}
			entries = append(entries, &dirEntry{
				name:  name,
				isDir: isDir,
				size:  size,
			})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		dirEntries[d] = entries
	}

	return &module{
		files:      files,
		dirEntries: dirEntries,
	}, nil
}

// escapePath case-encodes the input as required by the module proxy
// protocol: every uppercase ASCII letter is replaced by "!" + lowercase.
// Non-ASCII runes are rejected to avoid silent mis-encoding.
func escapePath(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case 'A' <= r && r <= 'Z':
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		case r < 0x80:
			b.WriteRune(r)
		default:
			return "", fmt.Errorf("modfs: non-ASCII rune in %q", s)
		}
	}
	return b.String(), nil
}

// file is a fs.File wrapping byte-slice contents.
type file struct {
	*bytes.Reader
	info fs.FileInfo
}

func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *file) Close() error               { return nil }

// dir is an fs.File for a directory; it also implements fs.ReadDirFile.
type dir struct {
	name    string
	entries []fs.DirEntry
	pos     int
}

func (d *dir) Stat() (fs.FileInfo, error) {
	return &fileInfo{name: d.name, isDir: true}, nil
}

func (d *dir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: errors.New("is a directory")}
}

func (d *dir) Close() error { return nil }

func (d *dir) ReadDir(n int) ([]fs.DirEntry, error) {
	remaining := len(d.entries) - d.pos
	if remaining <= 0 {
		if n <= 0 {
			return nil, nil
		}
		return nil, io.EOF
	}
	count := remaining
	if n > 0 && n < count {
		count = n
	}
	out := make([]fs.DirEntry, count)
	copy(out, d.entries[d.pos:d.pos+count])
	d.pos += count
	return out, nil
}

type dirEntry struct {
	name  string
	isDir bool
	size  int64
}

func (d *dirEntry) Name() string { return d.name }
func (d *dirEntry) IsDir() bool  { return d.isDir }
func (d *dirEntry) Type() fs.FileMode {
	if d.isDir {
		return fs.ModeDir
	}
	return 0
}

func (d *dirEntry) Info() (fs.FileInfo, error) {
	return &fileInfo{name: d.name, isDir: d.isDir, size: d.size}, nil
}

type fileInfo struct {
	name  string
	isDir bool
	size  int64
}

func (f *fileInfo) Name() string { return f.name }
func (f *fileInfo) Size() int64  { return f.size }
func (f *fileInfo) Mode() fs.FileMode {
	if f.isDir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (f *fileInfo) ModTime() time.Time { return time.Time{} }
func (f *fileInfo) IsDir() bool        { return f.isDir }
func (f *fileInfo) Sys() any           { return nil }
