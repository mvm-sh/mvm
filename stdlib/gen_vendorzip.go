//go:build ignore

// gen_vendorzip builds trimmed Go-module-proxy-format zips for the
// third-party packages that an interpreted net/http imports
// (golang.org/x/net and golang.org/x/text). Only the packages net/http
// actually reaches on wasm are included -- not the whole modules -- so
// the embedded payload stays small (the full x/text module is ~7 MB of
// source; the four packages used here are a fraction of that).
//
// Source is the extracted module cache ($GOMODCACHE/<module>@<version>).
// Output zips have all entries rooted at "<module>@<version>/", which
// modfs parses natively, so the same zips feed both the embedded path
// (wasm binary, via stdlib.EmbeddedXNet/EmbeddedXText) and a live proxy
// fetch on native builds.
//
// Run with: go generate ./stdlib/...
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type vendorMod struct {
	path    string
	version string
	out     string
	pkgs    []string // package subdirs to include
}

// Versions track what net/http was validated against; bump together with
// the std mirror on a Go toolchain upgrade and re-run `make diff-upstream`.
var vendorMods = []vendorMod{
	{
		path:    "golang.org/x/net",
		version: "v0.56.0",
		out:     "xnet.zip",
		pkgs:    []string{"http/httpguts", "http/httpproxy", "idna"},
	},
	{
		path:    "golang.org/x/text",
		version: "v0.38.0",
		out:     "xtext.zip",
		pkgs:    []string{"secure/bidirule", "transform", "unicode/bidi", "unicode/norm"},
	},
}

func main() {
	if wd, err := os.Getwd(); err != nil {
		log.Fatal(err)
	} else if filepath.Base(wd) != "stdlib" {
		log.Fatalf("run from stdlib/ directory (cwd=%s)", wd)
	}

	cache := goModCache()
	for _, m := range vendorMods {
		root := filepath.Join(cache, filepath.FromSlash(m.path)+"@"+m.version)
		if _, err := os.Stat(root); err != nil {
			if err := downloadModule(m.path, m.version); err != nil {
				log.Fatalf("module not in cache and download failed: %s@%s: %v", m.path, m.version, err)
			}
			if _, err := os.Stat(root); err != nil {
				log.Fatalf("module not in cache after download: %s@%s: %v", m.path, m.version, err)
			}
		}
		n, size, err := writeVendorZip(m, root)
		if err != nil {
			log.Fatalf("%s: %v", m.out, err)
		}
		fmt.Printf("wrote %s (%d bytes, %d files, %s@%s)\n", m.out, size, n, m.path, m.version)
	}
}

// downloadModule fetches module@version into the cache. The explicit
// path@version form does not modify go.mod, so it works in mvm's
// zero-dependency module.
func downloadModule(path, version string) error {
	cmd := exec.Command("go", "mod", "download", path+"@"+version)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod download %s@%s: %v: %s", path, version, err, out)
	}
	return nil
}

func goModCache() string {
	if v := strings.TrimSpace(os.Getenv("GOMODCACHE")); v != "" {
		return v
	}
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		log.Fatalf("go env GOMODCACHE: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func writeVendorZip(m vendorMod, root string) (nFiles int, size int, err error) {
	var rels []string
	for _, name := range []string{"go.mod", "LICENSE"} {
		if _, e := os.Stat(filepath.Join(root, name)); e == nil {
			rels = append(rels, name)
		}
	}
	for _, pkg := range m.pkgs {
		entries, e := os.ReadDir(filepath.Join(root, filepath.FromSlash(pkg)))
		if e != nil {
			return 0, 0, fmt.Errorf("package %s: %w", pkg, e)
		}
		for _, d := range entries {
			n := d.Name()
			if d.IsDir() || filepath.Ext(n) != ".go" || strings.HasSuffix(n, "_test.go") {
				continue
			}
			rels = append(rels, pkg+"/"+n)
		}
	}
	sort.Strings(rels)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	prefix := m.path + "@" + m.version + "/"
	for _, rel := range rels {
		w, e := zw.Create(prefix + rel)
		if e != nil {
			return 0, 0, e
		}
		data, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if e != nil {
			return 0, 0, e
		}
		if _, e := w.Write(data); e != nil {
			return 0, 0, e
		}
	}
	if e := zw.Close(); e != nil {
		return 0, 0, e
	}
	if e := os.WriteFile(m.out, buf.Bytes(), 0o644); e != nil {
		return 0, 0, e
	}
	return len(rels), buf.Len(), nil
}
