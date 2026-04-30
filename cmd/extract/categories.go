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

// SymbolBuildTags lists individual symbols that exist only on certain Go
// releases, keyed by import path then by build expression. Tagged symbols
// are emitted in a supplement file <pkg>_<suffix>.go (e.g. crypto_go126.go)
// guarded by //go:build <expr>; untagged symbols stay in the base file.
var SymbolBuildTags = map[string]map[string][]string{
	"crypto": {
		"go1.26": {"Decapsulator", "Encapsulator"},
	},
	"crypto/ecdh": {
		"go1.26": {"KeyExchanger"},
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
		"go1.26": {"ParseDirective", "Directive", "DirectiveArg"},
	},
	"log/slog": {
		"go1.26": {"NewMultiHandler", "MultiHandler"},
	},
	"net/http": {
		"go1.26": {"ClientConn"},
	},
	"os": {
		"go1.26": {"ErrNoHandle"},
	},
}

// tagFileSuffix maps a build expression to the filename suffix used for
// supplement files. Only expressions actually used in SymbolBuildTags need
// to be listed.
var tagFileSuffix = map[string]string{
	"go1.26": "go126",
}

func subDir(importPath string) string {
	if Core[importPath] {
		return "core"
	}
	return "ext"
}
