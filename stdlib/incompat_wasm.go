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
}
