package stdlib

import (
	"os/exec"
	"reflect"
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
	Values["internal/sysinfo"] = map[string]reflect.Value{
		"CPUName": reflect.ValueOf(func() string { return "" }),
	}
	Values["internal/testenv"] = map[string]reflect.Value{
		"Builder":               reflect.ValueOf(func() string { return "" }),
		"GOROOT":                reflect.ValueOf(func(testing.TB) string { return findGoroot() }),
		"OptimizationOff":       reflect.ValueOf(func() bool { return false }),
		"SkipIfOptimizationOff": reflect.ValueOf(func(testing.TB) {}),
		"SkipFlaky":             reflect.ValueOf(func(testing.TB, int) {}),
		"SkipIfShortAndSlow":    reflect.ValueOf(func(testing.TB) {}),
		"MustHaveExec":          reflect.ValueOf(func(testing.TB) {}),
		"MustHaveSource":        reflect.ValueOf(func(testing.TB) {}),
		"CleanCmdEnv":           reflect.ValueOf(func(cmd *exec.Cmd) *exec.Cmd { return cmd }),
		"Command": reflect.ValueOf(func(_ testing.TB, name string, args ...string) *exec.Cmd {
			return exec.Command(name, args...)
		}),
	}
}
