// Package modfs implements an io/fs.FS backed by the Go module proxy
// protocol. It fetches modules over HTTP on demand and caches them in
// memory. With no CacheDir set it writes nothing to disk, so it stays
// usable from WASM and other restricted environments; set Options.CacheDir
// to also persist fetched module zips across runs.
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
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultProxy is the public Go module proxy.
const DefaultProxy = "https://proxy.golang.org"

// Options configures FS construction.
type Options struct {
	Proxy    string       // module proxy base URL. Empty means DefaultProxy.
	Client   *http.Client // for proxy requests. Empty means http.DefaultClient.
	Offline  bool         // disable proxy fetch if true.
	CacheDir string       // writable dir to store zip, etc. Empty keeps modfs in-memory.
	ReadDirs []string     // additional read-only roots searched.
}

// FS is an in-memory filesystem that resolves Go import paths against a module proxy.
type FS struct {
	proxy    string
	client   *http.Client
	offline  bool
	cacheDir string   // writable persistent cache root ("" = memory only)
	readDirs []string // read-only cache roots (e.g. Go's module download cache)

	mu       sync.Mutex
	modules  map[string]*module  // module path -> loaded module
	fallback map[string]*module  // last-resort modules; off the missing/ path entirely
	missing  map[string]struct{} // module-path candidates known to be absent
	stats    NetStats            // proxy traffic counters; guarded by mu
	cache    CacheStats          // disk-cache counters; guarded by mu
}

// NetStats summarizes the network work performed by an FS instance.
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

// CacheStats summarizes the on-disk cache work performed by an FS instance.
type CacheStats struct {
	ZipHits         int   // module zips served from disk instead of the network
	ZipBytes        int64 // bytes of those cached zips
	ReadThroughHits int   // subset of ZipHits served from a read-only root
	ZipWrites       int   // zips persisted to CacheDir after a fetch
}

// CacheStats returns a snapshot of the FS's disk-cache counters.
func (f *FS) CacheStats() CacheStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cache
}

// CloseIdleConnections closes keep-alive proxy connections held by the HTTP client's transport.
func (f *FS) CloseIdleConnections() {
	if f.client == nil {
		return
	}
	rt := f.client.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	if c, ok := rt.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// countingReader wraps an io.Reader and tallies bytes consumed.
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
		proxy:    proxy,
		client:   client,
		offline:  opts.Offline,
		cacheDir: opts.CacheDir,
		readDirs: opts.ReadDirs,
		modules:  map[string]*module{},
		fallback: map[string]*module{},
		missing:  map[string]struct{}{},
	}
}

// Inject installs a pre-fetched module into the in-memory cache.
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

// InjectFallback installs a module consulted only when normal resolution fails,
// for trimmed copies that would otherwise hide the real module's other packages.
// It leaves missing alone: a fallback does not make the real module reachable.
func (f *FS) InjectFallback(modPath, version string, zipBytes []byte) error {
	mod, err := newModule(modPath, version, zipBytes)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback[modPath] = mod
	return nil
}

// Open implements fs.FS. The name is an import path or a path inside one.
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

// locate returns the owning module and the path within that module for the given import path.
// Real modules are probed before any fallback, never interleaved by prefix length.
func (f *FS) locate(importPath string) (*module, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if m, sub, ok := f.probe(importPath, f.resolveModule); ok {
		return m, sub, nil
	}
	if len(f.fallback) > 0 {
		if m, sub, ok := f.probe(importPath, func(c string) *module { return f.fallback[c] }); ok {
			return m, sub, nil
		}
	}
	return nil, "", fs.ErrNotExist
}

// probe returns the shortest module prefix of importPath that holds the rest of
// it (a module path has >=2 components).
// Candidates are prefixes, so slicing keeps the walk allocation-free.
func (f *FS) probe(importPath string, lookup func(string) *module) (*module, string, bool) {
	end := strings.IndexByte(importPath, '/')
	if end < 0 {
		return nil, "", false
	}
	for end < len(importPath) {
		if next := strings.IndexByte(importPath[end+1:], '/'); next < 0 {
			end = len(importPath)
		} else {
			end += 1 + next
		}
		m := lookup(importPath[:end])
		if m == nil {
			continue
		}
		sub := strings.TrimPrefix(importPath[end:], "/")
		if m.has(sub) {
			return m, sub, true
		}
		// Real module, wrong one for this import: try a longer prefix.
	}
	return nil, "", false
}

// resolveModule looks modPath up, negative-caching only definitive misses:
// caching a transient failure would pin the path to a trimmed fallback.
func (f *FS) resolveModule(modPath string) *module {
	if _, neg := f.missing[modPath]; neg {
		return nil
	}
	m, err := f.fetchModulePath(modPath)
	if err != nil {
		if !errors.Is(err, errTransient) {
			f.missing[modPath] = struct{}{}
		}
		return nil
	}
	return m
}

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
	version, err := f.resolveVersion(modPath)
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

