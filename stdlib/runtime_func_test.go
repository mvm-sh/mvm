package stdlib

import (
	"runtime"
	"sort"
	"testing"
	"unsafe"
)

// TestRuntimeFuncSentinelStride guards against a regression where two
// adjacent sentinels share enough address space to alias each other
// under the `pc-1 / pc` lookup heuristic that pkg/errors-style PC+1
// callers use. With a 1-byte sentinel struct, Go's tiny allocator
// packs consecutive allocations 1 byte apart and `(sentinel_b - 1) ==
// sentinel_a`, so a lookup against sentinel_b's PC returns sentinel_a's
// metadata instead. The padding in runtimeFuncSentinel must keep
// sentinels at least 2 bytes apart.
func TestRuntimeFuncSentinelStride(t *testing.T) {
	const n = 32
	rfs := make([]*runtime.Func, n)
	for i := range rfs {
		rfs[i] = NewRuntimeFuncSentinel()
	}
	addrs := make([]uintptr, n)
	for i, rf := range rfs {
		addrs[i] = uintptr(unsafe.Pointer(rf))
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i] < addrs[j] })
	for i := 1; i < n; i++ {
		// pkg/errors stores PCs as sentinel+1 and recovers via pc-1, so
		// adjacent sentinels exactly 1 byte apart make (b-1) alias a.
		if d := addrs[i] - addrs[i-1]; d < 2 {
			t.Errorf("sentinels at %x and %x are %d byte(s) apart, "+
				"will alias under pc-1 lookup", addrs[i-1], addrs[i], d)
		}
	}
}

// TestRuntimeFuncSentinelLookupNoAlias confirms that registering N
// distinct sentinels and looking each one up via the (sentinel+1)-1
// convention returns its own metadata, never an adjacent sentinel's.
func TestRuntimeFuncSentinelLookupNoAlias(t *testing.T) {
	const n = 8
	rfs := make([]*runtime.Func, n)
	for i := range rfs {
		rfs[i] = NewRuntimeFuncSentinel()
		RegisterRuntimeFunc(rfs[i], "fn"+string(rune('a'+i)), "f.go", i+1)
	}
	for i, rf := range rfs {
		// pkg/errors stores PCs as Frame(sentinel+1); LookupRuntimeFuncByPC
		// recovers the sentinel via pc-1 just like mvmFuncForPC.
		pc := uintptr(unsafe.Pointer(rf)) + 1
		got, info := LookupRuntimeFuncByPC(pc)
		if info == nil {
			t.Errorf("frame %d: pc-1 lookup returned nil", i)
			continue
		}
		if got != rf {
			t.Errorf("frame %d: pc-1 lookup returned a different sentinel", i)
		}
		want := "fn" + string(rune('a'+i))
		if info.Name != want {
			t.Errorf("frame %d: got name %q, want %q", i, info.Name, want)
		}
	}
}
