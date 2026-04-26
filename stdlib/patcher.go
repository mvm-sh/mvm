package stdlib

import "github.com/mvm-sh/mvm/vm"

// PackagePatcher mutates a package's exported values at interpreter
// startup. m is the live VM; values is the package's vm.Value symbol
// map (the same map the interpreter resolves imports against).
type PackagePatcher func(m *vm.Machine, values map[string]vm.Value)

// patchers is append-only and populated exclusively from init()
// functions, so no locking is needed: all writes happen before main
// starts and all reads happen after.
var patchers = map[string][]PackagePatcher{}

// RegisterPackagePatcher adds fn to the patcher list for importPath.
// Intended for init() in side-effect intercept packages such as
// stdlib/jsonx, so the interpreter itself needs no knowledge of them.
func RegisterPackagePatcher(importPath string, fn PackagePatcher) {
	patchers[importPath] = append(patchers[importPath], fn)
}

// PackagePatchers returns the registered patchers keyed by import path.
// Callers must not mutate the returned map.
func PackagePatchers() map[string][]PackagePatcher { return patchers }