func (f *FS) resolveVersion(modPath string) (string, error) {
	if v, ok := f.readCachedLatest(modPath); ok {
		return v, nil
	}
	if f.offline {
		if v, ok := f.readDirsLatest(modPath); ok {
			return v, nil
		}
		return "", fs.ErrNotExist
	}
	v, err := f.fetchLatest(modPath)
	if err != nil {
		// Only once the proxy is unreachable: a stale cache must not pin an old version.
		if cv, ok := f.readDirsLatest(modPath); ok {
			return cv, nil
		}
		return "", err
	}
	f.writeCachedLatest(modPath, v)
	return v, nil
}

// readDirsLatest returns the newest version of modPath a read-only root can serve.
// Go module caches have no @latest and a stale @v/list, so the zips are the truth.
// Newest-cached need not be the proxy's latest, so offline results vary by cache.
func (f *FS) readDirsLatest(modPath string) (string, bool) {
	escMod, err := escapePath(modPath)
	if err != nil {
		return "", false
	}
	best := ""
	for _, root := range f.readDirs {
		//nolint:gosec // G703: root derives from the user's own GOMODCACHE/GOPATH
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(escMod), "@v"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".zip") {
				continue
			}
			v := unescapeVersion(strings.TrimSuffix(name, ".zip"))
			if best == "" || versionLess(best, v) {
				best = v
			}
		}
	}
	return best, best != ""
}

// unescapeVersion reverses escapePath's "!x" case encoding.
func unescapeVersion(s string) string {
	if !strings.ContainsRune(s, '!') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '!' && i+1 < len(s) && 'a' <= s[i+1] && s[i+1] <= 'z' {
			b.WriteByte(s[i+1] - ('a' - 'A'))
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// versionLess orders module versions by semver precedence, enough to pick a newest.
// Build metadata is ignored; a prerelease sorts below its release.
func versionLess(a, b string) bool {
	an, apre := splitVersion(a)
	bn, bpre := splitVersion(b)
	for i := range 3 {
		if an[i] != bn[i] {
			return an[i] < bn[i]
		}
	}
	if (apre == "") != (bpre == "") {
		return apre != ""
	}
	return apre < bpre
}

// splitVersion parses "vX.Y.Z[-pre][+build]"; unparsable fields read as 0.
func splitVersion(v string) (num [3]int, pre string) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return num, pre
		}
		num[i] = n
	}
	return num, pre
}

// errTransient marks a recoverable fetch failure (no network, 5xx, throttling),
// as opposed to the proxy answering "no such module", which alone is cacheable.
var errTransient = errors.New("modfs: transient fetch failure")

func (f *FS) proxyGet(path string, consume func(io.Reader) error) error {
	url := f.proxy + "/" + path
	start := time.Now()
	f.stats.Requests++
	defer func() { f.stats.FetchTime += time.Since(start) }()
	resp, err := f.client.Get(url)
	if err != nil {
		return fmt.Errorf("%w: GET %s: %w", errTransient, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusGone {
			return fmt.Errorf("%w: GET %s: %s", errTransient, url, resp.Status)
		}
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
	if data, ok := f.readCachedZip(modPath, version); ok {
		return newModule(modPath, version, data)
	}
	if f.offline {
		return nil, fs.ErrNotExist
	}
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
	f.writeCachedZip(modPath, version, data)
	return newModule(modPath, version, data)
}

func zipCacheRel(modPath, version string) (string, bool) {
	escMod, err := escapePath(modPath)
	if err != nil {
		return "", false
	}
	escVer, err := escapePath(version)
	if err != nil {
		return "", false
	}
	return filepath.FromSlash(escMod + "/@v/" + escVer + ".zip"), true
}

func (f *FS) readCachedZip(modPath, version string) ([]byte, bool) {
	rel, ok := zipCacheRel(modPath, version)
	if !ok {
		return nil, false
	}
	if f.cacheDir != "" {
		if data, ok := f.tryCachedZip(filepath.Join(f.cacheDir, rel), false); ok {
			return data, true
		}
	}
	for _, root := range f.readDirs {
		if data, ok := f.tryCachedZip(filepath.Join(root, rel), true); ok {
			return data, true
		}
	}
	return nil, false
}

func (f *FS) tryCachedZip(path string, readOnly bool) ([]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	f.cache.ZipHits++
	f.cache.ZipBytes += int64(len(data))
	if readOnly {
		f.cache.ReadThroughHits++
	}
	return data, true
}

func (f *FS) writeCachedZip(modPath, version string, data []byte) {
	if f.cacheDir == "" {
		return
	}
	rel, ok := zipCacheRel(modPath, version)
	if !ok {
		return
	}
	if writeFileAtomic(filepath.Join(f.cacheDir, rel), data) == nil {
		f.cache.ZipWrites++
	}
}

func (f *FS) latestPath(modPath string) string {
	escMod, err := escapePath(modPath)
	if err != nil || f.cacheDir == "" {
		return ""
	}
	return filepath.Join(f.cacheDir, filepath.FromSlash(escMod), "@latest")
}

func (f *FS) readCachedLatest(modPath string) (string, bool) {
	p := f.latestPath(modPath)
	if p == "" {
		return "", false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	v := strings.TrimSpace(string(data))
	return v, v != ""
}

func (f *FS) writeCachedLatest(modPath, version string) {
	if p := f.latestPath(modPath); p != "" {
		_ = writeFileAtomic(p, []byte(version))
	}
}

func writeFileAtomic(name string, data []byte) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, name); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

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
