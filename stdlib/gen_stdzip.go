//go:build ignore

// gen_stdzip builds the embedded src.zip in Go module proxy layout
// from the sibling github.com/mvm-sh/std repo (cloned at ../../std
// relative to this script's working directory). The output is a
// single zip with all entries rooted at "<ModulePath>@<Version>/",
// matching what proxy.golang.org would serve for the std module.
// modfs parses this layout natively, so the same zip is consumed by
// both the embedded path (mvm binary) and the live proxy path
// (network fetch).
//
// Run with: go generate ./stdlib/...
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// srcDir is the path to the cloned std module, relative to this
	// script's cwd (stdlib/, set by go generate). Override with the
	// MVMSTD_SRC env var to point at a different clone or worktree.
	defaultSrcDir = "../../std"
	srcEnv        = "MVMSTD_SRC"
	zipPath       = "src.zip"
	modulePath    = "github.com/mvm-sh/std"
	moduleVer     = "v0.0.3"
)

// skipTop names top-level entries of the std repo that are repo
// scaffolding (build tooling, docs, VCS, override sources) rather than
// part of the module that the proxy would serve. They are excluded
// from the embedded zip.
var skipTop = map[string]bool{
	".git":       true,
	".gitignore": true,
	"Makefile":   true,
	"README.md":  true,
	"patches":    true,
}

func main() {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	if filepath.Base(wd) != "stdlib" {
		log.Fatalf("run from stdlib/ directory (cwd=%s)", wd)
	}

	srcDir := defaultSrcDir
	if v := os.Getenv(srcEnv); v != "" {
		srcDir = v
	}
	if _, err := os.Stat(srcDir); err != nil {
		log.Fatalf("std clone not found at %s: %v (override with %s=...)",
			srcDir, err, srcEnv)
	}

	paths, err := collect(srcDir)
	if err != nil {
		log.Fatal(err)
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	prefix := modulePath + "@" + moduleVer + "/"
	for _, p := range paths {
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			log.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		if err := writeZipEntry(zw, prefix+rel, p); err != nil {
			log.Fatalf("zip entry %s: %v", rel, err)
		}
	}
	if err := zw.Close(); err != nil {
		log.Fatal(err)
	}

	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %s (%d bytes, %d files, src=%s, prefix=%s)\n",
		zipPath, buf.Len(), len(paths), srcDir,
		strings.TrimSuffix(prefix, "/"))
}

// collect walks srcDir and returns the file paths to include in the
// proxy zip: go.mod and LICENSE at the root, plus every .go file
// (including *_test.go) under the per-package subdirectories, mirroring
// what a Go module proxy serves. Test files are kept so `mvm test <pkg>`
// runs a mirror-interpreted package's own suite; `import` resolution
// filters _test.go itself (LoadPackageSources, includeTests=false), so
// shipping them does not affect normal imports.
func collect(srcDir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(srcDir, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		if skipTop[parts[0]] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if len(parts) == 1 {
			// Root-level: keep the module file and the license;
			// everything else (NOTES, scripts, etc.) is repo-only.
			switch rel {
			case "go.mod", "LICENSE":
				paths = append(paths, p)
			}
			return nil
		}
		base := filepath.Base(p)
		if filepath.Ext(base) == ".go" {
			paths = append(paths, p)
		}
		return nil
	})
	return paths, err
}

func writeZipEntry(zw *zip.Writer, name, srcPath string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
