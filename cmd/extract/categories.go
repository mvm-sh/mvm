package main

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

// BuildTags is an optional per-package //go:build expression to emit at the
// top of the generated file. Used to keep cgo-only bindings out of builds
// where cgo is disabled (notably GOOS=js GOARCH=wasm), and to gate bindings
// for stdlib packages that only exist in newer Go releases.
var BuildTags = map[string]string{
	"runtime/cgo":        "cgo",
	"crypto/hpke":        "go1.26",
	"testing/cryptotest": "go1.26",
}

func subDir(importPath string) string {
	if Core[importPath] {
		return "core"
	}
	return "ext"
}
