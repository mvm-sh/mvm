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
	"runtime/cgo":        "cgo",
	"crypto/hpke":        "go1.26",
	"testing/cryptotest": "go1.26",
	"testing/synctest":   "go1.25", // GA in go1.25; GOEXPERIMENT-gated in go1.24
	// Interpreted from the mirror on wasm, not bridged; each must be in ~/src/std.
	"fmt":           "!wasm",
	"strconv":       "!wasm",
	"strings":       "!wasm",
	"bytes":         "!wasm",
	"bufio":         "!wasm",
	"sort":          "!wasm",
	"unicode":       "!wasm",
	"unicode/utf8":  "!wasm",
	"unicode/utf16": "!wasm",
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

var WasmDropExact = map[string]bool{
	"database/sql":        true,
	"database/sql/driver": true,
	"expvar":              true,
	"index/suffixarray":   true,
	"math/big":            true,
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
// Tagged symbols are emitted in a supplement file <pkg>_<suffix>.go (e.g. crypto_go126.go).
//
// This list is hand-maintained.
var SymbolBuildTags = map[string]map[string][]string{
	"crypto": {
		"go1.25": {"MessageSigner", "SignMessage"},
		"go1.26": {"Decapsulator", "Encapsulator"},
	},
	"crypto/ecdh": {
		"go1.26": {"KeyExchanger"},
	},
	"crypto/ecdsa": {
		"go1.25": {"ParseRawPrivateKey", "ParseUncompressedPublicKey"},
	},
	"crypto/fips140": {
		"go1.26": {"Enforced", "Version", "WithoutEnforcement"},
	},
	"crypto/rsa": {
		"go1.26": {"EncryptOAEPWithOptions"},
	},
	"crypto/tls": {
		"go1.26": {"QUICErrorEvent", "SecP256r1MLKEM768", "SecP384r1MLKEM1024"},
	},
	"crypto/x509": {
		"go1.26": {"OIDFromASN1OID"},
	},
	"debug/elf": {
		"go1.25": {"PT_RISCV_ATTRIBUTES", "SHT_RISCV_ATTRIBUTES"},
		"go1.26": {
			"R_LARCH_CALL36",
			"R_LARCH_TLS_DESC32",
			"R_LARCH_TLS_DESC64",
			"R_LARCH_TLS_DESC64_HI12",
			"R_LARCH_TLS_DESC64_LO20",
			"R_LARCH_TLS_DESC64_PC_HI12",
			"R_LARCH_TLS_DESC64_PC_LO20",
			"R_LARCH_TLS_DESC_CALL",
			"R_LARCH_TLS_DESC_HI20",
			"R_LARCH_TLS_DESC_LD",
			"R_LARCH_TLS_DESC_LO12",
			"R_LARCH_TLS_DESC_PCREL20_S2",
			"R_LARCH_TLS_DESC_PC_HI20",
			"R_LARCH_TLS_DESC_PC_LO12",
			"R_LARCH_TLS_GD_PCREL20_S2",
			"R_LARCH_TLS_LD_PCREL20_S2",
			"R_LARCH_TLS_LE_ADD_R",
			"R_LARCH_TLS_LE_HI20_R",
			"R_LARCH_TLS_LE_LO12_R",
		},
	},
	"go/ast": {
		"go1.25": {"PreorderStack"},
		"go1.26": {"ParseDirective", "Directive", "DirectiveArg"},
	},
	"go/types": {
		"go1.25": {
			"FieldVar", "LocalVar", "PackageVar", "ParamVar", "RecvVar", "ResultVar",
			"LookupSelection", "VarKind",
		},
	},
	"hash": {
		"go1.25": {"Cloner", "XOF"},
	},
	"io/fs": {
		"go1.25": {"Lstat", "ReadLink", "ReadLinkFS"},
	},
	"log/slog": {
		"go1.25": {"GroupAttrs"},
		"go1.26": {"NewMultiHandler", "MultiHandler"},
	},
	"mime/multipart": {
		"go1.25": {"FileContentDisposition"},
	},
	"net/http": {
		"go1.25": {"CrossOriginProtection", "NewCrossOriginProtection"},
		"go1.26": {"ClientConn"},
	},
	"os": {
		"go1.26": {"ErrNoHandle"},
	},
	"runtime": {
		"go1.25": {"SetDefaultGOMAXPROCS"},
	},
	"runtime/trace": {
		"go1.25": {"FlightRecorder", "FlightRecorderConfig", "NewFlightRecorder"},
	},
	"unicode": {
		"go1.25": {"CategoryAliases", "Cn", "LC"},
	},
}

// tagFileSuffix maps a build expression to the filename suffix used for
// supplement files. Only expressions actually used in SymbolBuildTags need
// to be listed.
var tagFileSuffix = map[string]string{
	"go1.25": "go125",
	"go1.26": "go126",
}

func subDir(importPath string) string {
	if Core[importPath] {
		return "core"
	}
	return "ext"
}
