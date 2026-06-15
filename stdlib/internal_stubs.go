package stdlib

import (
	"bytes"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// Stub bindings for std-internal packages that external stdlib test files
// import but mvm cannot bridge for real: they live under the std module's
// internal/ tree, unreachable from this module. Without these, every
// stdlib test file importing one is dropped by loadBridgedTestSources
// (see goparser/import.go), which for many packages means no tests run.
//
// The stubs assume a friendly dev machine: sanitizers off, optimizations
// on, nothing flaky-skipped, exec and source available. That maximizes
// the set of stdlib tests that actually run. Tests gated on a sanitizer
// being *on* simply don't exercise their sanitizer-only path, which is
// the correct behavior for an interpreter that has no sanitizer.
func init() {
	Values["internal/asan"] = map[string]reflect.Value{
		"Enabled": reflect.ValueOf(false),
	}
	Values["internal/msan"] = map[string]reflect.Value{
		"Enabled": reflect.ValueOf(false),
	}
	Values["internal/race"] = map[string]reflect.Value{
		"Enabled": reflect.ValueOf(false),
		"Errors":  reflect.ValueOf(func() int { return 0 }),
	}
	// internal/goversion.Version: in-development Go 1.x minor, read by go/types
	// api_test.go. Tracks GOROOT.
	Values["internal/goversion"] = map[string]reflect.Value{
		"Version": reflect.ValueOf(26),
	}
	Values["internal/sysinfo"] = map[string]reflect.Value{
		"CPUName": reflect.ValueOf(func() string { return "" }),
	}
	// internal/reflectlite is a real subset of reflect (used by errors/wrap.go
	// when the errors package is loaded from source, e.g. `mvm test errors`).
	// Unlike the stubs above, this is a faithful re-export of reflect, not a
	// no-op shim. reflectlite.Ptr is the deprecated alias; reflect.Ptr exists.
	Values["internal/reflectlite"] = map[string]reflect.Value{
		"TypeOf":    reflect.ValueOf(reflect.TypeOf),
		"ValueOf":   reflect.ValueOf(reflect.ValueOf),
		"Type":      reflect.ValueOf((*reflect.Type)(nil)),
		"Value":     reflect.ValueOf((*reflect.Value)(nil)),
		"Kind":      reflect.ValueOf((*reflect.Kind)(nil)),
		"Ptr":       reflect.ValueOf(reflect.Pointer),
		"Interface": reflect.ValueOf(reflect.Interface),
	}
	Values["internal/testenv"] = map[string]reflect.Value{
		"Builder":               reflect.ValueOf(func() string { return "" }),
		"GOROOT":                reflect.ValueOf(func(testing.TB) string { return findGoroot() }),
		"GoToolPath":            reflect.ValueOf(func(testing.TB) string { return findGoroot() + "/bin/go" }),
		"GoTool":                reflect.ValueOf(func() (string, error) { return findGoroot() + "/bin/go", nil }),
		"HasGoBuild":            reflect.ValueOf(func() bool { return true }),
		"MustHaveGoBuild":       reflect.ValueOf(func(testing.TB) {}),
		"MustHaveGoRun":         reflect.ValueOf(func(testing.TB) {}),
		"OptimizationOff":       reflect.ValueOf(func() bool { return false }),
		"SkipIfOptimizationOff": reflect.ValueOf(func(testing.TB) {}),
		"SkipFlaky":             reflect.ValueOf(func(testing.TB, int) {}),
		"SkipIfShortAndSlow":    reflect.ValueOf(func(testing.TB) {}),
		"MustHaveExec":          reflect.ValueOf(func(testing.TB) {}),
		"MustHaveSource":        reflect.ValueOf(func(testing.TB) {}),
		// Tests using Executable() re-exec the test binary, which mvm can't
		// reproduce; skip them rather than fail with a bogus exec target.
		"Executable": reflect.ValueOf(func(tb testing.TB) string {
			tb.Skip("mvm test: testenv.Executable unsupported (no re-exec)")
			return ""
		}),
		"CleanCmdEnv": reflect.ValueOf(func(cmd *exec.Cmd) *exec.Cmd { return cmd }),
		"Command": reflect.ValueOf(func(_ testing.TB, name string, args ...string) *exec.Cmd {
			return exec.Command(name, args...)
		}),
	}
	// internal/bytealg provides assembly-accelerated byte/string scanning used
	// by source-loaded mirror packages (e.g. path imports it for IndexByteString
	// and LastIndexByteString). It is unreachable for a real bridge under
	// internal/, so re-export the equivalent bytes/strings helpers. The generic
	// helpers (HashStr, IndexRabinKarp) cannot be reflect-bridged and are omitted.
	Values["internal/bytealg"] = map[string]reflect.Value{
		"IndexByte":           reflect.ValueOf(bytes.IndexByte),
		"IndexByteString":     reflect.ValueOf(strings.IndexByte),
		"LastIndexByte":       reflect.ValueOf(bytes.LastIndexByte),
		"LastIndexByteString": reflect.ValueOf(strings.LastIndexByte),
		"Index":               reflect.ValueOf(bytes.Index),
		"IndexString":         reflect.ValueOf(strings.Index),
		"Compare":             reflect.ValueOf(bytes.Compare),
		"CompareString":       reflect.ValueOf(strings.Compare),
		"Equal":               reflect.ValueOf(bytes.Equal),
		"Count":               reflect.ValueOf(func(b []byte, c byte) int { return bytes.Count(b, []byte{c}) }),
		"CountString":         reflect.ValueOf(func(s string, c byte) int { return strings.Count(s, string(c)) }),
	}
}
