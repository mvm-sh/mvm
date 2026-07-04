//go:build wasm

package stdlib

// Wasm-only incompatibilities: these pass natively, so gate on GOARCH.
func init() {
	m := Incompat["encoding/gob"]
	if m == nil {
		m = map[string]string{}
		Incompat["encoding/gob"] = m
	}
	m["TestLargeSlice"] = "four parallel multi-MB slice encode/decodes exceed wasm's 4GB linear memory under the interpreter"
	m["TestCountEncodeMallocs"] = "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0"
	m["TestCountDecodeMallocs"] = "testing.AllocsPerRun observes mvm interpreter allocations; native expects 3"

	// mime is interpreted on wasm only; native bridges it and passes.
	Incompat["mime"] = map[string]string{
		"TestLookupMallocs": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
	}

	// Natively the alloc section is skipped via its GOMAXPROCS>1 guard;
	// wasm is single-threaded so it runs and sees interpreter allocations.
	m = Incompat["path/filepath"]
	if m == nil {
		m = map[string]string{}
		Incompat["path/filepath"] = m
	}
	m["TestClean"] = "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0 (check gated on GOMAXPROCS==1, always true on wasm)"

	// net/netip is interpreted on wasm only; native bridges it and drops the
	// test file (export_test.go internals).
	Incompat["net/netip"] = map[string]string{
		"TestParsePrefixAllocs": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestNoAllocs":          "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestAddrStringAllocs":  "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0/1",
	}
}
