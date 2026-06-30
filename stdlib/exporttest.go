package stdlib

import (
	"go/token"
	"maps"
	"math"
	"reflect"
	"strings"
)

// TestValues holds faithful stand-ins for symbols that a stdlib package's own
// export_test.go injects into the package under test.
//
// Merged over Values ONLY by `mvm test` (see TestOverlay); `mvm run` never
// sees these, so the real package surface stays clean.
//
// Only faithfully-reproducible symbols belong here.
var TestValues = map[string]map[string]reflect.Value{
	"math": {
		"ExpGo":           reflect.ValueOf(math.Exp),
		"Exp2Go":          reflect.ValueOf(math.Exp2),
		"HypotGo":         reflect.ValueOf(math.Hypot),
		"SqrtGo":          reflect.ValueOf(math.Sqrt),
		"TrigReduce":      reflect.ValueOf(trigReduce),
		"ReduceThreshold": reflect.ValueOf(float64(reduceThreshold)),
	},
	"math/bits": {
		"DeBruijn64": reflect.ValueOf(uint64(0x03f79d71b4ca8b09)),
	},
	"reflect": {
		// export_test.go: MapGroupOf = groupAndSlotOf's group type, built from
		// public reflect API + abi constants (MapGroupSlots=8, MapMax*Bytes=128).
		"MapGroupOf": reflect.ValueOf(func(x, y reflect.Type) reflect.Type {
			if x.Size() > 128 {
				x = reflect.PointerTo(x)
			}
			if y.Size() > 128 {
				y = reflect.PointerTo(y)
			}
			slot := reflect.StructOf([]reflect.StructField{
				{Name: "Key", Type: x},
				{Name: "Elem", Type: y},
			})
			return reflect.StructOf([]reflect.StructField{
				{Name: "Ctrl", Type: reflect.TypeFor[uint64]()},
				{Name: "Slots", Type: reflect.ArrayOf(8, slot)},
			})
		}),
	},
	"fmt": {
		"IsSpace":  reflect.ValueOf(fmtIsSpace),
		"Parsenum": reflect.ValueOf(fmtParsenum),
	},
	"bytes": {
		// Verbatim indexBytePortable (bytes.go); the bridge has only optimized IndexByte.
		"IndexBytePortable": reflect.ValueOf(func(s []byte, c byte) int {
			for i, b := range s {
				if b == c {
					return i
				}
			}
			return -1
		}),
	},
	"strings": {
		"StringFind": reflect.ValueOf(func(pattern, text string) int {
			return strings.Index(text, pattern)
		}),
		"DumpTables": reflect.ValueOf(func(pattern string) ([]int, []int) {
			f := makeStringFinder(pattern)
			return f.badCharSkip[:], f.goodSuffixSkip
		}),
	},
	"go/types": {
		// Exported by util_test.go (internal) for external tests; absent on the bridge.
		"CmpPos": reflect.ValueOf(func(p, q token.Pos) int { return int(p - q) }),
	},
}

// TestOverlay returns each TestValues package merged over its Values base, so
// a single ImportPackageValues installs the package with both its real bridge
// symbols and the test-only stand-ins.
func TestOverlay() map[string]map[string]reflect.Value {
	out := make(map[string]map[string]reflect.Value, len(TestValues))
	for pkg, syms := range TestValues {
		m := make(map[string]reflect.Value, len(Values[pkg])+len(syms))
		maps.Copy(m, Values[pkg])
		maps.Copy(m, syms)
		out[pkg] = m
	}
	return out
}

// stringFinder, makeStringFinder, and longestCommonSuffix are ported verbatim
// from $GOROOT/src/strings/search.go (BSD-licensed, The Go Authors) so that
// DumpTables can reproduce the exact Boyer-Moore skip tables search_test.go
// asserts on. Keep in sync if the upstream algorithm changes.
type stringFinder struct {
	pattern        string
	badCharSkip    [256]int
	goodSuffixSkip []int
}

func makeStringFinder(pattern string) *stringFinder {
	f := &stringFinder{
		pattern:        pattern,
		goodSuffixSkip: make([]int, len(pattern)),
	}
	last := len(pattern) - 1

	// Bad-character table: bytes not in the pattern skip its whole length.
	for i := range f.badCharSkip {
		f.badCharSkip[i] = len(pattern)
	}
	for i := range last {
		f.badCharSkip[pattern[i]] = last - i
	}

	// Good-suffix table, first pass: next index starting a prefix of pattern.
	lastPrefix := last
	for i := last; i >= 0; i-- {
		if strings.HasPrefix(pattern, pattern[i+1:]) {
			lastPrefix = i + 1
		}
		f.goodSuffixSkip[i] = lastPrefix + last - i
	}
	// Second pass: repeats of the suffix starting from the front.
	for i := range last {
		lenSuffix := longestCommonSuffix(pattern, pattern[1:i+1])
		if pattern[i-lenSuffix] != pattern[last-lenSuffix] {
			f.goodSuffixSkip[last-lenSuffix] = lenSuffix + last - i
		}
	}

	return f
}

func longestCommonSuffix(a, b string) (i int) {
	for ; i < len(a) && i < len(b); i++ {
		if a[len(a)-1-i] != b[len(b)-1-i] {
			break
		}
	}
	return
}

// fmtIsSpace and fmtParsenum are ported verbatim from $GOROOT/src/fmt scan.go
// and print.go (BSD-licensed, The Go Authors) for fmt's export_test.go stand-ins.
var fmtSpace = [][2]uint16{
	{0x0009, 0x000d},
	{0x0020, 0x0020},
	{0x0085, 0x0085},
	{0x00a0, 0x00a0},
	{0x1680, 0x1680},
	{0x2000, 0x200a},
	{0x2028, 0x2029},
	{0x202f, 0x202f},
	{0x205f, 0x205f},
	{0x3000, 0x3000},
}

func fmtIsSpace(r rune) bool {
	if r >= 1<<16 {
		return false
	}
	rx := uint16(r)
	for _, rng := range fmtSpace {
		if rx < rng[0] {
			return false
		}
		if rx <= rng[1] {
			return true
		}
	}
	return false
}

func fmtParsenum(s string, start, end int) (num int, isnum bool, newi int) {
	if start >= end {
		return 0, false, end
	}
	for newi = start; newi < end && '0' <= s[newi] && s[newi] <= '9'; newi++ {
		if num > 1e6 || num < -1e6 {
			return 0, false, end
		}
		num = num*10 + int(s[newi]-'0')
		isnum = true
	}
	return
}
