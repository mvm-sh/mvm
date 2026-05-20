// Package stdmod redirects stdlib-shaped import paths to a
// std module hosted on the Go module proxy. The mvm binary embeds a
// proxy-format zip of this module as an offline floor.
package stdmod

import (
	"io/fs"
	"os"
	"strings"
	"sync"

	"github.com/mvm-sh/mvm/modfs"
	"github.com/mvm-sh/mvm/stdlib"
)

// ModulePath and Version pin the std module shipped with mvm. The
// embedded src.zip must match these values so that modfs.Inject keys the
// module under the same path.
const (
	ModulePath = "github.com/mvm-sh/std"
	Version    = "v0.1.0"
)

// Resolve returns the active std module path and version, applying any
// MVMSTD override in the format "<ModulePath>@<Version>".
func Resolve() (modPath, version string) {
	if v := os.Getenv("MVMSTD"); v != "" {
		if at := strings.LastIndex(v, "@"); at > 0 {
			return v[:at], v[at+1:]
		}
		return v, Version
	}
	return ModulePath, Version
}

// IsStdlibImport reports whether path looks like a Go stdlib import.
func IsStdlibImport(path string) bool {
	return stdlib.IsStdlibImport(path)
}

// FS returns an fs.FS that serves stdlib-shaped imports by rewriting them
// to "<modPath>/<path>" against the given backing modfs. Non-stdlib
// imports return fs.ErrNotExist.
func FS(backing *modfs.FS) fs.FS {
	mp, _ := Resolve()
	return &redirectFS{backing: backing, modPath: mp}
}

// DefaultFS returns a stdlib redirect FS backed by an offline modfs
// pre-populated with the embedded std zip.
func DefaultFS() fs.FS { return defaultFS() }

var defaultFS = sync.OnceValue(func() fs.FS {
	mp, ver := Resolve()
	mfs := modfs.New(modfs.Options{Offline: true})
	if err := mfs.Inject(mp, ver, stdlib.EmbeddedStd()); err != nil {
		panic("stdmod: inject embedded std: " + err.Error())
	}
	return FS(mfs)
})

type redirectFS struct {
	backing *modfs.FS
	modPath string
}

func (r *redirectFS) rewrite(name string) (string, bool) {
	if !IsStdlibImport(name) {
		return "", false
	}
	return r.modPath + "/" + name, true
}

func (r *redirectFS) Open(name string) (fs.File, error) {
	rw, ok := r.rewrite(name)
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return r.backing.Open(rw)
}

func (r *redirectFS) Stat(name string) (fs.FileInfo, error) {
	rw, ok := r.rewrite(name)
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return r.backing.Stat(rw)
}

func (r *redirectFS) ReadDir(name string) ([]fs.DirEntry, error) {
	rw, ok := r.rewrite(name)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return r.backing.ReadDir(rw)
}
