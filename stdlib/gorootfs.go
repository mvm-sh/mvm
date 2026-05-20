package stdlib

import (
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// GorootTestFS returns an fs.FS rooted at $GOROOT/src, intended to serve
// stdlib *_test.go files for `mvm test <stdlib-path>` against the existing
// reflect bindings (e.g. `mvm test strings` loads strings_test.go from the
// host Go installation).
//
// Returns nil if GOROOT cannot be determined or its src/ subtree is absent.
// Callers (the parser's test-source FS hook) must accept nil and treat it
// as "no test sources available" rather than failing.
//
// Important: this FS is intentionally NOT chained into ordinary import
// resolution. Wiring it next to stdlibfs/remotefs would make `import
// "strings"` start loading interpreted source side-by-side with the
// reflect bridge, double-defining every exported symbol. It must only be
// consulted from the test-loading branch in LoadPackageSources.
func GorootTestFS() fs.FS {
	root := findGoroot()
	if root == "" {
		return nil
	}
	src := root + "/src"
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return nil
	}
	return &gorootFS{inner: os.DirFS(src)}
}

// findGoroot resolves the host Go installation's GOROOT, memoized for the
// process. Prefers the GOROOT env var when set; otherwise asks the `go`
// tool via `go env GOROOT` (a subprocess, hence the cache). Returns ""
// when neither is available.
var findGoroot = sync.OnceValue(func() string {
	if v := strings.TrimSpace(os.Getenv("GOROOT")); v != "" {
		return v
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

// GorootSrcDir returns the absolute path of $GOROOT/src/<importPath> on
// the host, or "" when GOROOT is unknown or the path is not a directory.
// Callers chdir there before driving bridged-stdlib tests so testdata-
// relative paths resolve against the host's stdlib source tree.
func GorootSrcDir(importPath string) string {
	root := findGoroot()
	if root == "" {
		return ""
	}
	dir := root + "/src/" + importPath
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// gorootFS wraps os.DirFS with a stdlib-shape guard: a path whose first
// segment contains '.' is treated as a non-stdlib import and refused, so
// accidental misuse cannot reach into a project checkout that happens to
// share $GOROOT/src as its parent directory. Only Open is implemented;
// io/fs's Stat/ReadDir/ReadFile helpers route through it (os.DirFS files
// satisfy the file-level interfaces those helpers fall back to), so the
// guard applies to every access.
type gorootFS struct{ inner fs.FS }

func (g *gorootFS) Open(name string) (fs.File, error) {
	if !IsStdlibImport(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return g.inner.Open(name)
}

// IsStdlibImport reports whether path looks like a Go stdlib import path:
// non-empty and with no dot in its first segment (which would mark a
// module-qualified third-party path like example.com/x).
func IsStdlibImport(name string) bool {
	if name == "" || name == "." {
		return false
	}
	first := name
	if i := strings.IndexByte(name, '/'); i >= 0 {
		first = name[:i]
	}
	return !strings.ContainsRune(first, '.')
}
