package main

import "strings"

// Core lists the stdlib import paths that go into stdlib/core/.
// Everything not listed here goes to stdlib/ext/.
// Pure-compute, browser-safe packages with modest transitive footprint belong here.
// Host-coupled or transitively heavy packages (net/*, os/*, syscall, crypto/*, image/*,
// runtime/*, ...) belong in ext.
var Core = map[string]bool{
	"bytes":           true,
	"cmp":             true,
	"container/heap":  true,
	"container/list":  true,
	"container/ring":  true,
	"context":         true,
	"encoding":        true,
	"encoding/base64": true,
	"encoding/hex":    true,
	"encoding/json":   true,
	"errors":          true,
	"fmt":             true,
	"hash":            true,
	"hash/adler32":    true,
	"hash/crc32":      true,
	"hash/crc64":      true,
	"hash/fnv":        true,
	"hash/maphash":    true,
	"io":              true,
	"iter":            true,
	"maps":            true,
	"math":            true,
	"math/big":        true,
	"math/bits":       true,
	"math/cmplx":      true,
	"math/rand":       true,
	"math/rand/v2":    true,
	"regexp":          true,
	"regexp/syntax":   true,
	"slices":          true,
	"sort":            true,
	"strconv":         true,
	"strings":         true,
	"structs":         true,
	"sync":            true,
	"sync/atomic":     true,
	"text/scanner":    true,
	"text/tabwriter":  true,
	"time":            true,
	"unicode":         true,
	"unicode/utf16":   true,
	"unicode/utf8":    true,
	"unique":          true,
	"unsafe":          true, // hand-written core/unsafe.go goes here too
	"weak":            true,
}

// BuildTags is an optional per-package //go:build expression.
// Used to keep cgo-only bindings out of builds  where cgo is disabled
// (notably GOOS=js GOARCH=wasm), and to gate bindings
// for whole stdlib packages that only build on newer Go releases.
// Per-symbol additions go in SymbolBuildTags instead.
var BuildTags = map[string]string{
	"runtime/cgo": "cgo",
	// Interpreted from the mirror on wasm, not bridged; each must be in ~/src/std.
	"fmt":             "!wasm",
	"strconv":         "!wasm",
	"strings":         "!wasm",
	"bytes":           "!wasm",
	"bufio":           "!wasm",
	"sort":            "!wasm",
	"unicode":         "!wasm",
	"unicode/utf8":    "!wasm",
	"unicode/utf16":   "!wasm",
	"io":              "!wasm",
	"io/fs":           "!wasm",
	"context":         "!wasm",
	"encoding":        "!wasm",
	"encoding/json":   "!wasm",
	"encoding/binary": "!wasm",
	"encoding/hex":    "!wasm",
	"encoding/base64": "!wasm",
	"container/heap":  "!wasm",
	"container/list":  "!wasm",
	"container/ring":  "!wasm",
	"flag":            "!wasm",
	"regexp":          "!wasm",
	"regexp/syntax":   "!wasm",
	"text/scanner":    "!wasm",
	"text/tabwriter":  "!wasm",
	"html":            "!wasm",
}

// WasmDropPrefixes and WasmDropExact tag bridges !wasm to shrink the binary; the
// linker then DCEs their unused exported functions. See docs/modules/stubs.md.
var WasmDropPrefixes = []string{
	"crypto",
	"net",
	"image",
	"debug",
	"go",
	"compress",
	"archive",
	"mime",
	"text/template",
	"html/template",
}

// WasmKeepExact overrides a WasmDrop prefix: these packages stay native bridges
// on wasm because an interpreted package needs them and they cross the boundary
// by value only (no interpreted-method dispatch, so no shared-PC trap).
var WasmKeepExact = map[string]bool{
	"crypto/rand": true, // mime/multipart Writer boundary; host random source
}

var WasmDropExact = map[string]bool{
	"database/sql":        true,
	"database/sql/driver": true,
	"expvar":              true,
	"index/suffixarray":   true,
	"os/user":             true,
	"log/syslog":          true,
	"encoding/gob":        true,
	"encoding/asn1":       true,
	"encoding/csv":        true,
	"encoding/xml":        true,
	"encoding/pem":        true,
	"encoding/ascii85":    true,
	"encoding/base32":     true,
	"runtime/pprof":       true,
	"runtime/trace":       true,
	"runtime/metrics":     true,
	"runtime/coverage":    true,
	"testing/fstest":      true,
	"testing/slogtest":    true,
	"testing/iotest":      true,
	"testing/cryptotest":  true,
}

func isWasmDropped(importPath string) bool {
	if WasmKeepExact[importPath] {
		return false
	}
	if WasmDropExact[importPath] {
		return true
	}
	for _, pre := range WasmDropPrefixes {
		if importPath == pre || strings.HasPrefix(importPath, pre+"/") {
			return true
		}
	}
	return false
}

// wasmDropTag folds !wasm into a dropped package's build constraint.
func wasmDropTag(importPath, tag string) string {
	if !isWasmDropped(importPath) {
		return tag
	}
	if tag == "" {
		return "!wasm"
	}
	return tag + " && !wasm"
}

// SymbolBuildTags lists individual exported symbols that were added in a newer
// Go release, keyed by import path then by build expression.
// Tagged symbols are emitted in a supplement file <pkg>_<suffix>.go (e.g. crypto_go127.go).
//
// Empty: the go.mod floor (go1.26) absorbs every symbol added through go1.26.
// Populate when a release past the floor adds symbols. This list is hand-maintained.
var SymbolBuildTags = map[string]map[string][]string{}

// tagFileSuffix maps a build expression to the filename suffix used for
// supplement files. Only expressions actually used in SymbolBuildTags need
// to be listed.
var tagFileSuffix = map[string]string{}

func subDir(importPath string) string {
	if Core[importPath] {
		return "core"
	}
	return "ext"
}
