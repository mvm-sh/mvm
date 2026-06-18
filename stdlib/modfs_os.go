package stdlib

import (
	"io/fs"
	"os"
	"reflect"
	"strings"

	"github.com/mvm-sh/mvm/vm"
)

var moduleFS fs.FS // "modfs/" root that mvm's runtime.Caller reports for module-sourced frames.

// RegisterModuleFS installs the module filesystem consulted by the os patcher
// for "modfs/"-prefixed paths. Call once at startup; nil leaves os untouched.
func RegisterModuleFS(fsys fs.FS) { moduleFS = fsys }

const modfsPrefix = "modfs/"

func modfsSub(name string) (string, bool) {
	if moduleFS == nil || !strings.HasPrefix(name, modfsPrefix) {
		return "", false
	}
	return strings.TrimPrefix(name, modfsPrefix), true
}

// ReadFileModfs reads name from the module FS when it carries the synthetic
// "modfs/" root, else from the host os.
func ReadFileModfs(name string) ([]byte, error) {
	if sub, ok := modfsSub(name); ok {
		return fs.ReadFile(moduleFS, sub)
	}
	return os.ReadFile(name)
}

func init() {
	RegisterPackagePatcher("os", patchOsModfs)
}

func patchOsModfs(_ *vm.Machine, values map[string]vm.Value) {
	values["ReadFile"] = vm.FromReflect(reflect.ValueOf(ReadFileModfs))
	values["ReadDir"] = vm.FromReflect(reflect.ValueOf(func(name string) ([]os.DirEntry, error) {
		if sub, ok := modfsSub(name); ok {
			return fs.ReadDir(moduleFS, sub)
		}
		return os.ReadDir(name)
	}))
	values["Stat"] = vm.FromReflect(reflect.ValueOf(func(name string) (os.FileInfo, error) {
		if sub, ok := modfsSub(name); ok {
			return fs.Stat(moduleFS, sub)
		}
		return os.Stat(name)
	}))
}
